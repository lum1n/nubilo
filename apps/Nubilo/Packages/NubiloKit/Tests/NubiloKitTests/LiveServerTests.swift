import Foundation
import XCTest
@testable import NubiloKit

/// End-to-end against a real server. Skipped unless NUBILO_TEST_URL (an
/// http://127.0.0.1 loopback server) and NUBILO_TEST_INVITE (an admin invite
/// code) are set, e.g. by scripts/swift-live-test.sh.
final class LiveServerTests: XCTestCase {
    func testJoinUploadAndBrowse() async throws {
        let env = ProcessInfo.processInfo.environment
        guard let base = env["NUBILO_TEST_URL"].flatMap(URL.init(string:)), let invite = env["NUBILO_TEST_INVITE"] else {
            throw XCTSkip("set NUBILO_TEST_URL and NUBILO_TEST_INVITE")
        }
        let key = Ed25519Key()
        let paired = try await Onboarding.join(
            link: SetupLink(server: base, invite: invite), username: "swift\(Int.random(in: 1000...9999))",
            displayName: "Swift Test", deviceName: "Linux CI", key: key, keyTag: "test")
        let c = NubiloClient(baseURL: base, signer: RequestSigner(deviceID: paired.deviceID, key: key))

        let me = try await c.me()
        XCTAssertEqual(me.user.username, paired.username)
        XCTAssertTrue(me.urls["caldav"]?.hasSuffix("/caldav/u/\(paired.username)/") ?? false)
        XCTAssertTrue(me.user.isAdmin)

        // Photo backup protocol with an upload token, as the iOS app does in the background.
        let jpeg = try XCTUnwrap(Data(base64Encoded: tinyJPEG))
        let hash = sha256Hex(jpeg)
        let token = try await c.uploadToken()
        let before = try await c.missingPhotos(hashes: [hash], auth: .bearer(token.token))
        XCTAssertEqual(before, [hash])
        let put = try await c.putBlob(jpeg, auth: .bearer(token.token))
        XCTAssertEqual(put.blobId, hash)
        let photo = try await c.commitPhoto(.init(blobId: hash, name: "IMG_0001.JPG", takenAt: 1_700_000_000_000, localId: "A/L0/001"), auth: .bearer(token.token))
        let after = try await c.missingPhotos(hashes: [hash])
        XCTAssertEqual(after, [])
        let page = try await c.photos()
        XCTAssertTrue(page.photos.contains { $0.id == photo.id })
        let original = try await c.photoData(photo.id, rendition: "original")
        XCTAssertEqual(original, jpeg)

        let album = try await c.createAlbum(name: "Swift")
        _ = try await c.addToAlbum(album.id, photoIDs: [photo.id])
        let items = try await c.albumItems(album.id).items
        XCTAssertEqual(items.count, 1)

        let pw = try await c.createAppPassword(label: "Laptop", scope: "webdav")
        XCTAssertEqual(pw.username, paired.username)
        let link = try await c.createProfile()
        XCTAssertTrue(link.url.contains("/api/v1/profile/"))

        let inv = try await c.createInvite(.init(role: "member"))
        XCTAssertTrue(inv.setupUrl.hasPrefix("nubilo://setup?"))
        XCTAssertNoThrow(try SetupLink(parsing: inv.url))
        let users = try await c.adminUsers()
        XCTAssertFalse(users.isEmpty)
        let cols = try await c.collections()
        XCTAssertFalse(cols.isEmpty)
    }
}

// 8×8 baseline JPEG.
let tinyJPEG = "/9j/4AAQSkZJRgABAQAAAQABAAD/2wBDAAgGBgcGBQgHBwcJCQgKDBQNDAsLDBkSEw8UHRofHh0aHBwgJC4nICIsIxwcKDcpLDAxNDQ0Hyc5PTgyPC4zNDL/wAALCAAIAAgBAREA/8QAFQABAQAAAAAAAAAAAAAAAAAAAAb/xAAUEAEAAAAAAAAAAAAAAAAAAAAA/9oACAEBAAA/AKp//9k="
