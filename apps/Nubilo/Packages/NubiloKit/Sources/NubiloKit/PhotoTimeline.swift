import Foundation

/// Photos sorted newest first and grouped by the day they were taken, with a
/// month and year index for jumping around. Photos without a capture date
/// come last, in one "undated" day.
///
/// The server pages photos by id, not by date, so the apps build this from
/// the whole list once it has loaded.
public struct PhotoTimeline: Sendable {
    public struct Day: Identifiable, Sendable, Hashable {
        /// "2025-03-14", or "undated".
        public var id: String
        /// Midnight of the day in the timeline's calendar; nil for undated.
        public var date: Date?
        public var photos: [Photo]
    }

    public struct Month: Identifiable, Sendable, Hashable {
        /// "2025-03", or "undated".
        public var id: String
        /// The first of the month; nil for undated.
        public var date: Date?
        public var year: Int?
        /// The newest day of the month, which is where a jump lands.
        public var firstDayID: String
        public var count: Int
        /// Photos before this month in the timeline: where it starts on a
        /// scrubber that gives each photo the same height.
        public var offset: Int
    }

    public struct Year: Identifiable, Sendable, Hashable {
        public var id: Int { year }
        public var year: Int
        /// Newest month first.
        public var months: [Month]
        public var count: Int { months.reduce(0) { $0 + $1.count } }
    }

    public static let undatedID = "undated"

    public private(set) var days: [Day] = []
    public private(set) var months: [Month] = []
    public private(set) var years: [Year] = []
    /// Every photo in display order, for paging through them one by one.
    public private(set) var photos: [Photo] = []

    private var monthIndexByDay: [String: Int] = [:]
    private var dayIndexByID: [String: Int] = [:]

    public var count: Int { photos.count }
    public var isEmpty: Bool { photos.isEmpty }

    public init() {}

    public init(photos unsorted: [Photo], calendar: Calendar = .current) {
        photos = unsorted.sorted { a, b in
            switch (a.takenAtMs, b.takenAtMs) {
            case let (x?, y?) where x != y: return x > y
            case (_?, nil): return true
            case (nil, _?): return false
            default: return a.id > b.id
            }
        }

        for photo in photos {
            let (dayID, date) = Self.day(of: photo, calendar: calendar)
            if days.last?.id == dayID {
                days[days.count - 1].photos.append(photo)
            } else {
                days.append(Day(id: dayID, date: date, photos: [photo]))
            }
        }

        var offset = 0
        for day in days {
            let monthID = day.id == Self.undatedID ? Self.undatedID : String(day.id.prefix(7))
            if months.last?.id == monthID {
                months[months.count - 1].count += day.photos.count
            } else {
                let start = day.date.flatMap { calendar.dateInterval(of: .month, for: $0)?.start }
                months.append(Month(id: monthID, date: start,
                                    year: day.date.map { calendar.component(.year, from: $0) },
                                    firstDayID: day.id, count: day.photos.count, offset: offset))
            }
            monthIndexByDay[day.id] = months.count - 1
            dayIndexByID[day.id] = dayIndexByID.count
            offset += day.photos.count
        }

        for month in months {
            guard let year = month.year else { continue }
            if years.last?.year == year {
                years[years.count - 1].months.append(month)
            } else {
                years.append(Year(year: year, months: [month]))
            }
        }
    }

    /// Where a day sits in `days`.
    public func index(ofDay dayID: String) -> Int? {
        dayIndexByID[dayID]
    }

    /// The month a day belongs to.
    public func month(containing dayID: String) -> Month? {
        monthIndexByDay[dayID].map { months[$0] }
    }

    /// Where a month starts on a scrubber, from 0 (top) to 1 (bottom).
    public func fraction(of month: Month) -> Double {
        count == 0 ? 0 : Double(month.offset) / Double(count)
    }

    /// The month under a point on a scrubber, from 0 (top) to 1 (bottom).
    public func month(atFraction fraction: Double) -> Month? {
        guard !months.isEmpty else { return nil }
        let target = Int((min(max(fraction, 0), 1) * Double(count)).rounded(.down))
        // Last month that starts at or before the target photo.
        var lo = 0, hi = months.count - 1
        while lo < hi {
            let mid = (lo + hi + 1) / 2
            if months[mid].offset <= target { lo = mid } else { hi = mid - 1 }
        }
        return months[lo]
    }

    private static func day(of photo: Photo, calendar: Calendar) -> (String, Date?) {
        guard let taken = photo.takenAt else { return (undatedID, nil) }
        let c = calendar.dateComponents([.year, .month, .day], from: taken)
        guard let y = c.year, let m = c.month, let d = c.day else { return (undatedID, nil) }
        return (String(format: "%04d-%02d-%02d", y, m, d), calendar.startOfDay(for: taken))
    }
}
