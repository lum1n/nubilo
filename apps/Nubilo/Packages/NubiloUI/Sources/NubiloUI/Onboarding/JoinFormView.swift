import SwiftUI
import NubiloKit

/// Collects account details and pairs this device with the invite.
struct JoinFormView: View {
    let link: SetupLink
    let onCancel: () -> Void

    @Environment(AppModel.self) private var model
    @State private var username = ""
    @State private var displayName = ""
    @State private var deviceName = ""
    @State private var joining = false
    @State private var error: String?

    var body: some View {
        Form {
            Section {
                LabeledContent("Server", value: link.server.host() ?? link.server.absoluteString)
                if link.serverKey != nil {
                    Label("The server's identity will be checked against the invite.", systemImage: "checkmark.shield")
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                }
            }
            Section {
                TextField("Username", text: $username)
                    .plainTextInput()
                    .textContentType(.username)
                TextField("Your name", text: $displayName)
            } header: {
                Text("Your account")
            } footer: {
                // The API cannot tell in advance whether an invite creates an
                // account, so both fields are asked for and ignored when unused.
                Text("Lowercase letters, digits, dot, dash and underscore. If the invite is for an account that already exists, these are ignored.")
            }
            Section("This device") {
                TextField("Device name", text: $deviceName)
            }
            Section {
                Button {
                    Task { await join() }
                } label: {
                    HStack {
                        Text(joining ? "Joining…" : "Join")
                        if joining { Spacer(); ProgressView() }
                    }
                }
                .disabled(joining || deviceName.trimmingCharacters(in: .whitespaces).isEmpty)
                Button("Use a different invite", role: .cancel, action: onCancel)
                    .disabled(joining)
            }
        }
        .formStyle(.grouped)
        .onAppear {
            if deviceName.isEmpty { deviceName = ThisDevice.defaultName }
        }
        .errorAlert($error, title: "Could not join")
    }

    private func join() async {
        joining = true
        defer { joining = false }
        let user = username.trimmingCharacters(in: .whitespaces).lowercased()
        let name = displayName.trimmingCharacters(in: .whitespaces)
        let keys = KeychainKeyStore()
        let tag = "device-\(UUID().uuidString)"
        let pin = Locked<String?>(nil)
        do {
            let key = try keys.loadOrCreate(tag: tag)
            // Self-signed LAN servers: trust the certificate seen now and pin it.
            let transport = URLSessionTransport.pinned(nil) { pin.set($0) }
            var paired = try await Onboarding.join(
                link: link,
                username: user,
                displayName: name.isEmpty ? user : name,
                deviceName: deviceName.trimmingCharacters(in: .whitespaces),
                key: key,
                keyTag: tag,
                transport: transport
            )
            paired.tlsPin = pin.current
            try model.completePairing(paired)
        } catch {
            try? keys.delete(tag: tag)
            self.error = Self.message(for: error)
        }
    }

    static func message(for error: Error) -> String {
        switch error {
        case let NubiloError.http(status, body):
            switch status {
            case 409: return "That username is taken. Pick another one."
            case 400 where body.contains("username"):
                return "Choose a username of 1–32 characters: a–z, 0–9, dot, dash or underscore."
            case 400 where body.contains("name"):
                return "That device or display name is not allowed. Try a shorter, plain name."
            case 401: return "This invite is invalid, already used or expired. Ask for a new one."
            case 429: return "Too many attempts from this network. Wait an hour and try again."
            default: return describe(error)
            }
        case NubiloError.serverKeyMismatch:
            return describe(error)
        case let urlError as URLError:
            return "Could not reach the server: \(urlError.localizedDescription)"
        default:
            return describe(error)
        }
    }
}
