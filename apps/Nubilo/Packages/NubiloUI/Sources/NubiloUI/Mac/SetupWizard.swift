#if os(macOS)
import AppKit
import Foundation
import Observation

/// Drives `nubilo setup --json --step N` one step at a time
/// (internal/setup/wizard.go). Steps are idempotent, so a failed step can be
/// retried after fixing its input.
@MainActor
@Observable
public final class SetupWizard {
    public enum Mode: String, CaseIterable, Identifiable {
        case relay, proxy, tailscale, direct, lan
        public var id: String { rawValue }

        var title: String {
            switch self {
            case .relay: "Relay"
            case .proxy: "Relay behind a proxy"
            case .tailscale: "Tailscale"
            case .direct: "Direct"
            case .lan: "Home network only"
            }
        }

        var explanation: String {
            switch self {
            case .relay: "A small VPS runs `nubilo relay`. Nothing opens on your router. Recommended."
            case .proxy: "`nubilo relay --behind-proxy` runs on a host such as exe.dev. Its proxy holds the certificate and can read traffic."
            case .tailscale: "Devices on your tailnet connect by this Mac's MagicDNS name, with a certificate from `tailscale cert`. Turn on MagicDNS and HTTPS Certificates in the Tailscale admin console first."
            case .direct: "This Mac is reachable from the internet on ports 443 and 80 (port forward)."
            case .lan: "Only devices on your home network can connect. Uses a self-signed certificate."
            }
        }

        /// The mode needs a public domain name.
        var needsDomain: Bool { self == .relay || self == .proxy || self == .direct }
        /// The mode joins a relay with an invite.
        var needsInvite: Bool { self == .relay || self == .proxy }
        /// The mode gets a Let's Encrypt certificate, so a notice email helps.
        var usesACME: Bool { self == .relay || self == .direct }
    }

    public enum StepState: Equatable {
        case pending, running, done(String), failed(String)
    }

    public struct Step: Identifiable {
        public let number: Int
        public let title: String
        public var state = StepState.pending
        public var id: Int { number }
    }

    public var mode = Mode.relay
    public var domain = ""
    public var email = ""
    public var relayInvite = ""
    public private(set) var steps: [Step] = [
        Step(number: 1, title: "Create the server"),
        Step(number: 2, title: "Set up reachability"),
        Step(number: 3, title: "Start the server at login"),
        Step(number: 4, title: "Check the server answers"),
        Step(number: 5, title: "Create your admin invite"),
    ]
    public private(set) var running = false
    public private(set) var inviteQR: NSImage?
    public private(set) var inviteURL: String?
    public private(set) var setupURL: String?

    private let cli: NubiloCLI?

    public init(cli: NubiloCLI? = NubiloCLI.bundled()) { self.cli = cli }

    /// Why the form cannot run yet, if anything is missing.
    public var missingInput: String? {
        if cli == nil { return "The nubilo command-line tool is missing from this build of the app." }
        if mode.needsDomain && domain.trimmingCharacters(in: .whitespaces).isEmpty { return "Enter the public domain." }
        if mode.needsInvite && relayInvite.trimmingCharacters(in: .whitespaces).isEmpty { return "Paste the relay invite." }
        return nil
    }

    public var firstUnfinished: Int? {
        steps.first { if case .done = $0.state { return false } else { return true } }?.number
    }

    public var finished: Bool { firstUnfinished == nil }

    /// Runs from `step` (default: the first unfinished one) to the end, stopping at a failure.
    public func run(from step: Int? = nil) async {
        guard let cli, !running, let start = step ?? firstUnfinished else { return }
        running = true
        defer { running = false }
        for index in steps.indices where steps[index].number >= start {
            steps[index].state = .running
            let result = await runStep(steps[index].number, cli: cli)
            steps[index].state = result
            if case .failed = result { return }
        }
    }

    private func runStep(_ number: Int, cli: NubiloCLI) async -> StepState {
        let output: NubiloCLI.Output
        do {
            output = try await cli.run(arguments(for: number))
        } catch {
            return .failed(describe(error))
        }
        // Each step prints exactly one JSON line on stdout.
        let line = String(decoding: output.stdout, as: UTF8.self)
            .split(separator: "\n").last { !$0.trimmingCharacters(in: .whitespaces).isEmpty }
        guard let line, let result = try? JSONDecoder().decode(SetupStepResult.self, from: Data(line.utf8)) else {
            return .failed(output.errorText)
        }
        guard result.ok else { return .failed(result.error ?? output.errorText) }
        if number == 5, let data = result.data {
            inviteURL = data["url"]?.string
            setupURL = data["setup_url"]?.string
            inviteQR = data["qr_png"]?.string.flatMap(Self.image(fromDataURL:))
        }
        return .done(result.message ?? "Done")
    }

    func arguments(for step: Int) -> [String] {
        var args = ["setup", "--json", "--step", String(step), "--mode", mode.rawValue]
        let domain = domain.trimmingCharacters(in: .whitespaces)
        let email = email.trimmingCharacters(in: .whitespaces)
        let invite = relayInvite.trimmingCharacters(in: .whitespacesAndNewlines)
        if mode.needsDomain, !domain.isEmpty { args += ["--domain", domain] }
        if mode.usesACME, !email.isEmpty { args += ["--email", email] }
        if mode.needsInvite, !invite.isEmpty { args += ["--relay-invite", invite] }
        return args
    }

    /// Decodes "data:image/png;base64,…".
    static func image(fromDataURL string: String) -> NSImage? {
        guard let comma = string.firstIndex(of: ","),
              let data = Data(base64Encoded: String(string[string.index(after: comma)...])) else { return nil }
        return NSImage(data: data)
    }
}
#endif
