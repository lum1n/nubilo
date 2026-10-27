import Foundation
import XCTest
@testable import NubiloKit

final class CalendarMirrorTests: XCTestCase {
    let utc = TimeZone(identifier: "UTC")!

    func date(_ s: String) -> Date {
        let f = ISO8601DateFormatter()
        return f.date(from: s)!
    }

    func testULID() {
        let a = ULID.make(date: date("2026-10-27T12:00:00Z"))
        let b = ULID.make(date: date("2026-10-27T12:00:01Z"))
        XCTAssertEqual(a.count, 26)
        XCTAssertTrue(a.allSatisfy { "0123456789ABCDEFGHJKMNPQRSTVWXYZ".contains($0) })
        XCTAssertLessThan(a.prefix(10), b.prefix(10))
        XCTAssertTrue("01234567".contains(a.first!))
    }

    func testTimedEventICS() {
        let e = MirrorEvent(key: "x@2026-10-27", title: "Standup; team, all", notes: "line1\nline2", location: "Room 1",
                            start: date("2026-10-27T09:00:00Z"), end: date("2026-10-27T09:15:00Z"),
                            status: "CONFIRMED", busy: false,
                            organizer: MirrorPerson(name: "Ada \"L\"", email: "ada@example.com"),
                            attendees: [MirrorPerson(name: nil, email: "bob@example.com", status: "ACCEPTED")],
                            alarms: [MirrorAlarm(offset: -900), MirrorAlarm(date: date("2026-10-27T08:00:00Z"))])
        let ics = ICSWriter.calendar(for: e)
        XCTAssertTrue(ics.hasSuffix("END:VCALENDAR\r\n"))
        XCTAssertFalse(ics.replacingOccurrences(of: "\r\n", with: "").contains("\n"))
        for want in ["UID:" + e.uid, "DTSTART:20261027T090000Z", "DTEND:20261027T091500Z",
                     "SUMMARY:Standup\\; team\\, all", "DESCRIPTION:line1\\nline2", "TRANSP:TRANSPARENT",
                     "STATUS:CONFIRMED", "ORGANIZER;CN=\"Ada 'L'\":mailto:ada@example.com",
                     "ATTENDEE;PARTSTAT=ACCEPTED:mailto:bob@example.com", "TRIGGER:-PT15M",
                     "TRIGGER;VALUE=DATE-TIME:20261027T080000Z", "DTSTAMP:19700101T000000Z"] {
            XCTAssertTrue(ics.contains(want), "missing \(want) in\n\(ics)")
        }
        // Same input, same bytes: unchanged events are not pushed again.
        XCTAssertEqual(ics, ICSWriter.calendar(for: e))
    }

    func testAllDayEndIsExclusive() {
        var cal = Calendar(identifier: .gregorian)
        cal.timeZone = utc
        let start = cal.date(from: DateComponents(year: 2026, month: 12, day: 24))!
        // EventKit ends a one-day event at 23:59:59 the same day.
        let end = cal.date(from: DateComponents(year: 2026, month: 12, day: 24, hour: 23, minute: 59, second: 59))!
        let ics = ICSWriter.calendar(for: MirrorEvent(key: "k", title: "Holiday", start: start, end: end, allDay: true, timeZone: utc))
        XCTAssertTrue(ics.contains("DTSTART;VALUE=DATE:20261224"))
        XCTAssertTrue(ics.contains("DTEND;VALUE=DATE:20261225"))
    }

    func testFoldingKeepsUTF8Whole() {
        let long = String(repeating: "æøå ", count: 60)
        let ics = ICSWriter.calendar(for: MirrorEvent(key: "k", title: long, start: date("2026-01-01T00:00:00Z"), end: date("2026-01-01T01:00:00Z")))
        for line in ics.components(separatedBy: "\r\n") {
            XCTAssertLessThanOrEqual(line.utf8.count, 75, line)
        }
        let unfolded = ics.replacingOccurrences(of: "\r\n ", with: "")
        XCTAssertTrue(unfolded.contains("SUMMARY:" + long))
    }

    func testDurations() {
        XCTAssertEqual(ICSWriter.duration(-900), "-PT15M")
        XCTAssertEqual(ICSWriter.duration(-86400), "-P1D")
        XCTAssertEqual(ICSWriter.duration(-90000), "-P1DT1H")
        XCTAssertEqual(ICSWriter.duration(0), "PT0S")
    }

    func testMirrorNameNeverTakesOverOwnCalendar() throws {
        func col(_ name: String, mirror: Bool = false, device: String? = nil) throws -> SyncCollection {
            var meta = "{}"
            if mirror { meta = "{\"mirror\":true,\"mirror_device\":\"\(device ?? "")\"}" }
            let json = "{\"id\":\"\(name)\",\"kind\":\"calendar\",\"name\":\"\(name)\",\"access\":\"owner\",\"metadata\":\(meta)}"
            let d = JSONDecoder()
            d.keyDecodingStrategy = .convertFromSnakeCase
            return try d.decode(SyncCollection.self, from: Data(json.utf8))
        }
        let cols = [try col("Calendar"), try col("Work", mirror: true, device: "me"), try col("Team", mirror: true, device: "other-mac")]
        XCTAssertEqual(CalendarMirrorEngine.mirrorName(title: "Calendar", deviceID: "me", cols: cols, used: []), "Calendar (Mac)")
        XCTAssertEqual(CalendarMirrorEngine.mirrorName(title: "Work", deviceID: "me", cols: cols, used: []), "Work")
        XCTAssertEqual(CalendarMirrorEngine.mirrorName(title: "Team", deviceID: "me", cols: cols, used: []), "Team (Mac)")
        XCTAssertEqual(CalendarMirrorEngine.mirrorName(title: "Calendar", deviceID: "me", cols: cols, used: ["Calendar (Mac)"]), "Calendar (Mac 2)")
        XCTAssertEqual(CalendarMirrorEngine.mirrorName(title: "A/B", deviceID: "me", cols: [], used: []), "A_B")
    }
}

/// A Calendar app with events that tests change between runs.
final class FakeCalendars: CalendarReading, @unchecked Sendable {
    var list: [MirrorCalendarInfo]
    var events: [String: [MirrorEvent]]
    var fail = false

    init(list: [MirrorCalendarInfo], events: [String: [MirrorEvent]]) {
        self.list = list
        self.events = events
    }

    func calendars() async throws -> [MirrorCalendarInfo] { list }

    func events(calendarID: String, from: Date, to: Date) async throws -> [MirrorEvent] {
        if fail { throw NubiloError.http(status: 0, message: "eventkit unavailable") }
        return events[calendarID] ?? []
    }
}

final class FilesChangesTests: XCTestCase {
    func collection(_ id: String, parent: String? = nil, name: String, revision: UInt64 = 1, access: String = "owner") throws -> SyncCollection {
        var json = "{\"id\":\"\(id)\",\"kind\":\"files\",\"name\":\"\(name)\",\"revision\":\(revision),\"access\":\"\(access)\",\"owner\":\"ann\""
        if let parent { json += ",\"parent_id\":\"\(parent)\"" }
        json += "}"
        let d = JSONDecoder()
        d.keyDecodingStrategy = .convertFromSnakeCase
        return try d.decode(SyncCollection.self, from: Data(json.utf8))
    }

    func page(_ json: String) throws -> SyncChangesPage {
        let d = JSONDecoder()
        d.keyDecodingStrategy = .convertFromSnakeCase
        return try d.decode(SyncChangesPage.self, from: Data(json.utf8))
    }

    func testFolderAndFileChanges() throws {
        let before = FilesAnchor(seq: 10, folders: FilesSession.stamps([try collection("A", name: "Files"), try collection("B", parent: "A", name: "Old")]))
        let now = [try collection("A", name: "Files"), try collection("B", parent: "A", name: "New", revision: 2),
                   try collection("S", name: "Shared", access: "ro")]
        let p = try page("""
        {"changes":[
          {"seq":11,"object_id":"o1","collection_id":"B","kind":"file","op":"create","revision":1,"content_hash":"h1","size":3,"metadata":{"name":"a.txt"}},
          {"seq":12,"object_id":"o1","collection_id":"B","kind":"file","op":"update","revision":2,"content_hash":"h2","size":4,"metadata":{"name":"b.txt"}},
          {"seq":13,"object_id":"o2","collection_id":"A","op":"delete","revision":3,"content_hash":"","deleted":true},
          {"seq":14,"object_id":"ev","collection_id":"CAL","kind":"event","op":"create","revision":1,"content_hash":"x"}
        ],"next_seq":14,"has_more":false}
        """)
        let c = FilesSession.changes(previous: before, folders: now, page: p)
        XCTAssertEqual(c.anchor.seq, 14)
        XCTAssertEqual(Set(c.updated.filter { $0.kind == .folder }.map(\.id)), ["c:B", "c:S"])
        XCTAssertEqual(c.updated.first { $0.id == "c:S" }?.name, "Shared (ann)")
        XCTAssertEqual(c.updated.first { $0.id == "c:S" }?.writable, false)
        let file = try XCTUnwrap(c.updated.first { $0.id == "o:o1" })
        XCTAssertEqual(file.name, "b.txt")
        XCTAssertEqual(file.contentHash, "h2")
        XCTAssertEqual(c.deleted, ["o:o2"])

        // B disappears: the folder is reported deleted.
        let gone = FilesSession.changes(previous: c.anchor, folders: [try collection("A", name: "Files")],
                                        page: try page("{\"changes\":[],\"next_seq\":14,\"has_more\":false}"))
        XCTAssertEqual(Set(gone.deleted), ["c:B", "c:S"])
    }
}
