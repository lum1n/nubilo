import Foundation

/// One server object of a mirrored calendar: a single occurrence, or a whole series.
public struct MirrorObject: Equatable, Sendable {
    /// Stable: MirrorEvent.key for an occurrence, "series:" + series id for a series.
    public var key: String
    public var ics: String
    /// Start of the occurrence, or the series' DTSTART.
    public var start: Date
    /// For a series, the end of its last occurrence (distantFuture when it never ends).
    public var last: Date?
    /// Set when the object holds a whole series.
    public var seriesID: String?

    public var uid: String { MirrorEvent.uid(forKey: key) }
}

/// Turns EventKit's expanded occurrences back into series: one VEVENT with an
/// RRULE, EXDATE for occurrences EventKit no longer has, and RECURRENCE-ID
/// overrides for moved or edited ones. A series whose rule cannot be written
/// faithfully, or that does not reproduce the occurrences EventKit returned,
/// falls back to one object per occurrence.
public enum MirrorSeries {
    /// The objects for one calendar's events in [from, to], in the order the events came.
    public static func objects(for events: [MirrorEvent], from: Date, to: Date) -> [MirrorObject] {
        enum Item { case single(MirrorEvent), series(String) }
        var items: [Item] = []
        var groups: [String: [MirrorEvent]] = [:]
        for e in events {
            if let s = e.seriesID, !s.isEmpty {
                if groups[s] == nil { items.append(.series(s)) }
                groups[s, default: []].append(e)
            } else {
                items.append(.single(e))
            }
        }
        var out: [MirrorObject] = []
        for item in items {
            switch item {
            case .single(let e):
                out.append(single(e))
            case .series(let s):
                let group = groups[s] ?? []
                if let o = series(s, group, from: from, to: to) {
                    out.append(o)
                } else {
                    out += group.map(single)
                }
            }
        }
        return out
    }

    static func single(_ e: MirrorEvent) -> MirrorObject {
        MirrorObject(key: e.key, ics: ICSWriter.calendar(for: e), start: e.start, last: nil, seriesID: nil)
    }

    /// One object for the series, or nil to fall back to single occurrences.
    static func series(_ id: String, _ group: [MirrorEvent], from: Date, to: Date) -> MirrorObject? {
        func original(_ e: MirrorEvent) -> Date { e.occurrenceDate ?? e.start }
        let sorted = group.sorted { original($0) < original($1) }
        let regular = sorted.filter { !$0.detached }
        guard let base = regular.first(where: { second(original($0)) == second($0.start) }),
              base.recurrence.count == 1,
              regular.allSatisfy({ $0.recurrence == base.recurrence }) else { return nil }
        let rule = base.recurrence[0]
        let tz = base.timeZone ?? .current
        let cal = gregorian(tz)
        // Without the series' first occurrence, a COUNT cannot be placed.
        guard var dtstart = base.seriesStart ?? (rule.count == nil ? sorted.first.map(original) : nil) else { return nil }
        if base.allDay { dtstart = cal.startOfDay(for: dtstart) }

        var master = base
        master.start = dtstart
        let duration: TimeInterval
        if base.allDay {
            let days = max(1, dayCount(base))
            master.end = cal.date(byAdding: .day, value: days, to: dtstart) ?? dtstart
            duration = master.end.timeIntervalSince(dtstart)
        } else {
            duration = max(0, base.end.timeIntervalSince(base.start))
            master.end = dtstart.addingTimeInterval(duration)
        }

        // A rule with an end is followed to it, for the series' last occurrence.
        let latest = rule.count != nil || rule.until != nil ? Date.distantFuture
            : max(to, sorted.last.map(original) ?? to).addingTimeInterval(86400)
        guard let rrule = rrule(rule, allDay: base.allDay, tz: tz),
              let expansion = expand(rule, start: dtstart, tz: tz, through: latest),
              let first = expansion.dates.first, second(first) == second(dtstart) else { return nil }
        let instances = Set(expansion.dates.map(second))
        var returned = Set<Int>()
        for e in sorted {
            // Every occurrence EventKit has must be one the rule makes.
            guard instances.contains(second(original(e))), returned.insert(second(original(e))).inserted else { return nil }
        }
        let window = expansion.dates.filter { $0 >= from && $0.addingTimeInterval(duration) <= to }
        let exdates = window.filter { !returned.contains(second($0)) }
        // A rule that makes many occurrences EventKit does not have was probably read wrongly.
        if exdates.count > 2 && exdates.count * 2 > window.count { return nil }
        let overrides = sorted.filter { $0.detached || second(original($0)) != second($0.start) || !sameAsSeries($0, base) }

        // RECURRENCE-ID and EXDATE use the same value type as DTSTART.
        let tzid = "TZID=" + tzParam(tz.identifier)
        func value(_ d: Date) -> String { base.allDay ? ICSWriter.day(d, cal) : local(d, cal) }
        let kind = base.allDay ? "VALUE=DATE" : tzid

        var timing = base.allDay ? ICSWriter.timing(master)
            : ["DTSTART;" + tzid + ":" + local(dtstart, cal), "DTEND;" + tzid + ":" + local(master.end, cal)]
        timing.append("RRULE:" + rrule)
        if !exdates.isEmpty { timing.append("EXDATE;" + kind + ":" + exdates.map(value).joined(separator: ",")) }

        let uid = MirrorEvent.uid(forKey: "series:" + id)
        var body = base.allDay ? [] : vtimezone(tz, from: from, to: to)
        body += ICSWriter.vevent(master, uid: uid, timing: timing)
        for o in overrides {
            body += ICSWriter.vevent(o, uid: uid, timing: ["RECURRENCE-ID;" + kind + ":" + value(original(o))] + ICSWriter.timing(o))
        }

        var last = expansion.finished ? (expansion.dates.last ?? dtstart).addingTimeInterval(duration) : Date.distantFuture
        for o in overrides { last = max(last, o.end) }
        return MirrorObject(key: "series:" + id, ics: ICSWriter.document(body), start: dtstart, last: last, seriesID: id)
    }

    /// Same series data apart from when it happens.
    static func sameAsSeries(_ e: MirrorEvent, _ base: MirrorEvent) -> Bool {
        func strip(_ x: MirrorEvent) -> MirrorEvent {
            var x = x
            let length = x.allDay ? TimeInterval(dayCount(x)) * 86400 : x.end.timeIntervalSince(x.start)
            x.key = ""
            x.start = Date(timeIntervalSince1970: 0)
            x.end = x.start.addingTimeInterval(length)
            x.occurrenceDate = nil
            x.lastModified = nil
            x.seriesStart = nil
            return x
        }
        return strip(e) == strip(base)
    }

    static func dayCount(_ e: MirrorEvent) -> Int {
        let (first, last, cal) = ICSWriter.allDayRange(e)
        return cal.dateComponents([.day], from: first, to: last).day ?? 1
    }

    // MARK: - RRULE

    /// Whether the rule's shape can be written and expanded here.
    static func supported(_ r: MirrorRecurrence) -> Bool {
        guard r.interval >= 1, r.weeksOfTheYear.isEmpty, r.daysOfTheYear.isEmpty,
              !(r.count != nil && r.until != nil), (r.count ?? 1) >= 1,
              r.daysOfTheWeek.allSatisfy({ (1...7).contains($0.weekday) && abs($0.week) <= 5 }),
              r.daysOfTheMonth.allSatisfy({ $0 != 0 && abs($0) <= 31 }),
              r.monthsOfTheYear.allSatisfy({ (1...12).contains($0) }),
              r.setPositions.allSatisfy({ $0 != 0 && abs($0) <= 366 }),
              (0...7).contains(r.firstDayOfTheWeek) else { return false }
        let ordinals = r.daysOfTheWeek.contains { $0.week != 0 }
        let monthShape = !(!r.daysOfTheWeek.isEmpty && !r.daysOfTheMonth.isEmpty)
            && !(ordinals && !r.setPositions.isEmpty)
            && !(!r.setPositions.isEmpty && r.daysOfTheWeek.isEmpty && r.daysOfTheMonth.isEmpty)
        switch r.frequency {
        case .daily:
            return r.daysOfTheWeek.isEmpty && r.daysOfTheMonth.isEmpty && r.monthsOfTheYear.isEmpty && r.setPositions.isEmpty
        case .weekly:
            return !ordinals && r.daysOfTheMonth.isEmpty && r.monthsOfTheYear.isEmpty && r.setPositions.isEmpty
        case .monthly:
            return r.monthsOfTheYear.isEmpty && monthShape
        case .yearly:
            // Without BYMONTH, BYDAY and BYMONTHDAY would count across the whole year.
            if r.monthsOfTheYear.isEmpty {
                return r.daysOfTheWeek.isEmpty && r.daysOfTheMonth.isEmpty && r.setPositions.isEmpty
            }
            return monthShape
        }
    }

    static let dayCodes = ["SU", "MO", "TU", "WE", "TH", "FR", "SA"]

    /// The RRULE value, or nil when the shape is not supported.
    public static func rrule(_ r: MirrorRecurrence, allDay: Bool, tz: TimeZone) -> String? {
        guard supported(r) else { return nil }
        var parts = ["FREQ=" + r.frequency.rawValue]
        if r.interval > 1 { parts.append("INTERVAL=\(r.interval)") }
        if let c = r.count {
            parts.append("COUNT=\(c)")
        } else if let u = until(r, tz: tz) {
            // DATE when DTSTART is a DATE, otherwise UTC (RFC 5545 3.3.10).
            parts.append("UNTIL=" + (allDay ? ICSWriter.day(u, gregorian(tz)) : ICSWriter.utc(u)))
        }
        if !r.daysOfTheWeek.isEmpty {
            parts.append("BYDAY=" + r.daysOfTheWeek.map { ($0.week != 0 ? "\($0.week)" : "") + dayCodes[$0.weekday - 1] }.joined(separator: ","))
        }
        if !r.daysOfTheMonth.isEmpty { parts.append("BYMONTHDAY=" + r.daysOfTheMonth.map(String.init).joined(separator: ",")) }
        if !r.monthsOfTheYear.isEmpty { parts.append("BYMONTH=" + r.monthsOfTheYear.map(String.init).joined(separator: ",")) }
        if !r.setPositions.isEmpty { parts.append("BYSETPOS=" + r.setPositions.map(String.init).joined(separator: ",")) }
        if r.firstDayOfTheWeek != 0 { parts.append("WKST=" + dayCodes[r.firstDayOfTheWeek - 1]) }
        return parts.joined(separator: ";")
    }

    /// The last possible start. EventKit may give an end date at midnight,
    /// meaning the whole day; it then counts to the end of that day.
    static func until(_ r: MirrorRecurrence, tz: TimeZone) -> Date? {
        guard let u = r.until else { return nil }
        let cal = gregorian(tz)
        let day = cal.startOfDay(for: u)
        guard day == u else { return u }
        return (cal.date(byAdding: .day, value: 1, to: day) ?? u).addingTimeInterval(-1)
    }

    struct Expansion {
        var dates: [Date]
        /// The rule ended (COUNT or UNTIL) before `through`.
        var finished: Bool
    }

    /// The rule's occurrences from `start` (its DTSTART) through `limit`, or nil for an unsupported shape.
    static func expand(_ r: MirrorRecurrence, start: Date, tz: TimeZone, through limit: Date) -> Expansion? {
        guard supported(r) else { return nil }
        let cal = gregorian(tz)
        let t = cal.dateComponents([.year, .month, .day, .hour, .minute, .second], from: start)
        guard let sy = t.year, let sm = t.month, let sd = t.day else { return nil }
        let startDay = dayNumber(sy, sm, sd)
        let limitParts = cal.dateComponents([.year, .month, .day], from: limit)
        let limitDay = dayNumber(limitParts.year ?? sy, limitParts.month ?? sm, limitParts.day ?? sd)
        let until = until(r, tz: tz)
        let wkst = r.firstDayOfTheWeek == 0 ? 2 : r.firstDayOfTheWeek // RFC default MO
        let weekdays = r.daysOfTheWeek.isEmpty ? [weekday(startDay)] : Set(r.daysOfTheWeek.map(\.weekday))
        var out: [Date] = []
        for k in 0..<100_000 {
            let periodStart: Int
            let candidates: [Int]
            switch r.frequency {
            case .daily:
                periodStart = startDay + k * r.interval
                candidates = [periodStart]
            case .weekly:
                periodStart = startDay - (weekday(startDay) - wkst + 7) % 7 + 7 * r.interval * k
                candidates = (0..<7).map { periodStart + $0 }.filter { weekdays.contains(weekday($0)) }
            case .monthly:
                let m0 = sy * 12 + (sm - 1) + k * r.interval
                periodStart = dayNumber(m0 / 12, m0 % 12 + 1, 1)
                candidates = positions(monthDays(m0 / 12, m0 % 12 + 1, r, defaultDay: sd), r.setPositions)
            case .yearly:
                let y = sy + k * r.interval
                periodStart = dayNumber(y, 1, 1)
                let months = r.monthsOfTheYear.isEmpty ? [sm] : r.monthsOfTheYear
                candidates = positions(Array(Set(months.flatMap { monthDays(y, $0, r, defaultDay: sd) })).sorted(), r.setPositions)
            }
            if periodStart > limitDay { return Expansion(dates: out, finished: false) }
            for c in candidates {
                let (y, m, d) = ymd(c)
                guard let date = cal.date(from: DateComponents(year: y, month: m, day: d, hour: t.hour, minute: t.minute, second: t.second)),
                      date >= start else { continue }
                if let until, date > until { return Expansion(dates: out, finished: true) }
                if date > limit { return Expansion(dates: out, finished: false) }
                out.append(date)
                if let count = r.count, out.count >= count { return Expansion(dates: out, finished: true) }
            }
        }
        return Expansion(dates: out, finished: false)
    }

    /// Days (day numbers) of a month the rule picks, sorted.
    static func monthDays(_ y: Int, _ m: Int, _ r: MirrorRecurrence, defaultDay: Int) -> [Int] {
        let n = daysInMonth(y, m)
        var days: [Int]
        if !r.daysOfTheMonth.isEmpty {
            days = r.daysOfTheMonth.map { $0 > 0 ? $0 : n + $0 + 1 }.filter { (1...n).contains($0) }
        } else if !r.daysOfTheWeek.isEmpty {
            days = []
            for w in r.daysOfTheWeek {
                let all = (1...n).filter { weekday(dayNumber(y, m, $0)) == w.weekday }
                if w.week == 0 {
                    days += all
                } else if w.week > 0, w.week <= all.count {
                    days.append(all[w.week - 1])
                } else if w.week < 0, all.count + w.week >= 0 {
                    days.append(all[all.count + w.week])
                }
            }
        } else {
            days = defaultDay <= n ? [defaultDay] : []
        }
        return Array(Set(days)).sorted().map { dayNumber(y, m, $0) }
    }

    /// BYSETPOS over one period's sorted days.
    static func positions(_ days: [Int], _ setPositions: [Int]) -> [Int] {
        guard !setPositions.isEmpty else { return days }
        let picked = setPositions.compactMap { p -> Int? in
            let i = p > 0 ? p - 1 : days.count + p
            return days.indices.contains(i) ? days[i] : nil
        }
        return Array(Set(picked)).sorted()
    }

    // MARK: - Dates

    static let utcCalendar = gregorian(TimeZone(identifier: "UTC")!)

    static func gregorian(_ tz: TimeZone) -> Calendar {
        var cal = Calendar(identifier: .gregorian)
        cal.timeZone = tz
        return cal
    }

    /// Days since 1970-01-01 for a calendar date.
    static func dayNumber(_ y: Int, _ m: Int, _ d: Int) -> Int {
        let date = utcCalendar.date(from: DateComponents(year: y, month: m, day: d)) ?? Date(timeIntervalSince1970: 0)
        return Int((date.timeIntervalSince1970 / 86400).rounded(.down))
    }

    static func ymd(_ n: Int) -> (Int, Int, Int) {
        let c = utcCalendar.dateComponents([.year, .month, .day], from: Date(timeIntervalSince1970: TimeInterval(n) * 86400))
        return (c.year ?? 1970, c.month ?? 1, c.day ?? 1)
    }

    /// 1 = Sunday … 7 = Saturday (1970-01-01 was a Thursday).
    static func weekday(_ n: Int) -> Int { ((n + 4) % 7 + 7) % 7 + 1 }

    static func daysInMonth(_ y: Int, _ m: Int) -> Int {
        let next = m == 12 ? dayNumber(y + 1, 1, 1) : dayNumber(y, m + 1, 1)
        return next - dayNumber(y, m, 1)
    }

    static func second(_ d: Date) -> Int { Int(d.timeIntervalSince1970.rounded()) }

    /// Wall-clock time in the calendar's zone, for TZID values.
    static func local(_ d: Date, _ cal: Calendar) -> String {
        let c = cal.dateComponents([.year, .month, .day, .hour, .minute, .second], from: d)
        return String(format: "%04d%02d%02dT%02d%02d%02d", c.year ?? 0, c.month ?? 0, c.day ?? 0, c.hour ?? 0, c.minute ?? 0, c.second ?? 0)
    }

    static func tzParam(_ id: String) -> String {
        id.contains(where: { ":;,".contains($0) }) ? "\"" + id.filter { $0 != "\"" } + "\"" : id
    }

    // MARK: - VTIMEZONE

    /// The zone's offsets and changes from the year before `from` to the
    /// year after `to`. Whole years, so the text changes only once a year.
    static func vtimezone(_ tz: TimeZone, from: Date, to: Date) -> [String] {
        let utc = utcCalendar
        let y0 = utc.component(.year, from: from) - 1
        let y1 = utc.component(.year, from: to) + 2
        let start = utc.date(from: DateComponents(year: y0, month: 1, day: 1)) ?? from
        let end = utc.date(from: DateComponents(year: y1, month: 1, day: 1)) ?? to
        var lines = ["BEGIN:VTIMEZONE", "TZID:" + tz.identifier]
        func observance(_ at: Date, from before: Int, to after: Int) {
            let kind = tz.isDaylightSavingTime(for: at) ? "DAYLIGHT" : "STANDARD"
            lines += ["BEGIN:" + kind, "DTSTART:" + local(at.addingTimeInterval(TimeInterval(before)), utc),
                      "TZOFFSETFROM:" + offset(before), "TZOFFSETTO:" + offset(after), "END:" + kind]
        }
        let first = tz.secondsFromGMT(for: start)
        observance(start, from: first, to: first)
        var t = start
        for _ in 0..<64 {
            guard let next = tz.nextDaylightSavingTimeTransition(after: t), next < end, next > t else { break }
            observance(next, from: tz.secondsFromGMT(for: next.addingTimeInterval(-1)), to: tz.secondsFromGMT(for: next))
            t = next
        }
        lines.append("END:VTIMEZONE")
        return lines
    }

    /// "+0100", "-0430", "+053000".
    static func offset(_ seconds: Int) -> String {
        let sign = seconds < 0 ? "-" : "+"
        let s = abs(seconds)
        var out = sign + String(format: "%02d%02d", s / 3600, s % 3600 / 60)
        if s % 60 != 0 { out += String(format: "%02d", s % 60) }
        return out
    }
}
