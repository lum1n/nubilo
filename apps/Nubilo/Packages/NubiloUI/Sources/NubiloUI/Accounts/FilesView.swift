import SwiftUI
import NubiloKit

/// The Files app (iOS) or Finder (macOS) location, plus WebDAV access
/// details and a one-time app password for other file clients.
struct FilesView: View {
    @Environment(AppModel.self) private var model
    @State private var created: NewAppPassword?
    @State private var working = false
    @State private var error: String?
    #if (os(iOS) || os(macOS)) && canImport(FileProvider)
    @State private var locationStatus: String?
    @State private var locationOK = false
    @State private var checking = false
    #endif

    var body: some View {
        Form {
            #if (os(iOS) || os(macOS)) && canImport(FileProvider)
            Section {
                #if os(iOS)
                Text("Your folders are in the Files app: open Files, tap Browse and choose Nubilo. Files you add there are stored on your server.")
                Button("Open the Files App") {
                    if let url = URL(string: "shareddocuments://") { ExternalURL.open(url) }
                }
                #else
                Text("Your folders are in Finder: Nubilo is listed under Locations in the sidebar. Files are downloaded when you open them, and files you add there are stored on your server.")
                Button("Show in Finder") {
                    Task {
                        if let problem = await FilesIntegration.showInFinder() {
                            locationOK = false
                            locationStatus = problem
                        }
                    }
                }
                .disabled(!locationOK)
                #endif
                if let locationStatus {
                    Label(locationStatus, systemImage: locationOK ? "checkmark.circle" : "exclamationmark.triangle")
                        .foregroundStyle(locationOK ? Color.secondary : Color.red)
                        .textSelection(.enabled)
                }
                Button {
                    Task { await setUpLocation() }
                } label: {
                    HStack {
                        Text(Self.setUpAgainTitle)
                        if checking { Spacer(); ProgressView() }
                    }
                }
                .disabled(checking || model.client == nil)
            } header: {
                Text("On this device")
            } footer: {
                Text(Self.locationFooter)
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
            #if (os(iOS) || os(macOS)) && canImport(FileProvider)
            await checkLocation()
            #endif
        }
    }

    #if os(iOS) && canImport(FileProvider)
    static let setUpAgainTitle = "Set Up the Files Location Again"
    static let locationFooter = "Nubilo is listed under Locations in Files → Browse. If it is missing, use the button above and check the message."
    #elseif os(macOS) && canImport(FileProvider)
    static let setUpAgainTitle = "Set Up the Finder Location Again"
    static let locationFooter = "If Nubilo is missing from the Finder sidebar, use the button above and check the message, and make sure Nubilo is turned on in System Settings → General → Login Items & Extensions → File Providers."
    #endif

    #if os(iOS)
    static let connectionTitle = "Other apps and computers"
    // This device uses the Files app integration; the WebDAV details are for
    // other apps and for computers.
    static let guide = [
        "On a Mac with the Nubilo app, your folders are in Finder already. Without it: Finder → Go → Connect to Server (⌘K), paste the server address and sign in with the username and password above.",
        "In another app that supports WebDAV, add a connection with the same details.",
    ]
    #elseif canImport(FileProvider)
    // The Mac uses the Finder location; WebDAV remains for other apps and
    // for Macs without the app.
    static let connectionTitle = "WebDAV, for other apps and computers"
    static let guide = [
        "Other apps that support WebDAV, or a Mac without the Nubilo app: use the server address, username and password above.",
        "In Finder on such a Mac: Go → Connect to Server (⌘K), paste the server address and choose Registered User.",
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

    #if (os(iOS) || os(macOS)) && canImport(FileProvider)
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
                locationStatus = "Nubilo is set up in \(FilesIntegration.placeName) (\(items.filter { $0.kind == .folder }.count) folders, \(items.filter { $0.kind == .file }.count) files)."
            } catch {
                locationOK = false
                locationStatus = "The \(FilesIntegration.placeTitle) location is set up, but listing files fails: \(describe(error))"
            }
        } else {
            locationOK = false
            locationStatus = FilesIntegration.extensionInstalled
                ? "Nubilo is not registered with \(FilesIntegration.placeName) yet."
                : "This build of the app does not contain the File Provider extension."
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
