import Foundation
import XCTest
@testable import NubiloKit

final class ServerUpdateTests: XCTestCase {
    func client(_ t: FakeTransport) -> NubiloClient {
        NubiloClient(baseURL: URL(string: "https://cloud.example.com")!, signer: RequestSigner(deviceID: "D1", key: Ed25519Key()), transport: t)
    }

    func testDecodesFullStatus() async throws {
        let t = FakeTransport()
        t.routes["GET /api/v1/admin/update"] = (200, """
        {"current":"0.8.0","available":{"version":"0.8.1","notes":"**Faster** photos","published_at":1762000000000},
         "auto":true,"state":"downloading","progress":0.4,"blocked":"","checked_at":1762000001000,"next_auto_at":1762030000000,
         "last":{"from":"0.7.0","to":"0.8.0","result":"updated","message":"","at":1761000000000}}
        """)
        let s = try await client(t).updateStatus()
        XCTAssertEqual(s.current, "0.8.0")
        XCTAssertEqual(s.available?.version, "0.8.1")
        XCTAssertEqual(s.available?.publishedAt, 1_762_000_000_000)
        XCTAssertEqual(s.progress, 0.4)
        XCTAssertTrue(s.isBusy)
        XCTAssertNil(s.blockedReason)
        XCTAssertEqual(s.nextAutoAt, 1_762_030_000_000)
        XCTAssertEqual(s.last?.succeeded, true)
    }

    func testNullsAndBlocked() async throws {
        let t = FakeTransport()
        t.routes["POST /api/v1/admin/update/check"] = (200, """
        {"current":"0.8.0","available":null,"auto":false,"state":"idle","blocked":"The relay must be updated first.",
         "checked_at":0,"next_auto_at":0,"last":{"from":"0.8.0","to":"0.8.1","result":"rolled_back","message":"did not start","at":1}}
        """)
        let s = try await client(t).checkForUpdate()
        XCTAssertNil(s.available)
        XCTAssertNil(s.progress)
        XCTAssertFalse(s.isBusy)
        XCTAssertEqual(s.blockedReason, "The relay must be updated first.")
        XCTAssertEqual(s.last?.succeeded, false)
    }

    func testSettingsBodyAndUnsupported() async throws {
        let t = FakeTransport()
        t.routes["PUT /api/v1/admin/update/settings"] = (200, #"{"current":"0.8.0","available":null,"auto":false,"state":"idle","last":null}"#)
        let s = try await client(t).setAutoUpdate(false)
        XCTAssertFalse(s.auto)
        let body = try JSONSerialization.jsonObject(with: XCTUnwrap(t.seen.last?.httpBody)) as? [String: Bool]
        XCTAssertEqual(body, ["auto": false])

        // No route: the fake answers 404, like a server without the updater.
        do {
            _ = try await client(t).installUpdate()
            XCTFail("expected unsupported")
        } catch let e as ServerUpdateUnsupported {
            XCTAssertEqual(e.status, 404)
        }
        t.routes["GET /api/v1/admin/update"] = (403, "forbidden")
        do {
            _ = try await client(t).updateStatus()
            XCTFail("expected unsupported")
        } catch let e as ServerUpdateUnsupported {
            XCTAssertEqual(e.status, 403)
        }
    }
}
