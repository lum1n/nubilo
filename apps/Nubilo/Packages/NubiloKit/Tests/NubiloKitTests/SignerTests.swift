import Foundation
import XCTest
@testable import NubiloKit

struct Vector: Decodable {
    var name, seed, deviceId: String
    var ts: Int64
    var nonce, method, path, body, canonical, signature, authorization: String
}

final class SignerTests: XCTestCase {
    /// apps/Nubilo/Tests/Fixtures/signing_vectors.json, written by `go test ./internal/auth -update-vectors`.
    func loadVectors() throws -> [Vector] {
        let url = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().deletingLastPathComponent()
            .appendingPathComponent("Tests/Fixtures/signing_vectors.json")
        let d = JSONDecoder()
        d.keyDecodingStrategy = .convertFromSnakeCase
        return try d.decode([Vector].self, from: Data(contentsOf: url))
    }

    func testCanonicalAndSignaturesMatchGo() throws {
        let vectors = try loadVectors()
        XCTAssertFalse(vectors.isEmpty)
        for v in vectors {
            let key = try Ed25519Key(rawRepresentation: XCTUnwrap(Data(base64Encoded: v.seed)))
            let body = try XCTUnwrap(Data(base64Encoded: v.body))
            let canon = RequestSigner.canonical(deviceID: v.deviceId, ts: v.ts, nonce: v.nonce, method: v.method, path: v.path, body: body)
            XCTAssertEqual(String(decoding: canon, as: UTF8.self), v.canonical, v.name)
            // The Go signature verifies against our public key (same key derivation)…
            let goSig = try XCTUnwrap(Data(base64Lenient: v.signature))
            XCTAssertTrue(verifyEd25519(publicKey: key.publicKeyRaw, signature: goSig, message: canon), v.name)
            // …and ours verifies too. CryptoKit randomizes Ed25519 signatures, so
            // compare the header structure rather than signature bytes.
            let header = try RequestSigner(deviceID: v.deviceId, key: key).authorization(method: v.method, path: v.path, body: body, ts: v.ts, nonce: v.nonce)
            let prefix = v.authorization.components(separatedBy: "sig=")[0]
            XCTAssertTrue(header.hasPrefix(prefix), v.name)
            let ours = try XCTUnwrap(Data(base64Lenient: String(header.dropFirst(prefix.count + 4))))
            XCTAssertTrue(verifyEd25519(publicKey: key.publicKeyRaw, signature: ours, message: canon), v.name)
            XCTAssertFalse(header.hasSuffix("="), "signature must be unpadded base64")
        }
    }

    func testSetupLinks() throws {
        let a = try SetupLink(parsing: "nubilo://setup?url=https%3A%2F%2Fcloud.example.com&invite=nbi-ABCDEFGHJKMNPQRSTVWX&fp=abc_-")
        XCTAssertEqual(a.server.absoluteString, "https://cloud.example.com")
        XCTAssertEqual(a.invite, "nbi-ABCDEFGHJKMNPQRSTVWX")
        XCTAssertEqual(a.serverKey, "abc_-")
        let b = try SetupLink(parsing: "https://cloud.example.com:8443/join#nbi-ABCDEFGHJKMNPQRSTVWX")
        XCTAssertEqual(b.server.absoluteString, "https://cloud.example.com:8443")
        XCTAssertEqual(b.invite, "nbi-ABCDEFGHJKMNPQRSTVWX")
        XCTAssertNil(b.serverKey)
        XCTAssertThrowsError(try SetupLink(parsing: "nubilo://setup?url=http%3A%2F%2Finsecure&invite=x"))
        XCTAssertThrowsError(try SetupLink(parsing: "https://cloud.example.com/other#x"))
    }

    func testStreamingHashMatches() throws {
        let data = Data((0..<3_000_000).map { UInt8(truncatingIfNeeded: $0 &* 31) })
        let url = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try data.write(to: url)
        defer { try? FileManager.default.removeItem(at: url) }
        XCTAssertEqual(try sha256Hex(fileAt: url, chunkSize: 4096), sha256Hex(data))
    }
}
