import SwiftUI
import NubiloKit

/// First launch: get a setup link (QR, paste or deep link), then join.
public struct OnboardingView: View {
    @Environment(AppModel.self) private var model
    @State private var link: SetupLink?

    public init() {}

    public var body: some View {
        NavigationStack {
            Group {
                if let link {
                    JoinFormView(link: link) { self.link = nil }
                } else {
                    LinkEntryView { link = $0 }
                }
            }
            .navigationTitle("Welcome to Nubilo")
        }
        .onAppear(perform: takePendingLink)
        .onChange(of: model.pendingLink) { _, _ in takePendingLink() }
    }

    private func takePendingLink() {
        guard let pending = model.pendingLink else { return }
        link = pending
        model.pendingLink = nil
    }
}

/// Scan a QR code (iPhone/iPad) or paste a setup link.
struct LinkEntryView: View {
    let onLink: (SetupLink) -> Void
    @State private var pasted = ""
    @State private var error: String?

    var body: some View {
        Form {
            Section {
                Text("Ask the person who runs your Nubilo server for an invite. It is a QR code or a link that starts with nubilo://setup or ends in /join#….")
                    .foregroundStyle(.secondary)
            }
            #if os(iOS)
            Section("Scan the invite") {
                QRScannerSection { accept($0) }
            }
            #endif
            Section("Or paste the invite link") {
                TextField("nubilo://setup?… or https://…/join#…", text: $pasted, axis: .vertical)
                    .plainTextInput()
                    .onSubmit { accept(pasted) }
                HStack {
                    PasteButton(payloadType: String.self) { strings in
                        if let first = strings.first { pasted = first }
                    }
                    Spacer()
                    Button("Continue") { accept(pasted) }
                        .buttonStyle(.borderedProminent)
                        .disabled(pasted.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                }
            }
        }
        .formStyle(.grouped)
        .errorAlert($error, title: "Not a Nubilo invite")
    }

    private func accept(_ text: String) {
        do {
            onLink(try SetupLink(parsing: text))
        } catch {
            self.error = describe(error)
        }
    }
}
