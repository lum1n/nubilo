import SwiftUI
import NubiloKit

/// WebDAV access details and a one-time app password for file clients.
struct FilesView: View {
    @Environment(AppModel.self) private var model
    @State private var created: NewAppPassword?
    @State private var working = false
    @State private var error: String?
    #if os(iOS) && canImport(FileProvider)
    @State private var locationStatus: String?
    @State private var locationOK = false
    @State private var checking = false
    #endif

    var body: some View {
        Form {
            #if os(iOS)
            Section {
                Text("Your folders are in the Files app: open Files, tap Browse and choose Nubilo. Files you add there are stored on your server.")
                Button("Open the Files App") {
                    if let url = URL(string: "shareddocuments://") { ExternalURL.open(url) }
                }
                #if canImport(FileProvider)
                if let locationStatus {
                    Label(locationStatus, systemImage: locationOK ? "checkmark.circle" : "exclamationmark.triangle")
                        .foregroundStyle(locationOK ? Color.secondary : Color.red)
                        .textSelection(.enabled)
                }
                Button {
                    Task { await setUpLocation() }
                } label: {
                    HStack {
                        Text("Set Up the Files Location Again")
                        if checking { Spacer(); ProgressView() }
                    }
                }
                .disabled(checking || model.client == nil)
                #endif
            } header: {
                Text("On this device")
            } footer: {
                Text("Nubilo is listed under Locations in Files → Browse. If it is missing, use the button above and check the message.")
            }
            #endif
            Section(Self.connectionTitle) {
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
                    Text("Copy it now: it is shown only once. Revoke it under People → App Passwords.")
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
        .task {
            if model.me == nil { await model.refreshMe() }
            #if os(iOS) && canImport(FileProvider)
            await checkLocation()
            #endif
        }
    }

    #if os(iOS)
    static let connectionTitle = "Other apps and computers"
    // This device uses the Files app integration; the WebDAV details are for
    // other apps and for computers.
    static let guide = [
        "On a Mac: Finder → Go → Connect to Server (⌘K), paste the server address and sign in with the username and password above.",
        "In another app that supports WebDAV, add a connection with the same details.",
    ]
    #else
    static let connectionTitle = "Connection"
    static let guide = [
        "In Finder choose Go → Connect to Server (⌘K).",
        "Paste the server address above and click Connect.",
        "Choose Registered User and enter the username and password above.",
        "Tick “Remember this password in my keychain” to reconnect automatically.",
    ]
    #endif

    #if os(iOS) && canImport(FileProvider)
    private func checkLocation() async {
        if let problem = FilesIntegration.problem {
            locationOK = false
            locationStatus = problem
        } else if await FilesIntegration.isRegistered() {
            // Try what the extension does: list every folder and file.
            guard let client = model.client else { return }
            do {
                let items = try await FilesSession(client: client).allItems()
                locationOK = true
                locationStatus = "Nubilo is set up in the Files app (\(items.filter { $0.kind == .folder }.count) folders, \(items.filter { $0.kind == .file }.count) files)."
            } catch {
                locationOK = false
                locationStatus = "The Files location is set up, but listing files fails: \(describe(error))"
            }
        } else {
            locationOK = false
            locationStatus = FilesIntegration.extensionInstalled
                ? "Nubilo is not registered with the Files app yet."
                : "This build of the app does not contain the Files extension."
        }
    }

    private func setUpLocation() async {
        checking = true
        defer { checking = false }
        if let problem = await model.setUpFilesLocation() {
            locationOK = false
            locationStatus = problem
        } else {
            await checkLocation()
        }
    }
    #endif

    private func createPassword() async {
        guard let client = model.client else { return }
        working = true
        defer { working = false }
        do {
            created = try await client.createAppPassword(label: "Files on \(ThisDevice.defaultName)", scope: "webdav")
        } catch let NubiloError.http(status, _) where status == 409 {
            error = "You already have 10 app passwords. Revoke one under People → App Passwords first."
        } catch {
            self.error = describe(error)
        }
    }
}
