import SwiftUI
import NubiloKit

/// WebDAV access details and a one-time app password for file clients.
struct FilesView: View {
    @Environment(AppModel.self) private var model
    @State private var created: NewAppPassword?
    @State private var working = false
    @State private var error: String?

    var body: some View {
        Form {
            Section("Connection") {
                if let me = model.me {
                    if let dav = me.urls["webdav"] {
                        CopyableRow(title: "Server", value: dav)
                    }
                    CopyableRow(title: "Username", value: me.user.username)
                } else {
                    ProgressView()
                }
            }
            Section {
                if let created {
                    CopyableRow(title: "Password", value: created.password)
                    Text("Copy it now: it is shown only once. Revoke it under Family → App passwords.")
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                } else {
                    Button {
                        Task { await createPassword() }
                    } label: {
                        HStack {
                            Text("Create a password for Files")
                            if working { Spacer(); ProgressView() }
                        }
                    }
                    .disabled(working || model.client == nil)
                }
            } header: {
                Text("Password")
            } footer: {
                Text("File apps cannot sign requests like this app does, so they use an app password that only works for files.")
            }
            Section("Connect") {
                ForEach(Array(Self.guide.enumerated()), id: \.offset) { index, step in
                    HStack(alignment: .firstTextBaseline, spacing: 12) {
                        Text("\(index + 1).").monospacedDigit().foregroundStyle(.secondary)
                        Text(step)
                    }
                }
            }
        }
        .formStyle(.grouped)
        .navigationTitle("Files")
        .errorAlert($error)
        .task { if model.me == nil { await model.refreshMe() } }
    }

    #if os(iOS)
    // The Files app's "Connect to Server" only speaks SMB, so WebDAV needs a
    // client app; once added there it shows up as a location in Files.
    static let guide = [
        "Install a file app that supports WebDAV from the App Store.",
        "In that app, add a WebDAV connection with the server address, username and password above.",
        "If the app integrates with Files, turn it on in Files → ⋯ → Edit to browse it there too.",
        "On a Mac: Finder → Go → Connect to Server (⌘K), paste the server address and sign in.",
    ]
    #else
    static let guide = [
        "In Finder choose Go → Connect to Server (⌘K).",
        "Paste the server address above and click Connect.",
        "Choose Registered User and enter the username and password above.",
        "Tick “Remember this password in my keychain” to reconnect automatically.",
    ]
    #endif

    private func createPassword() async {
        guard let client = model.client else { return }
        working = true
        defer { working = false }
        do {
            created = try await client.createAppPassword(label: "Files on \(ThisDevice.defaultName)", scope: "webdav")
        } catch let NubiloError.http(status, _) where status == 409 {
            error = "You already have 10 app passwords. Revoke one under Family → App passwords first."
        } catch {
            self.error = describe(error)
        }
    }
}
