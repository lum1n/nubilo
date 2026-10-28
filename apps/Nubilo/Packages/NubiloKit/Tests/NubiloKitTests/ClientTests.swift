import Foundation
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif
import XCTest
@testable import NubiloKit

/// Replays canned responses and records requests.
final class FakeTransport: HTTPTransport, @unchecked Sendable {
    var routes: [String: (Int, String)] = [:]
    var seen: [URLRequest] = []

    func send(_ request: URLRequest) async throws -> (Data, HTTPURLResponse) {
        seen.append(request)
        let key = "\(request.httpMethod ?? "GET") \(request.url!.path)"
        let (status, body) = routes[key] ?? (404, "not found")
        let resp = HTTPURLResponse(url: request.url!, statusCode: status, httpVersion: "HTTP/1.1", headerFields: nil)!
        return (Data(body.utf8), resp)
    }
}

final class ClientTests: XCTestCase {
    func testJoinPinsServerKey() async throws {
        let device = Ed25519Key()
        let server = Ed25519Key()
        let t = FakeTransport()
        let serverID = server.publicKeyRaw.base64RawStd
        let challenge = Data(repeating: 7, count: 32)
        t.routes["POST /api/v1/invites/redeem"] = (200, #"{"pairing_id":"P1","challenge":"\#(challenge.base64RawStd)","server_id":"\#(serverID)","user":{"id":"U1","username":"carol","display_name":"Carol","role":"member"}}"#)
        t.routes["POST /api/v1/pair/complete"] = (200, #"{"device_id":"D1","user_id":"U1","created_at":1,"server_public_key":"\#(serverID)"}"#)
        let link = SetupLink(server: URL(string: "https://cloud.example.com")!, invite: "nbi-X", serverKey: server.publicKeyRaw.base64RawURL)
        let paired = try await Onboarding.join(link: link, username: "carol", displayName: "Carol", deviceName: "iPhone", key: device, keyTag: "k", transport: t)
        XCTAssertEqual(paired.deviceID, "D1")
        XCTAssertEqual(paired.username, "carol")
        // The completion carries a signature over the challenge.
        let body = try JSONSerialization.jsonObject(with: XCTUnwrap(t.seen[1].httpBody)) as! [String: String]
        let sig = try XCTUnwrap(Data(base64Lenient: body["signature"]!))
        XCTAssertTrue(verifyEd25519(publicKey: device.publicKeyRaw, signature: sig, message: challenge))
        // A different fingerprint aborts before pairing.
        let wrong = SetupLink(server: link.server, invite: "nbi-X", serverKey: Ed25519Key().publicKeyRaw.base64RawURL)
        do {
            _ = try await Onboarding.join(link: wrong, username: "carol", displayName: "", deviceName: "iPhone", key: device, keyTag: "k", transport: t)
            XCTFail("mismatched server key accepted")
        } catch NubiloError.serverKeyMismatch {}
    }

    func testSignedRequestsAndErrors() async throws {
        let t = FakeTransport()
        let key = Ed25519Key()
        let c = NubiloClient(baseURL: URL(string: "https://cloud.example.com")!, signer: RequestSigner(deviceID: "D1", key: key), transport: t)
        t.routes["GET /api/v1/photos"] = (200, #"{"photos":[{"id":"P","name":"a.jpg","taken_at_ms":1000,"thumb_hash":"h"}],"next":"P"}"#)
        let page = try await c.photos(after: "X", limit: 10)
        XCTAssertEqual(page.photos.first?.thumbHash, "h")
        XCTAssertEqual(page.next, "P")
        let auth = try XCTUnwrap(t.seen[0].value(forHTTPHeaderField: "Authorization"))
        XCTAssertTrue(auth.hasPrefix("Nubilo-Sig v1 device=D1,"))
        XCTAssertEqual(t.seen[0].url?.query, "limit=10&after=X")

        t.routes["POST /api/v1/me/passwords"] = (409, "too many app passwords\n")
        do {
            _ = try await c.createAppPassword(label: "x", scope: "webdav")
            XCTFail("expected error")
        } catch let e as NubiloError {
            XCTAssertEqual(e, .http(status: 409, message: "too many app passwords"))
        }

        t.routes["POST /api/v1/photos/check"] = (200, #"{"missing":["a","b"]}"#)
        let missing = try await c.missingPhotos(hashes: ["a", "b", "c"], auth: .bearer("nbu_x"))
        XCTAssertEqual(missing, ["a", "b"])
        XCTAssertEqual(t.seen.last?.value(forHTTPHeaderField: "Authorization"), "Bearer nbu_x")
    }

    func testThumbnailRequest() async throws {
        let t = FakeTransport()
        let c = NubiloClient(baseURL: URL(string: "https://cloud.example.com")!, signer: RequestSigner(deviceID: "D1", key: Ed25519Key()), transport: t)
        let req = try c.thumbnailRequest(objectID: "OBJ", size: 180)
        XCTAssertEqual(req.httpMethod, "POST")
        XCTAssertEqual(req.url?.path, "/sync/v1/thumb")
        XCTAssertEqual(req.value(forHTTPHeaderField: "Content-Type"), "application/json")
        XCTAssertEqual(req.httpBody.map { String(decoding: $0, as: UTF8.self) }, #"{"id":"OBJ","size":180}"#)
        XCTAssertTrue(req.value(forHTTPHeaderField: "Authorization")?.hasPrefix("Nubilo-Sig v1 device=D1,") ?? false)

        let fs = FilesSession(client: c)
        t.routes["POST /sync/v1/thumb"] = (200, "\u{FF}jpeg")
        let data = try await fs.thumbnail(of: "o:OBJ", size: 64)
        XCTAssertEqual(data, Data("\u{FF}jpeg".utf8))
        for (status, body, want) in [(415, "not an image", FilesError.noThumbnail), (404, "not found", .notFound),
                                     (404, "404 page not found", .noThumbnail), (413, "too large", .noThumbnail)] {
            t.routes["POST /sync/v1/thumb"] = (status, body)
            do {
                _ = try await fs.thumbnail(of: "o:OBJ", size: 64)
                XCTFail("\(status) accepted")
            } catch let e as FilesError {
                XCTAssertEqual(e, want, "\(status) \(body)")
            }
        }
        do {
            _ = try await fs.thumbnail(of: "c:FOLDER", size: 64)
            XCTFail("folder thumbnail")
        } catch FilesError.noThumbnail {}
    }

    func testThumbnailCandidates() {
        XCTAssertTrue(RemoteItem.mayHaveThumbnail(name: "IMG_1.JPG", mime: nil))
        XCTAssertTrue(RemoteItem.mayHaveThumbnail(name: "scan", mime: "image/png"))
        XCTAssertFalse(RemoteItem.mayHaveThumbnail(name: "IMG_2.HEIC", mime: "image/heic"))
        XCTAssertFalse(RemoteItem.mayHaveThumbnail(name: "notes.txt", mime: "image/png"))
        XCTAssertFalse(RemoteItem.mayHaveThumbnail(name: "clip", mime: "video/mp4"))
    }
}
