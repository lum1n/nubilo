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

final class CalendarMirrorSeriesTests: XCTestCase {
    let oslo = TimeZone(identifier: "Europe/Oslo")!
    let utc = TimeZone(identifier: "UTC")!
    // The engine's window for "now" = 2026-10-28.
    let from = ISO8601DateFormatter().date(from: "2026-09-28T12:00:00Z")!
    let to = ISO8601DateFormatter().date(from: "2027-10-28T12:00:00Z")!

    func at(_ y: Int, _ m: Int, _ d: Int, _ h: Int = 0, _ min: Int = 0, _ tz: TimeZone) -> Date {
        var cal = Calendar(identifier: .gregorian)
        cal.timeZone = tz
        return cal.date(from: DateComponents(year: y, month: m, day: d, hour: h, minute: min))!
    }

    /// What EventKit returns for a series: one event per occurrence in the window.
    func occurrences(_ series: String, _ starts: [Date], length: TimeInterval = 1800, rule: MirrorRecurrence,
                     seriesStart: Date?, title: String = "Standup", allDay: Bool = false, tz: TimeZone? = nil) -> [MirrorEvent] {
        starts.map { s in
            MirrorEvent(key: series + "@" + ICSWriter.utc(s), title: title, start: s, end: s.addingTimeInterval(length),
                        allDay: allDay, timeZone: tz ?? oslo, seriesID: series, occurrenceDate: s, recurrence: [rule],
                        seriesStart: seriesStart)
        }
    }

    func unfold(_ ics: String) -> String { ics.replacingOccurrences(of: "\r\n ", with: "") }

    func testRRULEStrings() {
        func rr(_ r: MirrorRecurrence, allDay: Bool = false) -> String? { MirrorSeries.rrule(r, allDay: allDay, tz: oslo) }
        XCTAssertEqual(rr(MirrorRecurrence(frequency: .daily)), "FREQ=DAILY")
        XCTAssertEqual(rr(MirrorRecurrence(frequency: .weekly, interval: 2, daysOfTheWeek: [MirrorWeekday(2), MirrorWeekday(4)],
                                           firstDayOfTheWeek: 1, count: 10)),
                       "FREQ=WEEKLY;INTERVAL=2;COUNT=10;BYDAY=MO,WE;WKST=SU")
        XCTAssertEqual(rr(MirrorRecurrence(frequency: .monthly, daysOfTheWeek: [MirrorWeekday(3, week: 2)])), "FREQ=MONTHLY;BYDAY=2TU")
        XCTAssertEqual(rr(MirrorRecurrence(frequency: .monthly, daysOfTheWeek: (2...6).map { MirrorWeekday($0) }, setPositions: [-1])),
                       "FREQ=MONTHLY;BYDAY=MO,TU,WE,TH,FR;BYSETPOS=-1")
        XCTAssertEqual(rr(MirrorRecurrence(frequency: .monthly, daysOfTheMonth: [1, -1])), "FREQ=MONTHLY;BYMONTHDAY=1,-1")
        XCTAssertEqual(rr(MirrorRecurrence(frequency: .yearly, daysOfTheWeek: [MirrorWeekday(5, week: 4)], monthsOfTheYear: [11])),
                       "FREQ=YEARLY;BYDAY=4TH;BYMONTH=11")
        // UNTIL: UTC for timed series (a bare date counts to the end of that day), DATE for all-day ones.
        let until = at(2026, 12, 31, 0, 0, oslo)
        XCTAssertEqual(rr(MirrorRecurrence(frequency: .weekly, until: until)), "FREQ=WEEKLY;UNTIL=20261231T225959Z")
        XCTAssertEqual(rr(MirrorRecurrence(frequency: .yearly, until: until), allDay: true), "FREQ=YEARLY;UNTIL=20261231")
        // Shapes that fall back to single occurrences.
        XCTAssertNil(rr(MirrorRecurrence(frequency: .yearly, weeksOfTheYear: [20])))
        XCTAssertNil(rr(MirrorRecurrence(frequency: .yearly, daysOfTheYear: [100])))
        XCTAssertNil(rr(MirrorRecurrence(frequency: .yearly, daysOfTheWeek: [MirrorWeekday(2, week: 20)])))
        XCTAssertNil(rr(MirrorRecurrence(frequency: .weekly, daysOfTheWeek: [MirrorWeekday(2, week: 1)])))
        XCTAssertNil(rr(MirrorRecurrence(frequency: .daily, daysOfTheWeek: [MirrorWeekday(2)])))
        XCTAssertNil(rr(MirrorRecurrence(frequency: .monthly, daysOfTheWeek: [MirrorWeekday(2)], daysOfTheMonth: [13])))
        XCTAssertNil(rr(MirrorRecurrence(frequency: .monthly, interval: 0)))
    }

    func testExpansion() {
        func days(_ r: MirrorRecurrence, _ start: Date, _ n: Int) -> [String] {
            let e = MirrorSeries.expand(r, start: start, tz: utc, through: start.addingTimeInterval(800 * 86400))!
            return e.dates.prefix(n).map { String(ICSWriter.utc($0).prefix(8)) }
        }
        // Last weekday of the month.
        XCTAssertEqual(days(MirrorRecurrence(frequency: .monthly, daysOfTheWeek: (2...6).map { MirrorWeekday($0) }, setPositions: [-1]),
                            at(2026, 1, 30, 9, 0, utc), 4), ["20260130", "20260227", "20260331", "20260430"])
        // The 31st skips short months.
        XCTAssertEqual(days(MirrorRecurrence(frequency: .monthly), at(2026, 1, 31, 9, 0, utc), 3), ["20260131", "20260331", "20260531"])
        // Second Tuesday; last Friday.
        XCTAssertEqual(days(MirrorRecurrence(frequency: .monthly, daysOfTheWeek: [MirrorWeekday(3, week: 2)]), at(2026, 1, 13, 9, 0, utc), 3),
                       ["20260113", "20260210", "20260310"])
        XCTAssertEqual(days(MirrorRecurrence(frequency: .monthly, daysOfTheWeek: [MirrorWeekday(6, week: -1)]), at(2026, 1, 30, 9, 0, utc), 2),
                       ["20260130", "20260227"])
        // Every other week on Monday and Wednesday, weeks starting Monday.
        XCTAssertEqual(days(MirrorRecurrence(frequency: .weekly, interval: 2, daysOfTheWeek: [MirrorWeekday(2), MirrorWeekday(4)]),
                            at(2026, 1, 7, 9, 0, utc), 4), ["20260107", "20260119", "20260121", "20260202"])
        // Thanksgiving; COUNT ends the rule.
        let tg = MirrorSeries.expand(MirrorRecurrence(frequency: .yearly, daysOfTheWeek: [MirrorWeekday(5, week: 4)], monthsOfTheYear: [11], count: 2),
                                     start: at(2026, 11, 26, 12, 0, utc), tz: utc, through: at(2035, 1, 1, 0, 0, utc))!
        XCTAssertEqual(tg.dates.map { String(ICSWriter.utc($0).prefix(8)) }, ["20261126", "20271125"])
        XCTAssertTrue(tg.finished)
        // Wall-clock time is kept across daylight saving time.
        let w = MirrorSeries.expand(MirrorRecurrence(frequency: .weekly), start: at(2026, 10, 19, 9, 0, oslo), tz: oslo,
                                    through: at(2026, 11, 3, 0, 0, oslo))!
        XCTAssertEqual(w.dates.map(ICSWriter.utc), ["20261019T070000Z", "20261026T080000Z", "20261102T080000Z"])
        XCTAssertFalse(w.finished)
    }

    func testWeeklySeriesWithExdateAndOverride() throws {
        let rule = MirrorRecurrence(frequency: .weekly, daysOfTheWeek: [MirrorWeekday(2)])
        let first = at(2026, 1, 5, 9, 0, oslo)
        var cal = Calendar(identifier: .gregorian)
        cal.timeZone = oslo
        var starts: [Date] = []
        var d = at(2026, 9, 28, 9, 0, oslo)
        while d < to { starts.append(d); d = cal.date(byAdding: .day, value: 7, to: d)! }
        var events = occurrences("S1", starts, rule: rule, seriesStart: first)
        // 2026-11-09 deleted on the Mac.
        let deleted = at(2026, 11, 9, 9, 0, oslo)
        events.removeAll { $0.occurrenceDate == deleted }
        // 2026-11-16 moved to 11:00 with a new title.
        let movedIndex = try XCTUnwrap(events.firstIndex { $0.occurrenceDate == at(2026, 11, 16, 9, 0, oslo) })
        events[movedIndex].start = at(2026, 11, 16, 11, 0, oslo)
        events[movedIndex].end = at(2026, 11, 16, 12, 0, oslo)
        events[movedIndex].title = "Standup (moved)"
        events[movedIndex].detached = true
        let single = MirrorEvent(key: "one", title: "Lunch", start: at(2026, 10, 30, 11, 0, oslo), end: at(2026, 10, 30, 12, 0, oslo))

        let objects = MirrorSeries.objects(for: events + [single], from: from, to: to)
        XCTAssertEqual(objects.map(\.key), ["series:S1", "one"])
        let series = objects[0]
        XCTAssertEqual(series.seriesID, "S1")
        XCTAssertEqual(series.uid, MirrorEvent.uid(forKey: "series:S1"))
        XCTAssertEqual(series.start, first)
        XCTAssertEqual(series.last, .distantFuture)
        let ics = unfold(series.ics)
        for want in ["BEGIN:VTIMEZONE", "TZID:Europe/Oslo", "BEGIN:DAYLIGHT", "TZOFFSETFROM:+0100", "TZOFFSETTO:+0200",
                     "DTSTART:20260329T020000", "BEGIN:STANDARD", "DTSTART:20261025T030000",
                     "DTSTART;TZID=Europe/Oslo:20260105T090000", "DTEND;TZID=Europe/Oslo:20260105T093000",
                     "RRULE:FREQ=WEEKLY;BYDAY=MO", "EXDATE;TZID=Europe/Oslo:20261109T090000",
                     "RECURRENCE-ID;TZID=Europe/Oslo:20261116T090000", "DTSTART:20261116T100000Z", "SUMMARY:Standup (moved)",
                     "UID:" + series.uid] {
            XCTAssertTrue(ics.contains(want), "missing \(want) in\n\(ics)")
        }
        XCTAssertEqual(ics.components(separatedBy: "BEGIN:VEVENT").count - 1, 2)
        XCTAssertEqual(ics.components(separatedBy: "UID:" + series.uid).count - 1, 2)
        // EXDATE only for the deleted one, not for the moved one.
        XCTAssertFalse(ics.contains("EXDATE;TZID=Europe/Oslo:20261116"))
        // Same input, same bytes.
        XCTAssertEqual(series.ics, MirrorSeries.objects(for: events + [single], from: from, to: to)[0].ics)
    }

    func testAllDayYearlySeriesUsesDates() {
        let rule = MirrorRecurrence(frequency: .yearly, count: 5)
        let events = occurrences("XMAS", [at(2026, 12, 24, 0, 0, oslo), at(2027, 12, 24, 0, 0, oslo)], length: 86399,
                                 rule: rule, seriesStart: at(2024, 12, 24, 0, 0, oslo), title: "Christmas Eve", allDay: true)
        let longer = at(2028, 1, 10, 0, 0, oslo)
        let objects = MirrorSeries.objects(for: events, from: from, to: longer)
        XCTAssertEqual(objects.count, 1)
        let ics = unfold(objects[0].ics)
        for want in ["DTSTART;VALUE=DATE:20241224", "DTEND;VALUE=DATE:20241225", "RRULE:FREQ=YEARLY;COUNT=5"] {
            XCTAssertTrue(ics.contains(want), "missing \(want) in\n\(ics)")
        }
        XCTAssertFalse(ics.contains("VTIMEZONE"))
        XCTAssertFalse(ics.contains("EXDATE"))
        // COUNT=5 from 2024: the last one is Christmas Eve 2028.
        XCTAssertEqual(objects[0].last, at(2028, 12, 25, 0, 0, oslo))

        // 2026 deleted on the Mac: EXDATE as a DATE.
        let ex = unfold(MirrorSeries.objects(for: [events[1]], from: from, to: longer)[0].ics)
        XCTAssertTrue(ex.contains("EXDATE;VALUE=DATE:20261224"), ex)
    }

    func testFallsBackToSingleOccurrences() {
        let first = at(2026, 1, 5, 9, 0, oslo)
        // A Tuesday occurrence the Monday rule does not make: the rule was read wrongly.
        let wrong = occurrences("W", [at(2026, 10, 5, 9, 0, oslo), at(2026, 10, 13, 9, 0, oslo)],
                                rule: MirrorRecurrence(frequency: .weekly, daysOfTheWeek: [MirrorWeekday(2)]), seriesStart: first)
        XCTAssertEqual(MirrorSeries.objects(for: wrong, from: from, to: to).map(\.key), wrong.map(\.key))
        // An unsupported rule shape.
        let odd = occurrences("Y", [at(2026, 10, 5, 9, 0, oslo)],
                              rule: MirrorRecurrence(frequency: .yearly, weeksOfTheYear: [41]), seriesStart: first)
        let objects = MirrorSeries.objects(for: odd, from: from, to: to)
        XCTAssertEqual(objects.map(\.key), odd.map(\.key))
        XCTAssertNil(objects[0].seriesID)
        XCTAssertNil(objects[0].last)
        XCTAssertEqual(objects[0].ics, ICSWriter.calendar(for: odd[0]))
        // COUNT without the series' first occurrence cannot be placed.
        let counted = occurrences("C", [at(2026, 10, 5, 9, 0, oslo)],
                                  rule: MirrorRecurrence(frequency: .weekly, count: 50), seriesStart: nil)
        XCTAssertEqual(MirrorSeries.objects(for: counted, from: from, to: to).map(\.key), counted.map(\.key))
        // Without COUNT, the first occurrence EventKit returned serves as DTSTART.
        let open = occurrences("O", [at(2026, 10, 5, 9, 0, oslo), at(2026, 10, 12, 9, 0, oslo)],
                               rule: MirrorRecurrence(frequency: .weekly), seriesStart: nil)
        let o = MirrorSeries.objects(for: open, from: from, to: at(2026, 10, 14, 0, 0, oslo))
        XCTAssertEqual(o.map(\.key), ["series:O"])
        XCTAssertTrue(unfold(o[0].ics).contains("DTSTART;TZID=Europe/Oslo:20261005T090000"))
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
