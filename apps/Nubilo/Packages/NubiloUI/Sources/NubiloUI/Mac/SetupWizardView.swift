#if os(macOS)
import SwiftUI

/// The server setup window on the Mac.
public struct SetupWizardView: View {
    @State private var wizard = SetupWizard()
    @Environment(ServerController.self) private var server

    public init() {}

    public var body: some View {
        @Bindable var wizard = wizard
        Form {
            Section("How will your devices reach this Mac from outside your home?") {
                Picker("Reachability", selection: $wizard.mode) {
                    ForEach(SetupWizard.Mode.allCases) { Text($0.title).tag($0) }
                }
                .labelsHidden()
                Text(wizard.mode.explanation).foregroundStyle(.secondary)
                if wizard.mode.needsDomain {
                    TextField("Public domain", text: $wizard.domain,
                              prompt: Text(wizard.mode == .proxy ? "nubilo.exe.xyz" : "cloud.example.com"))
                }
                if wizard.mode.usesACME {
                    TextField("Email for certificate notices (optional)", text: $wizard.email)
                }
                if wizard.mode.needsInvite {
                    TextField(wizard.mode == .proxy
                              ? "Relay invite (from `nubilo relay init --behind-proxy` on the proxied host)"
                              : "Relay invite (from `nubilo relay init` on the VPS)",
                              text: $wizard.relayInvite, axis: .vertical)
                        .lineLimit(2...4)
                }
            }
            .disabled(wizard.running)

            Section("Steps") {
                ForEach(wizard.steps) { step in
                    StepRow(step: step)
                }
                HStack {
                    if let missing = wizard.missingInput {
                        Text(missing).foregroundStyle(.secondary)
                    }
                    Spacer()
                    Button(wizard.firstUnfinished == 1 ? "Set Up" : "Continue") {
                        Task {
                            await wizard.run()
                            await server.refresh()
                        }
                    }
                    .buttonStyle(.borderedProminent)
                    .disabled(wizard.running || wizard.finished || wizard.missingInput != nil)
                }
            }

            if wizard.finished {
                Section("Add your first device") {
                    if let qr = wizard.inviteQR {
                        Image(nsImage: qr)
                            .interpolation(.none)
                            .resizable()
                            .scaledToFit()
                            .frame(width: 240, height: 240)
                            .frame(maxWidth: .infinity)
                    } else if let setupURL = wizard.setupURL {
                        QRCodeImage(text: setupURL).frame(width: 240, height: 240).frame(maxWidth: .infinity)
                    }
                    Text("Scan this with the Nubilo app on your iPhone to become the admin. The invite works once and expires in 24 hours.")
                    if let url = wizard.inviteURL {
                        CopyableRow(title: "Link", value: url)
                    }
                }
            }
        }
        .formStyle(.grouped)
        .frame(minWidth: 520, minHeight: 560)
        .navigationTitle("Set Up Nubilo Server")
    }
}

private struct StepRow: View {
    let step: SetupWizard.Step

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 10) {
            icon.frame(width: 18)
            VStack(alignment: .leading, spacing: 2) {
                Text("\(step.number). \(step.title)")
                switch step.state {
                case let .done(message):
                    Text(message).font(.caption).foregroundStyle(.secondary)
                case let .failed(error):
                    Text(error).font(.caption).foregroundStyle(.red).textSelection(.enabled)
                default:
                    EmptyView()
                }
            }
        }
    }

    @ViewBuilder
    private var icon: some View {
        switch step.state {
        case .pending: Image(systemName: "circle").foregroundStyle(.secondary)
        case .running: ProgressView().controlSize(.small)
        case .done: Image(systemName: "checkmark.circle.fill").foregroundStyle(.green)
        case .failed: Image(systemName: "xmark.circle.fill").foregroundStyle(.red)
        }
    }
}
#endif
