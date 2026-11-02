import Foundation
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif
import XCTest
@testable import NubiloKit

final class PhotoIndexTests: XCTestCase {
    func client(_ t: FakeTransport) -> NubiloClient {
        NubiloClient(baseURL: URL(string: "https://cloud.example.com")!, signer: RequestSigner(deviceID: "D1", key: Ed25519Key()), transport: t)
    }

    func testIndexSendsETagAndReadsNotModified() async throws {
        let t = FakeTransport()
        let c = client(t)
        t.routes["GET /api/v1/photos/index"] = (200, #"{"photos":[{"id":"P","taken_at_ms":5,"thumb_hash":"t","checksum":"c"}]}"#)
        t.headers["GET /api/v1/photos/index"] = ["ETag": #""abc""#]
        guard case let .photos(photos, etag) = try await c.photoIndex(etag: nil) else { return XCTFail("expected photos") }
        XCTAssertEqual(photos.map(\.id), ["P"])
        XCTAssertEqual(etag, #""abc""#)
        XCTAssertNil(t.seen[0].value(forHTTPHeaderField: "If-None-Match"))
        XCTAssertEqual(t.seen[0].cachePolicy, .reloadIgnoringLocalCacheData)
        XCTAssertNotNil(t.seen[0].value(forHTTPHeaderField: "Authorization"))

        t.routes["GET /api/v1/photos/index"] = (304, "")
        guard case .notModified = try await c.photoIndex(etag: #""abc""#) else { return XCTFail("expected 304") }
        XCTAssertEqual(t.seen[1].value(forHTTPHeaderField: "If-None-Match"), #""abc""#)
    }

    func testIndexMissingOnOldServers() async throws {
        let t = FakeTransport()
        do {
            _ = try await client(t).albumIndex("A1", etag: nil)
            XCTFail("expected PhotoIndexUnavailable")
        } catch is PhotoIndexUnavailable {}
        XCTAssertEqual(t.seen[0].url?.path, "/api/v1/albums/A1/index")

        t.routes["GET /api/v1/photos/index"] = (500, "boom")
        do {
            _ = try await client(t).photoIndex(etag: nil)
            XCTFail("expected error")
        } catch let e as NubiloError {
            XCTAssertEqual(e.status, 500)
        }
    }

    func testRenditionPathsCarryTheContentHash() {
        var p = Photo(id: "P1", checksum: "orig")
        XCTAssertEqual(NubiloClient.renditionPath(p, .thumb), "/api/v1/photos/P1/thumb?v=orig")
        XCTAssertTrue(p.fallsBackToOriginal(.thumb))
        p.thumbHash = "th"
        p.previewHash = "pv"
        XCTAssertEqual(NubiloClient.renditionPath(p, .thumb), "/api/v1/photos/P1/thumb?v=th")
        XCTAssertEqual(NubiloClient.renditionPath(p, .preview), "/api/v1/photos/P1/preview?v=pv")
        XCTAssertEqual(NubiloClient.renditionPath(p, .original), "/api/v1/photos/P1/original?v=orig")
        XCTAssertFalse(p.fallsBackToOriginal(.thumb))
        XCTAssertEqual(NubiloClient.renditionPath(Photo(id: "P2"), .preview), "/api/v1/photos/P2/preview")
    }

    func testRenditionRequestSignsTheQuery() async throws {
        let t = FakeTransport()
        t.routes["GET /api/v1/photos/P1/thumb"] = (200, "jpeg")
        let data = try await client(t).photoData(Photo(id: "P1", thumbHash: "th"), rendition: .thumb)
        XCTAssertEqual(data, Data("jpeg".utf8))
        XCTAssertEqual(t.seen[0].url?.query, "v=th")
    }

    func testIsVideo() {
        XCTAssertTrue(Photo(id: "a", kind: "video").isVideo)
        XCTAssertTrue(Photo(id: "a", mime: "video/quicktime").isVideo)
        XCTAssertFalse(Photo(id: "a", mime: "image/heic", kind: "live").isVideo)
    }

    func testCommitLeavesOutUnsetFields() throws {
        let json = try NubiloClient.encoder.encode(NubiloClient.Commit(blobId: "b", name: "n"))
        XCTAssertEqual(String(decoding: json, as: UTF8.self), #"{"blob_id":"b","name":"n"}"#)
        let full = try NubiloClient.encoder.encode(NubiloClient.Commit(blobId: "b", name: "n", thumbBlob: "t", previewBlob: "p"))
        XCTAssertEqual(String(decoding: full, as: UTF8.self), #"{"blob_id":"b","name":"n","preview_blob":"p","thumb_blob":"t"}"#)
    }

    func testAttachDerivatives() async throws {
        let t = FakeTransport()
        t.routes["POST /api/v1/photos/P1/derivatives"] = (200, #"{"id":"P1","thumb_hash":"t"}"#)
        let p = try await client(t).attachDerivatives("P1", thumbBlob: "t", previewBlob: nil)
        XCTAssertEqual(p.thumbHash, "t")
        XCTAssertEqual(String(decoding: try XCTUnwrap(t.seen[0].httpBody), as: UTF8.self), #"{"thumb_blob":"t"}"#)
    }

    func testCacheRoundTripPerKey() throws {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: dir) }
        let cache = PhotoIndexCache(directory: dir)
        XCTAssertNil(cache.load(PhotoIndexCache.libraryKey))
        let entry = PhotoIndexCache.Entry(etag: #""e1""#, photos: [Photo(id: "P", takenAtMs: 9, thumbHash: "t")])
        cache.save(entry, as: PhotoIndexCache.libraryKey)
        XCTAssertEqual(cache.load(PhotoIndexCache.libraryKey), entry)
        XCTAssertNil(cache.load(PhotoIndexCache.albumKey("A")))
        cache.remove(PhotoIndexCache.libraryKey)
        XCTAssertNil(cache.load(PhotoIndexCache.libraryKey))
    }

    func testCacheFolderDependsOnServerAndDevice() {
        let a = NubiloClient(baseURL: URL(string: "https://a.example")!, signer: RequestSigner(deviceID: "D1", key: Ed25519Key()))
        let b = NubiloClient(baseURL: URL(string: "https://a.example")!, signer: RequestSigner(deviceID: "D2", key: Ed25519Key()))
        XCTAssertNotEqual(PhotoIndexCache.standard(for: a).directory, PhotoIndexCache.standard(for: b).directory)
    }
}
