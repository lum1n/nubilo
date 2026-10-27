#if os(macOS)
import AppKit
import Foundation
import Observation

/// State of the home server and the Mac agent on this Mac, read through the
/// bundled CLI.
///
/// The agent's loopback API (internal/ui/agent.go) needs the random session
/// token that `nubilo agent ui` prints when it starts, so it cannot be polled
/// from here; agent health comes from `nubilo doctor --agent --json` instead,
/// and its settings open in the browser through `nubilo agent ui`.
@MainActor
@Observable
public final class ServerController {
    public enum ServiceState: Equatable {
        case unknown, notInstalled, stopped, running
    }

    public let cli: NubiloCLI?
    public private(set) var service = ServiceState.unknown
    public private(set) var summary: ServerSummary?
    public private(set) var doctor: DoctorReport?
    public private(set) var agentDoctor: DoctorReport?
    public private(set) var busy = false
    public var message: String?

    @ObservationIgnored private var agentUI: Process?
    @ObservationIgnored private var terminationObserver: NSObjectProtocol?

    /// Default data directories used by the CLI (internal/app.ResolveDataDir,
    /// `nubilo agent setup`).
    public static let agentDataDir = FileManager.default.homeDirectoryForCurrentUser
        .appendingPathComponent(".nubilo-agent").path

    public init(cli: NubiloCLI? = NubiloCLI.bundled()) {
        self.cli = cli
        terminationObserver = NotificationCenter.default.addObserver(
            forName: NSApplication.willTerminateNotification, object: nil, queue: .main
        ) { [weak self] _ in
            MainActor.assumeIsolated { self?.agentUI?.terminate() }
        }
    }

    /// `status` can fail while the running server holds the store, so a
    /// readable config.json in the doctor report counts too.
    public var isServerInitialized: Bool {
        summary != nil || doctor?.checks.contains { $0.id == "config" && $0.status == "ok" } == true
    }

    public func refresh() async {
        guard let cli else {
            message = "The nubilo command-line tool is missing from this build of the app."
            return
        }
        async let serviceOut = cli.output(["server", "service"])
        async let statusOut = cli.output(["--json", "status"])
        async let doctorOut = cli.output(["doctor", "--json"])
        async let agentOut = cli.output(["doctor", "--agent", "--json", "--data-dir", Self.agentDataDir])

        service = Self.parseService(await serviceOut)
        if let out = await statusOut, out.status == 0 {
            summary = try? JSONDecoder.snakeCase.decode(ServerSummary.self, from: out.stdout)
        } else {
            summary = nil
        }
        // doctor exits 1 when unhealthy but still prints the report.
        doctor = (await doctorOut).flatMap { try? JSONDecoder.snakeCase.decode(DoctorReport.self, from: $0.stdout) }
        agentDoctor = (await agentOut).flatMap { try? JSONDecoder.snakeCase.decode(DoctorReport.self, from: $0.stdout) }
    }

    /// Installs and starts the LaunchAgent that runs `nubilo server`.
    public func start() async { await perform(["server", "install"]) }

    /// Stops the server and removes its LaunchAgent; data stays in ~/.nubilo.
    public func stop() async { await perform(["server", "uninstall"]) }

    /// Opens the agent's folder selection UI in the browser.
    public func openAgentSettings() {
        guard let cli else { return }
        // The UI server binds a fixed loopback port; restart it so a fresh
        // session link opens instead of failing with "address in use".
        if let running = agentUI, running.isRunning { running.terminate() }
        do {
            agentUI = try cli.launch(["agent", "ui", "--open", "--data-dir", Self.agentDataDir])
        } catch {
            message = describe(error)
        }
    }

    private func perform(_ arguments: [String]) async {
        guard let cli else { return }
        busy = true
        defer { busy = false }
        do {
            let out = try await cli.run(arguments)
            if out.status != 0 { message = out.errorText }
        } catch {
            message = describe(error)
        }
        await refresh()
    }

    /// Parses `nubilo server service` ("  installed: true", "  loaded: true").
    static func parseService(_ out: NubiloCLI.Output?) -> ServiceState {
        guard let out else { return .unknown }
        // The command fails when the LaunchAgent plist does not exist.
        guard out.status == 0 else { return .notInstalled }
        var installed = false
        var loaded = false
        for line in String(decoding: out.stdout, as: UTF8.self).split(separator: "\n") {
            let parts = line.split(separator: ":", maxSplits: 1).map { $0.trimmingCharacters(in: .whitespaces) }
            guard parts.count == 2 else { continue }
            if parts[0] == "installed" { installed = parts[1] == "true" }
            if parts[0] == "loaded" { loaded = parts[1] == "true" }
        }
        if !installed { return .notInstalled }
        return loaded ? .running : .stopped
    }
}
#endif
