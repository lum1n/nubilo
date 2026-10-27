#if os(macOS) && canImport(EventKit)
import SwiftUI
import NubiloKit

/// Chooses Mac calendars (for example an Exchange account only this Mac
/// reaches) to copy to Nubilo, where other devices see them read-only.
struct CalendarMirrorView: View {
    @MainActor private var mirror: MacCalendarMirror { .shared }

    var body: some View {
        Form {
            Section {
                Text("Copy calendars that only this Mac can reach, such as a work Exchange account, to your Nubilo server. Your iPhone and other devices show them read-only in the Nubilo calendar account; nothing is changed on the Mac.")
                Text("Meetings from \(CalendarMirrorEngine.daysBack) days ago to a year ahead are copied, and a repeating meeting shows up as separate events. The copies update while Nubilo runs. Check your employer's policy before copying work calendars.")
                    .foregroundStyle(.secondary)
            }
            if !mirror.hasAccess {
                Section {
                    Button("Allow Access to Calendars") { Task { await mirror.requestAccess() } }
                } footer: {
                    Text("If it was turned off: System Settings → Privacy & Security → Calendars → Nubilo → Full Access.")
                }
            } else {
                ForEach(accounts, id: \.self) { account in
                    Section(account.isEmpty ? "Other" : account) {
                        ForEach(mirror.calendars.filter { $0.account == account }) { cal in
                            Toggle(isOn: Binding(get: { mirror.isMirrored(cal) }, set: { mirror.setMirrored(cal, $0) })) {
                                Label {
                                    Text(cal.title)
                                } icon: {
                                    Circle().fill(Color(hex: cal.color) ?? .secondary).frame(width: 10, height: 10)
                                }
                            }
                        }
                    }
                }
                Section {
                    Button {
                        Task { await mirror.runNow() }
                    } label: {
                        HStack {
                            Text(mirror.running ? "Copying…" : "Copy Now")
                            if mirror.running { Spacer(); ProgressView().controlSize(.small) }
                        }
                    }
                    .disabled(mirror.running || mirror.settings.calendars.isEmpty)
                    if let run = mirror.lastRun {
                        LabeledContent("Last copy", value: (run.finished ?? run.started).relative)
                        ForEach(run.calendars, id: \.title) { r in
                            LabeledContent(r.title == r.serverName ? r.title : "\(r.title) → \(r.serverName)",
                                           value: "\(r.events) events, \(r.pushed) updated, \(r.deleted) removed")
                        }
                        ForEach(Array(run.errors.prefix(5).enumerated()), id: \.offset) { _, message in
                            Label(message, systemImage: "exclamationmark.circle").foregroundStyle(.red)
                        }
                    }
                } footer: {
                    Text("Turning a calendar off stops updating its copy; delete the copy from any device if you no longer want it. If one of your own calendars has the same name, the copy is called “… (Mac)”.")
                }
            }
        }
        .formStyle(.grouped)
        .navigationTitle("Mirror Mac Calendars")
        .task { await mirror.reloadCalendars() }
    }

    private var accounts: [String] {
        var seen = Set<String>()
        return mirror.calendars.map(\.account).filter { seen.insert($0).inserted }
    }
}

private extension Color {
    init?(hex: String?) {
        guard let hex, hex.count == 7, hex.hasPrefix("#"), let v = Int(hex.dropFirst(), radix: 16) else { return nil }
        self.init(red: Double((v >> 16) & 0xFF) / 255, green: Double((v >> 8) & 0xFF) / 255, blue: Double(v & 0xFF) / 255)
    }
}
#endif
