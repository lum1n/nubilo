#if os(macOS)
import SwiftUI
import NubiloKit

/// The menu bar window: server and agent health with start/stop and setup.
public struct MenuBarView: View {
    @Environment(ServerController.self) private var server
    @Environment(AppModel.self) private var app
    @Environment(\.openWindow) private var openWindow
    @State private var update = ServerUpdateModel()

    public init() {}

    public var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            if update.supported, let release = update.status?.available {
                updateRow(release)
                Divider()
            }
            serverSection
            Divider()
            agentSection
            Divider()
            HStack {
                Button("Open Nubilo") {
                    openWindow(id: "main")
                    NSApp.activate(ignoringOtherApps: true)
                }
                Spacer()
                Button("Refresh") { Task { await server.refresh() } }
                Button("Quit") { NSApp.terminate(nil) }
            }
        }
        .padding()
        .frame(width: 340)
        .task { await server.refresh() }
        .task(id: app.me?.user.isAdmin) {
            guard app.me?.user.isAdmin != false else { return }
            await update.load(client: app.client)
        }
        // Update errors go to the menu's one alert.
        .onChange(of: update.error) { _, message in
            guard let message else { return }
            server.message = message
            update.error = nil
        }
        .alert("Nubilo", isPresented: Binding(get: { server.message != nil }, set: { if !$0 { server.message = nil } })) {
            Button("OK", role: .cancel) {}
        } message: {
            Text(server.message ?? "")
        }
    }

    private var serverSection: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                StatusDot(color: serviceColor)
                Text("Server").font(.headline)
                Spacer()
                Text(serviceText).foregroundStyle(.secondary)
            }
            if let summary = server.summary {
                Text("\(summary.listen) · \(summary.devices) devices · \(summary.version)")
                    .font(.caption).foregroundStyle(.secondary)
            }
            // Doctor checks ~/.nubilo; without it they only list missing files.
            if server.isServerInitialized, let doctor = server.doctor {
                DoctorSummary(report: doctor)
            } else if server.service == .running || server.service == .stopped {
                Text("A server service is installed on this Mac, but it has no data in ~/.nubilo. Stop removes the service.")
                    .font(.caption).foregroundStyle(.secondary)
            } else if !server.isServerInitialized {
                Text("No server runs on this Mac. That is normal when your server is on another computer.")
                    .font(.caption).foregroundStyle(.secondary)
            }
            HStack {
                if !server.isServerInitialized {
                    if server.service == .running || server.service == .stopped {
                        Button("Stop") { Task { await server.stop() } }
                    }
                    Button("Set Up Server…") {
                        openWindow(id: "setup")
                        NSApp.activate(ignoringOtherApps: true)
                    }
                } else if server.service == .running {
                    Button("Stop") { Task { await server.stop() } }
                } else {
                    Button("Start") { Task { await server.start() } }
                }
                if server.isServerInitialized {
                    Button("Setup Wizard…") {
                        openWindow(id: "setup")
                        NSApp.activate(ignoringOtherApps: true)
                    }
                }
                if server.busy { ProgressView().controlSize(.small) }
            }
            .disabled(server.busy || server.cli == nil)
        }
    }

    /// The home server has an update (admins only).
    private func updateRow(_ release: ServerUpdateStatus.Release) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack {
                Image(systemName: "arrow.down.circle.fill")
                    .foregroundStyle(.tint)
                Text("Update to \(release.version)").font(.headline)
                Spacer()
                if update.phaseText != nil {
                    ProgressView().controlSize(.small)
                } else {
                    Button("Update") { Task { await update.install(client: app.client) } }
                        .disabled(update.status?.blockedReason != nil)
                }
            }
            if let phase = update.phaseText {
                Text(phase).font(.caption).foregroundStyle(.secondary)
            } else if let reason = update.status?.blockedReason {
                Text(reason).font(.caption).foregroundStyle(.orange)
            } else {
                Text("For your Nubilo server. Details in Settings.").font(.caption).foregroundStyle(.secondary)
            }
        }
    }

    private var agentSection: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                StatusDot(color: agentColor)
                Text("Mac agent").font(.headline)
                Spacer()
            }
            if let report = server.agentDoctor {
                DoctorSummary(report: report)
            } else {
                Text("Not set up on this Mac. It syncs chosen folders and can mirror Mac calendars (e.g. Exchange) to Nubilo; run `nubilo agent setup` to pair it. Photos back up from the app itself.")
                    .font(.caption).foregroundStyle(.secondary)
            }
            Button("Open Agent Settings") { server.openAgentSettings() }
                .disabled(server.cli == nil)
        }
    }

    private var serviceText: String {
        switch server.service {
        case .unknown: "Checking…"
        case .notInstalled: server.isServerInitialized ? "Not started" : "Not set up"
        case .stopped: "Stopped"
        case .running: server.isServerInitialized ? "Running" : "Running without data"
        }
    }

    private var serviceColor: Color {
        switch server.service {
        case .running:
            !server.isServerInitialized || server.doctor?.healthy == false ? .orange : .green
        case .stopped: .red
        // Gray, not red: most Macs only run the app against a server elsewhere.
        case .notInstalled: server.isServerInitialized ? .red : .gray
        case .unknown: .gray
        }
    }

    private var agentColor: Color {
        guard let report = server.agentDoctor else { return .gray }
        if !report.healthy { return .red }
        return report.warn > 0 ? .orange : .green
    }
}

private struct StatusDot: View {
    let color: Color
    var body: some View {
        Circle().fill(color).frame(width: 8, height: 8)
    }
}

/// Counts plus the failing and warning checks with their fixes.
private struct DoctorSummary: View {
    let report: DoctorReport

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text("\(report.ok) ok · \(report.warn) warnings · \(report.fail) problems")
                .font(.caption).foregroundStyle(.secondary)
            let problems = report.checks.filter { $0.status == "fail" || $0.status == "warn" }
            ForEach(Array(problems.enumerated()), id: \.offset) { _, check in
                VStack(alignment: .leading, spacing: 1) {
                    Label(check.title, systemImage: check.status == "fail" ? "xmark.octagon" : "exclamationmark.triangle")
                        .foregroundStyle(check.status == "fail" ? Color.red : Color.orange)
                    if let fix = check.fix, !fix.isEmpty {
                        Text(fix).font(.caption.monospaced()).foregroundStyle(.secondary).textSelection(.enabled)
                    }
                }
                .font(.caption)
            }
        }
    }
}
#endif
