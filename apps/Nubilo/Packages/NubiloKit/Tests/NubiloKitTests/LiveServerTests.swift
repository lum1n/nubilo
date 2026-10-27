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

final class LiveCalendarMirrorTests: XCTestCase {
    func testMirrorRoundTrip() async throws {
        let env = ProcessInfo.processInfo.environment
        guard let base = env["NUBILO_TEST_URL"].flatMap(URL.init(string:)), let invite = env["NUBILO_TEST_INVITE_2"] else {
            throw XCTSkip("set NUBILO_TEST_URL and NUBILO_TEST_INVITE_2")
        }
        let key = Ed25519Key()
        let paired = try await Onboarding.join(
            link: SetupLink(server: base, invite: invite), username: "mirror\(Int.random(in: 1000...9999))",
            displayName: "Mirror Test", deviceName: "Mac", key: key, keyTag: "test")
        let c = NubiloClient(baseURL: base, signer: RequestSigner(deviceID: paired.deviceID, key: key))
        let now = Date()
        func ev(_ k: String, _ title: String, _ hours: Double) -> MirrorEvent {
            MirrorEvent(key: k, title: title, start: now.addingTimeInterval(hours * 3600), end: now.addingTimeInterval(hours * 3600 + 1800))
        }
        let fake = FakeCalendars(
            list: [MirrorCalendarInfo(id: "ex", title: "Work", color: "#0E61B9", account: "Exchange"),
                   MirrorCalendarInfo(id: "own", title: "Personal", color: nil, account: "iCloud")],
            events: ["ex": [ev("a", "Standup", 1), ev("b", "Review", 30)], "own": [ev("p", "Dentist", 5)]])
        let sels = [MirrorSelection(calendarID: "ex", title: "Work"), MirrorSelection(calendarID: "own", title: "Personal")]
        let stateURL = FileManager.default.temporaryDirectory.appendingPathComponent("mirror-\(UUID().uuidString).json")
        defer { try? FileManager.default.removeItem(at: stateURL) }

        var engine = CalendarMirrorEngine(stateURL: stateURL)
        var run = await engine.run(client: c, reader: fake, selections: sels, deviceID: paired.deviceID)
        XCTAssertEqual(run.errors, [])
        XCTAssertEqual(run.calendars.map(\.pushed), [2, 1])
        // The user's default "Personal" calendar is not taken over.
        XCTAssertEqual(run.calendars.map(\.serverName), ["Work", "Personal (Mac)"])
        let cols = try await c.syncCollections()
        let work = try XCTUnwrap(cols.first { $0.name == "Work" && $0.kind == "calendar" })
        XCTAssertEqual(work.calendar?.mirror, true)
        XCTAssertEqual(work.calendar?.mirrorDevice, paired.deviceID)
        XCTAssertEqual(work.calendar?.color, "#0E61B9")
        let personal = try XCTUnwrap(cols.first { $0.name == "Personal" && $0.kind == "calendar" })
        XCTAssertNotEqual(personal.calendar?.mirror, true)

        // Unchanged: nothing pushed.
        run = await engine.run(client: c, reader: fake, selections: sels, deviceID: paired.deviceID)
        XCTAssertEqual(run.calendars.map(\.pushed), [0, 0])

        // A failed listing deletes nothing.
        fake.fail = true
        run = await engine.run(client: c, reader: fake, selections: sels, deviceID: paired.deviceID)
        XCTAssertEqual(run.errors.count, 2)
        fake.fail = false
        var live = try await c.liveObjects(collectionID: work.id)
        XCTAssertEqual(live.count, 2)

        // Edit one, delete the other on the Mac.
        fake.events["ex"] = [ev("a", "Standup moved", 2)]
        run = await engine.run(client: c, reader: fake, selections: sels, deviceID: paired.deviceID)
        XCTAssertEqual(run.calendars.first?.pushed, 1)
        XCTAssertEqual(run.calendars.first?.deleted, 1)
        live = try await c.liveObjects(collectionID: work.id)
        XCTAssertEqual(live.count, 1)

        // This Mac lost its state: the copy is adopted, not duplicated.
        try? FileManager.default.removeItem(at: stateURL)
        engine = CalendarMirrorEngine(stateURL: stateURL)
        run = await engine.run(client: c, reader: fake, selections: sels, deviceID: paired.deviceID)
        XCTAssertEqual(run.errors, [])
        XCTAssertEqual(run.calendars.map(\.pushed), [0, 0])
        live = try await c.liveObjects(collectionID: work.id)
        XCTAssertEqual(live.count, 1)
    }
}

final class LiveFilesTests: XCTestCase {
    func testFilesRoundTrip() async throws {
        let env = ProcessInfo.processInfo.environment
        guard let base = env["NUBILO_TEST_URL"].flatMap(URL.init(string:)), let invite = env["NUBILO_TEST_INVITE_3"] else {
            throw XCTSkip("set NUBILO_TEST_URL and NUBILO_TEST_INVITE_3")
        }
        let key = Ed25519Key()
        let paired = try await Onboarding.join(
            link: SetupLink(server: base, invite: invite), username: "files\(Int.random(in: 1000...9999))",
            displayName: "Files Test", deviceName: "iPhone", key: key, keyTag: "test")
        let fs = FilesSession(client: NubiloClient(baseURL: base, signer: RequestSigner(deviceID: paired.deviceID, key: key)))

        let roots = try await fs.children(of: RemoteItem.rootID)
        let files = try XCTUnwrap(roots.first { $0.name == "Files" && $0.kind == .folder })
        let start = try await fs.currentAnchor()

        do {
            _ = try await fs.createFile(named: "loose.txt", in: RemoteItem.rootID, data: Data("x".utf8), mime: nil)
            XCTFail("file at the top level")
        } catch FilesError.filesNeedAFolder {}

        let docs = try await fs.createFolder(named: "Docs", in: files.id)
        let note = try await fs.createFile(named: "note.txt", in: docs.id, data: Data("hello".utf8), mime: "text/plain")
        XCTAssertEqual(note.parentID, docs.id)
        XCTAssertEqual(note.size, 5)
        let listed = try await fs.children(of: docs.id)
        XCTAssertEqual(listed.map(\.name), ["note.txt"])
        let body = try await fs.contents(of: note)
        XCTAssertEqual(String(decoding: body, as: UTF8.self), "hello")

        var changes = try await fs.changes(since: start)
        XCTAssertTrue(changes.updated.contains { $0.id == docs.id }, "new folder in changes")
        XCTAssertTrue(changes.updated.contains { $0.id == note.id }, "new file in changes")

        // Edit, rename and move the file; rename the folder.
        var mid = changes.anchor
        let edited = try await fs.updateFile(note, data: Data("hello again".utf8), name: "renamed.txt", parentID: files.id, mime: nil)
        XCTAssertEqual(edited.id, note.id)
        XCTAssertEqual(edited.parentID, files.id)
        XCTAssertEqual(edited.name, "renamed.txt")
        XCTAssertNotEqual(edited.contentHash, note.contentHash)
        let renamed = try await fs.updateFolder(docs, name: "Papers", parentID: nil)
        XCTAssertEqual(renamed.name, "Papers")
        changes = try await fs.changes(since: mid)
        let fileChange = try XCTUnwrap(changes.updated.first { $0.id == note.id })
        XCTAssertEqual(fileChange.name, "renamed.txt")
        XCTAssertEqual(fileChange.parentID, files.id)
        XCTAssertTrue(changes.updated.contains { $0.id == docs.id && $0.name == "Papers" })

        // Move the folder to the top level, then delete things.
        mid = changes.anchor
        let top = try await fs.updateFolder(renamed, name: nil, parentID: RemoteItem.rootID)
        XCTAssertEqual(top.parentID, RemoteItem.rootID)
        try await fs.delete(edited)
        try await fs.delete(top)
        changes = try await fs.changes(since: mid)
        XCTAssertTrue(changes.deleted.contains(note.id))
        XCTAssertTrue(changes.deleted.contains(docs.id))
        do {
            _ = try await fs.item(note.id)
            XCTFail("deleted file still found")
        } catch FilesError.notFound {}
    }
}
