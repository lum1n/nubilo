import Foundation
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif

// Calendar mirror (Mac): copies chosen calendars of the Mac's Calendar app,
// for example an Exchange account only that Mac reaches, one way into server
// calendars marked "mirror". Other devices see them read-only through their
// CalDAV account; the server refuses their edits. Nothing is written back.
//
// Every occurrence in the window becomes its own event. Rebuilding repeat
// rules would need EventKit internals to find deleted and moved instances;
// for a read-only copy, individual occurrences are always right.

/// A calendar in the Mac's Calendar app.
public struct MirrorCalendarInfo: Hashable, Sendable, Identifiable {
    public var id: String
    public var title: String
    /// "#RRGGBB", or nil.
    public var color: String?
    /// The account it belongs to, e.g. "Exchange" or "iCloud".
    public var account: String

    public init(id: String, title: String, color: String?, account: String) {
        self.id = id
        self.title = title
        self.color = color
        self.account = account
    }
}

public struct MirrorPerson: Hashable, Sendable {
    public var name: String?
    public var email: String
    /// ACCEPTED, DECLINED, TENTATIVE or NEEDS-ACTION.
    public var status: String?

    public init(name: String?, email: String, status: String? = nil) {
        self.name = name
        self.email = email
        self.status = status
    }
}

public struct MirrorAlarm: Hashable, Sendable {
    /// Seconds relative to the start (negative = before), or nil when `date` is set.
    public var offset: TimeInterval?
    public var date: Date?

    public init(offset: TimeInterval? = nil, date: Date? = nil) {
        self.offset = offset
        self.date = date
    }
}

/// One occurrence of an event.
public struct MirrorEvent: Hashable, Sendable {
    /// Stable per occurrence (the event's identity plus its original date).
    public var key: String
    public var title: String
    public var notes: String?
    public var location: String?
    public var url: String?
    public var start: Date
    public var end: Date
    public var allDay: Bool
    /// Time zone for all-day dates; nil means the Mac's.
    public var timeZone: TimeZone?
    /// CONFIRMED, TENTATIVE or CANCELLED.
    public var status: String?
    public var busy: Bool
    public var organizer: MirrorPerson?
    public var attendees: [MirrorPerson]
    public var alarms: [MirrorAlarm]
    public var lastModified: Date?

    public init(key: String, title: String, notes: String? = nil, location: String? = nil, url: String? = nil,
                start: Date, end: Date, allDay: Bool = false, timeZone: TimeZone? = nil, status: String? = nil,
                busy: Bool = true, organizer: MirrorPerson? = nil, attendees: [MirrorPerson] = [],
                alarms: [MirrorAlarm] = [], lastModified: Date? = nil) {
        self.key = key
        self.title = title
        self.notes = notes
        self.location = location
        self.url = url
        self.start = start
        self.end = end
        self.allDay = allDay
        self.timeZone = timeZone
        self.status = status
        self.busy = busy
        self.organizer = organizer
        self.attendees = attendees
        self.alarms = alarms
        self.lastModified = lastModified
    }

    /// The UID and object name on the server, derived from `key`.
    public var uid: String { "nubilo-mirror-" + String(sha256Hex(Data(key.utf8)).prefix(32)) }
}

/// Reads the Mac's calendars (EventKit in the app, a fake in tests).
public protocol CalendarReading: Sendable {
    func calendars() async throws -> [MirrorCalendarInfo]
    func events(calendarID: String, from: Date, to: Date) async throws -> [MirrorEvent]
}

// MARK: - ICS

public enum ICSWriter {
    /// A VCALENDAR with one VEVENT, CRLF line endings, lines folded at 75 octets.
    public static func calendar(for e: MirrorEvent) -> String {
        var lines = [
            "BEGIN:VCALENDAR",
            "VERSION:2.0",
            "PRODID:-//Nubilo//Calendar mirror//EN",
            "CALSCALE:GREGORIAN",
            "BEGIN:VEVENT",
            "UID:" + e.uid,
            // Stable, so an unchanged event hashes the same on every run.
            "DTSTAMP:" + utc(e.lastModified ?? Date(timeIntervalSince1970: 0)),
        ]
        if e.allDay {
            var cal = Calendar(identifier: .gregorian)
            cal.timeZone = e.timeZone ?? .current
            let first = cal.startOfDay(for: e.start)
            var last = cal.startOfDay(for: e.end)
            // EventKit ends all-day events at 23:59:59 of the last day; DTEND is exclusive.
            if last < e.end || last <= first {
                last = cal.date(byAdding: .day, value: 1, to: last) ?? last
            }
            lines.append("DTSTART;VALUE=DATE:" + day(first, cal))
            lines.append("DTEND;VALUE=DATE:" + day(last, cal))
        } else {
            lines.append("DTSTART:" + utc(e.start))
            lines.append("DTEND:" + utc(max(e.end, e.start)))
        }
        lines.append("SUMMARY:" + text(e.title.isEmpty ? "Busy" : e.title))
        if let v = e.location, !v.isEmpty { lines.append("LOCATION:" + text(v)) }
        if let v = e.notes, !v.isEmpty { lines.append("DESCRIPTION:" + text(v)) }
        if let v = e.url, !v.isEmpty { lines.append("URL:" + v.filter { !$0.isNewline }) }
        if let v = e.status { lines.append("STATUS:" + v) }
        lines.append("TRANSP:" + (e.busy ? "OPAQUE" : "TRANSPARENT"))
        if let o = e.organizer { lines.append("ORGANIZER" + params(o) + ":mailto:" + o.email) }
        for a in e.attendees {
            lines.append("ATTENDEE" + params(a) + (a.status.map { ";PARTSTAT=" + $0 } ?? "") + ":mailto:" + a.email)
        }
        for a in e.alarms {
            let trigger: String
            if let d = a.date {
                trigger = "TRIGGER;VALUE=DATE-TIME:" + utc(d)
            } else if let off = a.offset {
                trigger = "TRIGGER:" + duration(off)
            } else {
                continue
            }
            lines += ["BEGIN:VALARM", "ACTION:DISPLAY", "DESCRIPTION:" + text(e.title.isEmpty ? "Reminder" : e.title), trigger, "END:VALARM"]
        }
        lines += ["END:VEVENT", "END:VCALENDAR"]
        return lines.map(fold).joined(separator: "\r\n") + "\r\n"
    }

    public static func utc(_ d: Date) -> String {
        var cal = Calendar(identifier: .gregorian)
        cal.timeZone = TimeZone(identifier: "UTC")!
        let c = cal.dateComponents([.year, .month, .day, .hour, .minute, .second], from: d)
        return String(format: "%04d%02d%02dT%02d%02d%02dZ", c.year!, c.month!, c.day!, c.hour!, c.minute!, c.second!)
    }

    static func day(_ d: Date, _ cal: Calendar) -> String {
        let c = cal.dateComponents([.year, .month, .day], from: d)
        return String(format: "%04d%02d%02d", c.year!, c.month!, c.day!)
    }

    /// RFC 5545 TEXT: escape backslash, semicolon, comma and newlines.
    static func text(_ s: String) -> String {
        var out = ""
        for ch in s {
            switch ch {
            case "\\": out += "\\\\"
            case ";": out += "\\;"
            case ",": out += "\\,"
            case "\r\n", "\n", "\r": out += "\\n"
            default: out.append(ch)
            }
        }
        return out
    }

    /// CN as a quoted parameter (quotes are not allowed inside).
    static func params(_ p: MirrorPerson) -> String {
        guard let n = p.name?.replacingOccurrences(of: "\"", with: "'").filter({ !$0.isNewline }), !n.isEmpty else { return "" }
        return ";CN=\"" + n + "\""
    }

    static func duration(_ seconds: TimeInterval) -> String {
        let s = Int(seconds.rounded())
        let sign = s < 0 ? "-" : ""
        var rest = abs(s)
        let days = rest / 86400; rest %= 86400
        let hours = rest / 3600; rest %= 3600
        let minutes = rest / 60
        let secs = rest % 60
        var out = sign + "P"
        if days > 0 { out += "\(days)D" }
        if hours > 0 || minutes > 0 || secs > 0 || days == 0 {
            out += "T"
            if hours > 0 { out += "\(hours)H" }
            if minutes > 0 { out += "\(minutes)M" }
            if secs > 0 || (hours == 0 && minutes == 0) { out += "\(secs)S" }
        }
        return out
    }

    /// Folds a line at 75 octets without splitting a UTF-8 character.
    static func fold(_ line: String) -> String {
        var out = ""
        var width = 0
        for ch in line {
            let n = String(ch).utf8.count
            if width + n > 75 {
                out += "\r\n "
                width = 1
            }
            out.append(ch)
            width += n
        }
        return out
    }
}

// MARK: - Settings and state

/// A Mac calendar chosen for mirroring.
public struct MirrorSelection: Codable, Hashable, Sendable {
    public var calendarID: String
    public var title: String

    public init(calendarID: String, title: String) {
        self.calendarID = calendarID
        self.title = title
    }
}

public struct MirrorSettings: Codable, Equatable, Sendable {
    public var calendars: [MirrorSelection] = []

    public init() {}

    static let key = "dev.nubilo.calendarMirror.settings"

    public static func load() -> MirrorSettings {
        guard let d = UserDefaults.standard.data(forKey: key), let s = try? JSONDecoder().decode(MirrorSettings.self, from: d) else {
            return MirrorSettings()
        }
        return s
    }

    public func save() {
        UserDefaults.standard.set(try? JSONEncoder().encode(self), forKey: Self.key)
    }
}

/// What one run did.
public struct MirrorRun: Equatable, Sendable {
    public struct CalendarResult: Equatable, Sendable {
        public var title: String
        public var serverName: String
        public var events: Int
        public var pushed: Int
        public var deleted: Int
    }

    public var started = Date()
    public var finished: Date?
    public var calendars: [CalendarResult] = []
    public var errors: [String] = []
}

/// Keeps the server copies of the chosen calendars in step with the Mac.
public actor CalendarMirrorEngine {
    /// Days before today and after today that are copied.
    public static let daysBack = 30
    public static let daysAhead = 365

    struct Entry: Codable, Equatable {
        var objectID: String
        var hash: String
        var revision: UInt64
        var start: Date
    }

    struct CalendarState: Codable, Equatable {
        var collectionID: String
        var entries: [String: Entry] = [:] // by MirrorEvent.key
    }

    struct State: Codable, Equatable {
        var calendars: [String: CalendarState] = [:] // by Mac calendar id
    }

    private let stateURL: URL
    private var state: State
    private var running = false
    public private(set) var lastRun: MirrorRun?

    /// `stateURL` holds which server object belongs to which occurrence.
    public init(stateURL: URL) {
        self.stateURL = stateURL
        if let d = try? Data(contentsOf: stateURL), let s = try? JSONDecoder().decode(State.self, from: d) {
            state = s
        } else {
            state = State()
        }
    }

    /// ~/Library/Application Support/Nubilo/calendar-mirror.json
    public static var defaultStateURL: URL {
        let dir = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("Nubilo", isDirectory: true)
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        return dir.appendingPathComponent("calendar-mirror.json")
    }

    /// Forgets all mirror state (sign out, another server).
    public func reset() {
        state = State()
        try? FileManager.default.removeItem(at: stateURL)
    }

    /// One pass over `selections`. `deviceID` is this device, the mirrors' writer.
    public func run(client: NubiloClient, reader: CalendarReading, selections: [MirrorSelection],
                    deviceID: String, now: Date = Date()) async -> MirrorRun {
        var run = MirrorRun(started: now)
        guard !running else {
            run.errors.append("A calendar mirror run is already in progress.")
            run.finished = Date()
            return run
        }
        running = true
        defer { running = false }
        let from = Calendar(identifier: .gregorian).date(byAdding: .day, value: -Self.daysBack, to: now) ?? now
        let to = Calendar(identifier: .gregorian).date(byAdding: .day, value: Self.daysAhead, to: now) ?? now
        do {
            var cols = try await client.syncCollections()
            let colors = Dictionary((try? await reader.calendars())?.map { ($0.id, $0.color) } ?? [], uniquingKeysWith: { a, _ in a })
            var used = Set<String>()
            for sel in selections {
                if Task.isCancelled { break }
                do {
                    let col = try await ensureCollection(client: client, title: sel.title, color: colors[sel.calendarID] ?? nil,
                                                         deviceID: deviceID, cols: &cols, used: used)
                    used.insert(col.name)
                    let r = try await mirror(sel, into: col, client: client, reader: reader, from: from, to: to)
                    run.calendars.append(r)
                } catch {
                    run.errors.append("\(sel.title): \(error.localizedDescription)")
                }
            }
        } catch {
            run.errors.append(error.localizedDescription)
        }
        // Calendars no longer selected keep their server copy; drop their state.
        let selected = Set(selections.map(\.calendarID))
        state.calendars = state.calendars.filter { selected.contains($0.key) }
        save()
        run.finished = Date()
        lastRun = run
        return run
    }

    private func mirror(_ sel: MirrorSelection, into col: SyncCollection, client: NubiloClient, reader: CalendarReading,
                        from: Date, to: Date) async throws -> MirrorRun.CalendarResult {
        var cs = state.calendars[sel.calendarID] ?? CalendarState(collectionID: col.id)
        if cs.collectionID != col.id {
            // The copy was deleted on the server (or renamed); start over.
            cs = CalendarState(collectionID: col.id)
        }
        // A failed listing throws; it must never look like "everything was deleted".
        let events = try await reader.events(calendarID: sel.calendarID, from: from, to: to)
        if cs.entries.isEmpty {
            // No local state (first run, or this Mac lost it): adopt what the
            // server copy already has, matched by UID, instead of duplicating it.
            let live = try await client.liveObjects(collectionID: col.id)
            let keyByUID = Dictionary(events.map { ($0.uid, $0) }, uniquingKeysWith: { a, _ in a })
            for obj in live.values {
                if let uid = obj.uid, let e = keyByUID[uid] {
                    cs.entries[e.key] = Entry(objectID: obj.objectId, hash: obj.contentHash, revision: obj.revision, start: e.start)
                } else if !events.isEmpty {
                    // Left over from an earlier copy: no longer on the Mac.
                    cs.entries["orphan:" + obj.objectId] = Entry(objectID: obj.objectId, hash: obj.contentHash, revision: obj.revision, start: from)
                }
            }
        }
        var result = MirrorRun.CalendarResult(title: sel.title, serverName: col.name, events: events.count, pushed: 0, deleted: 0)

        var changes: [SyncChange] = []
        var pending: [String: Entry] = [:] // object id → entry once its push succeeds
        var keyByObject: [String: String] = [:]
        var seen = Set<String>()
        for e in events {
            guard seen.insert(e.key).inserted else { continue }
            let ics = Data(ICSWriter.calendar(for: e).utf8)
            let hash = sha256Hex(ics)
            let old = cs.entries[e.key]
            if let old, old.hash == hash { continue }
            _ = try await client.putBlob(ics)
            let objectID = old?.objectID ?? ULID.make()
            changes.append(.put(objectID: objectID, collectionID: col.id, kind: "event", baseRevision: old?.revision,
                                blob: hash, size: ics.count,
                                metadata: ["name": e.uid + ".ics", "uid": e.uid, "comp": "VEVENT"]))
            pending[objectID] = Entry(objectID: objectID, hash: hash, revision: old?.revision ?? 0, start: e.start)
            keyByObject[objectID] = e.key
        }
        // EventKit lists nothing while access is off or an account is still
        // loading; keep the copy rather than emptying it.
        if !(events.isEmpty && !cs.entries.isEmpty) {
            for (key, entry) in cs.entries where !seen.contains(key) && entry.start >= from && entry.start <= to {
                changes.append(.delete(objectID: entry.objectID, collectionID: col.id, baseRevision: entry.revision))
                keyByObject[entry.objectID] = key
            }
        }
        var firstError: String?
        for batch in stride(from: 0, to: changes.count, by: 200).map({ Array(changes[$0..<min($0 + 200, changes.count)]) }) {
            let results = try await client.push(batch)
            for (change, res) in zip(batch, results) {
                guard let key = keyByObject[change.objectId] else { continue }
                if res.status != "ok" {
                    if res.error == "collection deleted" || res.error == "collection not found" {
                        cs = CalendarState(collectionID: "")
                    }
                    firstError = firstError ?? "\(res.status) \(res.error ?? "")"
                    continue
                }
                if change.op == "delete" {
                    cs.entries[key] = nil
                    result.deleted += 1
                } else if var entry = pending[change.objectId] {
                    entry.revision = res.revision ?? entry.revision
                    cs.entries[key] = entry
                    result.pushed += 1
                }
            }
        }
        state.calendars[sel.calendarID] = cs
        save()
        if let firstError {
            throw NubiloError.http(status: 409, message: "some events were not copied: \(firstError)")
        }
        return result
    }

    /// The server calendar for a Mac calendar: its title, unless one of the
    /// user's own calendars already has that name (a mirror never takes over
    /// a calendar the phones edit), then "Title (Mac)", "Title (Mac 2)", …
    private func ensureCollection(client: NubiloClient, title: String, color: String?, deviceID: String,
                                  cols: inout [SyncCollection], used: Set<String>) async throws -> SyncCollection {
        let name = Self.mirrorName(title: title, deviceID: deviceID, cols: cols, used: used)
        if let c = cols.first(where: { $0.kind == "calendar" && $0.name == name && $0.isOwnRoot }),
           c.calendar?.mirror == true, c.calendar?.mirrorDevice == deviceID, color == nil || c.calendar?.color == color {
            return c
        }
        var meta: [String: SyncJSON] = ["mirror": .bool(true)]
        if let color { meta["color"] = .string(color) }
        let c = try await client.ensureCollection(kind: "calendar", name: name, metadata: meta)
        cols.removeAll { $0.id == c.id }
        cols.append(c)
        return c
    }

    static func mirrorName(title: String, deviceID: String, cols: [SyncCollection], used: Set<String>) -> String {
        let base = davName(title.isEmpty ? "Calendar" : title)
        for n in 1... {
            let cand = n == 1 ? base : davName(n == 2 ? "\(base) (Mac)" : "\(base) (Mac \(n - 1))")
            if used.contains(cand) { continue }
            let existing = cols.first { $0.kind == "calendar" && $0.name == cand && $0.isOwnRoot }
            if let existing, !(existing.calendar?.mirror == true && existing.calendar?.mirrorDevice == deviceID) {
                continue
            }
            return cand
        }
        return base
    }

    /// Mirrors the server's DAV name rules (dav.DAVResourceName).
    static func davName(_ s: String) -> String {
        var out = String(s.map { c -> Character in
            c == "/" || c == "\\" || (c.unicodeScalars.first.map { $0.value < 32 } ?? false) ? "_" : c
        })
        out = out.trimmingCharacters(in: CharacterSet(charactersIn: " ."))
        if out.isEmpty { return "Calendar" }
        if out.utf8.count > 200 { out = String(out.prefix(200)) }
        return out
    }

    private func save() {
        guard let d = try? JSONEncoder().encode(state) else { return }
        try? d.write(to: stateURL, options: .atomic)
    }
}
