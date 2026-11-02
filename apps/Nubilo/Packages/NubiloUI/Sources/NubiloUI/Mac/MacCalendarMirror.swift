#if os(macOS) && canImport(EventKit)
import AppKit
import EventKit
import Foundation
import Observation
import NubiloKit

/// Reads the Mac's Calendar app for the calendar mirror.
final class EventKitCalendarReader: CalendarReading, @unchecked Sendable {
    let store = EKEventStore()

    func calendars() async throws -> [MirrorCalendarInfo] {
        store.calendars(for: .event)
            .map { MirrorCalendarInfo(id: $0.calendarIdentifier, title: $0.title, color: Self.hex($0.cgColor), account: $0.source?.title ?? "") }
            .sorted { ($0.account, $0.title) < ($1.account, $1.title) }
    }

    func events(calendarID: String, from: Date, to: Date) async throws -> [MirrorEvent] {
        guard EKEventStore.authorizationStatus(for: .event) == .fullAccess else {
            throw NubiloError.http(status: 0, message: "Calendar access is off.")
        }
        guard let cal = store.calendar(withIdentifier: calendarID) else {
            throw NubiloError.http(status: 404, message: "This calendar is no longer on the Mac.")
        }
        let predicate = store.predicateForEvents(withStart: from, end: to, calendars: [cal])
        var firstStarts: [String: Date] = [:] // series → its first occurrence's original start
        return store.events(matching: predicate).map { e in
            var m = Self.mirrorEvent(e)
            if let series = m.seriesID, !m.recurrence.isEmpty, !e.isDetached {
                if firstStarts[series] == nil, let id = e.eventIdentifier, let first = store.event(withIdentifier: id) {
                    // For a repeating event this is its first occurrence.
                    firstStarts[series] = first.occurrenceDate ?? first.startDate
                }
                m.seriesStart = firstStarts[series]
            }
            return m
        }
    }

    static func mirrorEvent(_ e: EKEvent) -> MirrorEvent {
        // One occurrence: the series' identity plus its original date, which
        // stays put when a single occurrence is moved.
        let series = e.calendarItemExternalIdentifier ?? e.calendarItemIdentifier
        let occurrence = e.occurrenceDate ?? e.startDate ?? Date(timeIntervalSince1970: 0)
        // A rule EventKit has no counterpart for here leaves the series as single occurrences.
        let rules = e.hasRecurrenceRules ? (e.recurrenceRules ?? []) : []
        let recurrence = rules.compactMap(Self.recurrence)
        let repeating = !rules.isEmpty && recurrence.count == rules.count
        let status: String? = switch e.status {
        case .confirmed: "CONFIRMED"
        case .tentative: "TENTATIVE"
        case .canceled: "CANCELLED"
        default: nil
        }
        return MirrorEvent(
            key: series + "@" + ICSWriter.utc(occurrence),
            title: e.title ?? "",
            notes: e.notes,
            location: e.location,
            url: e.url?.absoluteString,
            start: e.startDate,
            end: e.endDate,
            allDay: e.isAllDay,
            timeZone: e.timeZone,
            status: status,
            busy: e.availability != .free,
            organizer: e.organizer.flatMap(person),
            attendees: (e.attendees ?? []).compactMap(person),
            alarms: (e.alarms ?? []).map { a in
                a.absoluteDate.map { MirrorAlarm(date: $0) } ?? MirrorAlarm(offset: a.relativeOffset)
            },
            lastModified: e.lastModifiedDate,
            seriesID: repeating || e.isDetached ? series : nil,
            occurrenceDate: e.occurrenceDate,
            recurrence: repeating ? recurrence : [],
            detached: e.isDetached)
    }

    static func recurrence(_ r: EKRecurrenceRule) -> MirrorRecurrence? {
        let frequency: MirrorRecurrence.Frequency
        switch r.frequency {
        case .daily: frequency = .daily
        case .weekly: frequency = .weekly
        case .monthly: frequency = .monthly
        case .yearly: frequency = .yearly
        @unknown default: return nil
        }
        let ints = { (v: [NSNumber]?) in (v ?? []).map(\.intValue) }
        return MirrorRecurrence(
            frequency: frequency,
            interval: r.interval,
            daysOfTheWeek: (r.daysOfTheWeek ?? []).map { MirrorWeekday($0.dayOfTheWeek.rawValue, week: $0.weekNumber) },
            daysOfTheMonth: ints(r.daysOfTheMonth),
            monthsOfTheYear: ints(r.monthsOfTheYear),
            weeksOfTheYear: ints(r.weeksOfTheYear),
            daysOfTheYear: ints(r.daysOfTheYear),
            setPositions: ints(r.setPositions),
            firstDayOfTheWeek: r.firstDayOfTheWeek,
            count: r.recurrenceEnd.flatMap { $0.occurrenceCount > 0 ? $0.occurrenceCount : nil },
            until: r.recurrenceEnd?.endDate)
    }

    static func person(_ p: EKParticipant) -> MirrorPerson? {
        guard p.url.scheme?.lowercased() == "mailto" else { return nil }
        let email = String(p.url.absoluteString.dropFirst("mailto:".count))
        guard !email.isEmpty else { return nil }
        let status: String? = switch p.participantStatus {
        case .accepted: "ACCEPTED"
        case .declined: "DECLINED"
        case .tentative: "TENTATIVE"
        case .pending: "NEEDS-ACTION"
        default: nil
        }
        return MirrorPerson(name: p.name, email: email, status: status)
    }

    static func hex(_ color: CGColor?) -> String? {
        guard let color, let c = NSColor(cgColor: color)?.usingColorSpace(.sRGB) else { return nil }
        let v = [c.redComponent, c.greenComponent, c.blueComponent].map { Int(($0 * 255).rounded()) }
        return String(format: "#%02X%02X%02X", v[0], v[1], v[2])
    }
}

/// Copies the chosen Mac calendars to the server while the app runs: after
/// Calendar changes (30 s later) and every 15 minutes.
@MainActor
@Observable
public final class MacCalendarMirror {
    public static let shared = MacCalendarMirror()

    public private(set) var settings = MirrorSettings.load()
    public private(set) var calendars: [MirrorCalendarInfo] = []
    public private(set) var access = EKEventStore.authorizationStatus(for: .event)
    public private(set) var lastRun: MirrorRun?
    public private(set) var running = false

    @ObservationIgnored private let reader = EventKitCalendarReader()
    @ObservationIgnored private let engine = CalendarMirrorEngine(stateURL: CalendarMirrorEngine.defaultStateURL)
    @ObservationIgnored private var task: Task<Void, Never>?
    @ObservationIgnored private var changedAt: Date?
    @ObservationIgnored private var lastAttempt = Date.distantPast
    @ObservationIgnored private var observer: NSObjectProtocol?

    private static let interval: TimeInterval = 15 * 60
    private static let settleDelay: TimeInterval = 30

    public var hasAccess: Bool { access == .fullAccess }

    /// Starts the loop once.
    public func start() {
        guard task == nil else { return }
        observer = NotificationCenter.default.addObserver(forName: .EKEventStoreChanged, object: reader.store, queue: .main) { [weak self] _ in
            MainActor.assumeIsolated {
                if self?.changedAt == nil { self?.changedAt = Date() }
            }
        }
        task = Task { [weak self] in
            while !Task.isCancelled {
                await self?.tick()
                try? await Task.sleep(for: .seconds(30))
            }
        }
    }

    public func requestAccess() async {
        do {
            _ = try await reader.store.requestFullAccessToEvents()
        } catch {}
        access = EKEventStore.authorizationStatus(for: .event)
        await reloadCalendars()
    }

    public func reloadCalendars() async {
        access = EKEventStore.authorizationStatus(for: .event)
        guard hasAccess else {
            calendars = []
            return
        }
        reader.store.refreshSourcesIfNecessary()
        calendars = (try? await reader.calendars()) ?? []
    }

    public func isMirrored(_ cal: MirrorCalendarInfo) -> Bool {
        settings.calendars.contains { $0.calendarID == cal.id }
    }

    public func setMirrored(_ cal: MirrorCalendarInfo, _ on: Bool) {
        settings.calendars.removeAll { $0.calendarID == cal.id }
        if on { settings.calendars.append(MirrorSelection(calendarID: cal.id, title: cal.title)) }
        settings.save()
        if on { changedAt = .distantPast }
    }

    /// Runs a pass now (the loop's runs and this one never overlap).
    public func runNow() async {
        guard !running else { return }
        guard let server = ServerStore().load(), let client = try? ClientFactory.make(for: server) else { return }
        running = true
        defer { running = false }
        lastAttempt = Date()
        lastRun = await engine.run(client: client, reader: reader, selections: settings.calendars, deviceID: server.deviceID)
    }

    /// Sign out: forget the selection and which server objects are ours.
    public func signedOut() async {
        settings = MirrorSettings()
        settings.save()
        lastRun = nil
        await engine.reset()
    }

    private func tick() async {
        access = EKEventStore.authorizationStatus(for: .event)
        guard hasAccess, !settings.calendars.isEmpty else { return }
        let now = Date()
        let changed = changedAt.map { now.timeIntervalSince($0) >= Self.settleDelay } ?? false
        guard changed || now.timeIntervalSince(lastAttempt) >= Self.interval else { return }
        changedAt = nil
        await runNow()
    }
}
#endif
