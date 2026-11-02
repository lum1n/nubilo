import Foundation
import XCTest
@testable import NubiloKit

final class PhotoTimelineTests: XCTestCase {
    var utc: Calendar {
        var c = Calendar(identifier: .gregorian)
        c.timeZone = TimeZone(identifier: "UTC")!
        return c
    }

    func photo(_ id: String, _ iso: String?) -> Photo {
        let ms = iso.flatMap { ISO8601DateFormatter().date(from: $0) }.map { Int64($0.timeIntervalSince1970 * 1000) }
        return Photo(id: id, takenAtMs: ms)
    }

    func testGroupsNewestFirstWithUndatedLast() {
        let t = PhotoTimeline(photos: [
            photo("a", "2024-12-31T23:00:00Z"),
            photo("u", nil),
            photo("b", "2025-03-14T08:00:00Z"),
            photo("c", "2025-03-14T18:00:00Z"),
            photo("d", "2025-03-02T12:00:00Z"),
        ], calendar: utc)

        XCTAssertEqual(t.photos.map(\.id), ["c", "b", "d", "a", "u"])
        XCTAssertEqual(t.days.map(\.id), ["2025-03-14", "2025-03-02", "2024-12-31", "undated"])
        XCTAssertEqual(t.days[0].photos.map(\.id), ["c", "b"])
        XCTAssertNil(t.days[3].date)

        XCTAssertEqual(t.months.map(\.id), ["2025-03", "2024-12", "undated"])
        XCTAssertEqual(t.months.map(\.count), [3, 1, 1])
        XCTAssertEqual(t.months.map(\.offset), [0, 3, 4])
        XCTAssertEqual(t.months[0].firstDayID, "2025-03-14")

        // Undated photos have no year.
        XCTAssertEqual(t.years.map(\.year), [2025, 2024])
        XCTAssertEqual(t.years[0].months.map(\.id), ["2025-03"])
        XCTAssertEqual(t.month(containing: "2025-03-02")?.id, "2025-03")
        XCTAssertEqual(t.index(ofDay: "2024-12-31"), 2)
        XCTAssertNil(t.index(ofDay: "2024-12-30"))
    }

    func testDaysFollowTheCalendarTimeZone() {
        var oslo = utc
        oslo.timeZone = TimeZone(identifier: "Europe/Oslo")!
        // 23:30 UTC on the 31st is already the 1st in Oslo.
        let t = PhotoTimeline(photos: [photo("a", "2024-12-31T23:30:00Z")], calendar: oslo)
        XCTAssertEqual(t.days.map(\.id), ["2025-01-01"])
        XCTAssertEqual(t.years.map(\.year), [2025])
    }

    func testSameTimeKeepsAStableOrder() {
        let ps = [photo("a", "2025-01-01T00:00:00Z"), photo("b", "2025-01-01T00:00:00Z")]
        XCTAssertEqual(PhotoTimeline(photos: ps, calendar: utc).photos.map(\.id), ["b", "a"])
        XCTAssertEqual(PhotoTimeline(photos: ps.reversed(), calendar: utc).photos.map(\.id), ["b", "a"])
    }

    func testScrubberFractions() {
        // 6 photos in March, 2 in February, 2 undated.
        var ps = (0..<6).map { photo("m\($0)", "2025-03-1\($0)T12:00:00Z") }
        ps += [photo("f0", "2025-02-01T12:00:00Z"), photo("f1", "2025-02-02T12:00:00Z")]
        ps += [photo("u0", nil), photo("u1", nil)]
        let t = PhotoTimeline(photos: ps, calendar: utc)

        XCTAssertEqual(t.fraction(of: t.months[1]), 0.6, accuracy: 1e-9)
        XCTAssertEqual(t.month(atFraction: -1)?.id, "2025-03")
        XCTAssertEqual(t.month(atFraction: 0.59)?.id, "2025-03")
        XCTAssertEqual(t.month(atFraction: 0.6)?.id, "2025-02")
        XCTAssertEqual(t.month(atFraction: 0.85)?.id, "undated")
        XCTAssertEqual(t.month(atFraction: 1)?.id, "undated")
    }

    func testEmpty() {
        let t = PhotoTimeline(photos: [], calendar: utc)
        XCTAssertTrue(t.isEmpty)
        XCTAssertNil(t.month(atFraction: 0.5))
    }
}
