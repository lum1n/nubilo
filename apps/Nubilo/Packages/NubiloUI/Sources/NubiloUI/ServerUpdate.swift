import SwiftUI
import NubiloKit

/// The home server's updates, for admins: what is available, installing it,
/// and the nightly setting. Servers without the updater, and non-admins,
/// answer 403/404; `supported` turns false and the UI hides.
@MainActor
@Observable
final class ServerUpdateModel {
    private(set) var status: ServerUpdateStatus?
    private(set) var supported = true
    /// An install was started here and its outcome is not in yet.
    private(set) var installing = false
    /// The server stopped answering mid-install: it is restarting.
    private(set) var waitingForRestart = false
    private(set) var checking = false
    var error: String?

    /// How long an install may take, restart included, before giving up.
    private let installTimeout: Duration = .seconds(180)

    func load(client: NubiloClient?) async {
        await run(client) { try await $0.updateStatus() }
    }

    func check(client: NubiloClient?) async {
        checking = true
        defer { checking = false }
        await run(client) { try await $0.checkForUpdate() }
    }

    func setAuto(_ on: Bool, client: NubiloClient?) async {
        await run(client) { try await $0.setAutoUpdate(on) }
    }

    func install(client: NubiloClient?) async {
        guard let client, let before = status, !installing else { return }
        installing = true
        defer {
            installing = false
            waitingForRestart = false
        }
        do {
            status = try await client.installUpdate()
        } catch {
            if !handleUnsupported(error) { self.error = describe(error) }
            return
        }
        // Poll until the server reports how it went. It restarts on the way,
        // so failed requests are expected for a while.
        let deadline = ContinuousClock.now + installTimeout
        while ContinuousClock.now < deadline {
            try? await Task.sleep(for: .seconds(2))
            if Task.isCancelled { return }
            do {
                let s = try await client.updateStatus()
                status = s
                waitingForRestart = false
                if !s.isBusy && (s.current != before.current || s.last != before.last) { return }
            } catch {
                if handleUnsupported(error) { return }
                waitingForRestart = true
            }
        }
        error = "The server has not come back from the update yet. It may still be finishing; check again in a minute."
    }

    private func run(_ client: NubiloClient?, _ op: (NubiloClient) async throws -> ServerUpdateStatus) async {
        guard let client else { return }
        do {
            status = try await op(client)
            supported = true
        } catch {
            if !handleUnsupported(error) { self.error = describe(error) }
        }
    }

    private func handleUnsupported(_ error: Error) -> Bool {
        guard error is ServerUpdateUnsupported else { return false }
        supported = false
        status = nil
        return true
    }

    /// What is happening now, while busy.
    var phaseText: String? {
        if waitingForRestart { return "Restarting the server…" }
        guard let s = status, s.isBusy || installing else { return nil }
        switch s.state {
        case "checking": return "Checking for updates…"
        case "downloading":
            if let p = s.progress { return "Downloading… \(Int(p * 100))%" }
            return "Downloading…"
        case "preflight": return "Checking the update…"
        case "installing": return "Installing…"
        case "restarting": return "Restarting the server…"
        default: return "Starting the update…"
        }
    }

    /// The last install's outcome, if it is recent enough to mention.
    var recentOutcome: ServerUpdateStatus.Outcome? {
        guard let last = status?.last else { return nil }
        if let at = last.at, Date().timeIntervalSince1970 * 1000 - Double(at) > 7 * 24 * 3600 * 1000 { return nil }
        return last
    }
}

/// The Settings section: hidden unless the server has an updater and the
/// user is an admin.
struct ServerUpdateSection: View {
    @Environment(AppModel.self) private var app
    @State private var update = ServerUpdateModel()

    var body: some View {
        @Bindable var update = update
        Group {
            if update.supported, let status = update.status {
                Section {
                    ServerUpdateCard(model: update, status: status)
                } header: {
                    Text("Server Updates")
                } footer: {
                    Text("Updates are signed by Nubilo. The server backs up its data first and goes back to the previous version by itself if the new one does not start.")
                }
            }
        }
        .task(id: app.me?.user.isAdmin) {
            // Members get 403 anyway; skip the request once we know.
            guard app.me?.user.isAdmin != false else { return }
            await update.load(client: app.client)
        }
        .errorAlert($update.error, title: "Server Update")
    }
}

/// Available version, notes and Update; progress while installing; the
/// outcome; the nightly toggle.
struct ServerUpdateCard: View {
    let model: ServerUpdateModel
    let status: ServerUpdateStatus

    @Environment(AppModel.self) private var app

    var body: some View {
        if let phase = model.phaseText {
            HStack(spacing: 10) {
                ProgressView()
                    .controlSize(.small)
                Text(phase)
            }
            if status.state == "downloading", let p = status.progress, !model.waitingForRestart {
                ProgressView(value: p)
            }
        } else if let release = status.available {
            VStack(alignment: .leading, spacing: 6) {
                Text("Nubilo \(release.version) is available")
                    .font(.headline)
                if let notes = release.notes, !notes.isEmpty {
                    Text(Self.markdown(notes))
                        .font(.callout)
                        .foregroundStyle(.secondary)
                }
            }
            if let reason = status.blockedReason {
                Label(reason, systemImage: "exclamationmark.triangle")
                    .foregroundStyle(.orange)
                    .font(.callout)
            }
            Button("Update to \(release.version)") {
                Task { await model.install(client: app.client) }
            }
            .disabled(status.blockedReason != nil || model.installing)
        } else {
            VStack(alignment: .leading, spacing: 2) {
                Text("Nubilo \(status.current) is up to date")
                if let checked = status.checkedAt, checked > 0 {
                    Text("Checked \(Self.date(checked).relative)")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
            }
            Button("Check Now") {
                Task { await model.check(client: app.client) }
            }
            .disabled(model.checking)
        }

        if model.phaseText == nil, let last = model.recentOutcome {
            if last.succeeded {
                Label("Updated to \(last.to)", systemImage: "checkmark.circle")
                    .foregroundStyle(.green)
            } else {
                let why = (last.message ?? "").isEmpty ? "" : ": \(last.message!)"
                Label("The update to \(last.to) failed and the server went back to \(last.from)\(why)",
                      systemImage: "arrow.uturn.backward.circle")
                    .foregroundStyle(.orange)
            }
        }

        Toggle("Install updates automatically at night", isOn: Binding(
            get: { status.auto },
            set: { on in Task { await model.setAuto(on, client: app.client) } }
        ))
        .disabled(model.installing)
        if status.auto, status.available != nil, status.blockedReason == nil, let next = status.nextAutoAt, next > 0 {
            Text("Installs by itself around \(Self.date(next).formatted(date: .omitted, time: .shortened)).")
                .font(.caption)
                .foregroundStyle(.secondary)
        }
    }

    static func date(_ ms: Int64) -> Date { Date(timeIntervalSince1970: Double(ms) / 1000) }

    static func markdown(_ s: String) -> AttributedString {
        (try? AttributedString(markdown: s, options: .init(interpretedSyntax: .inlineOnlyPreservingWhitespace)))
            ?? AttributedString(s)
    }
}
