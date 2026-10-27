import SwiftUI
import NubiloKit
#if os(macOS)
import ServiceManagement
#endif

/// Server, account and sign out.
struct SettingsView: View {
    @Environment(AppModel.self) private var model
    @State private var status: ServerStatus?
    @State private var confirmSignOut = false
    @State private var signingOut = false
    #if os(macOS)
    @State private var openAtLogin = SMAppService.mainApp.status == .enabled
    #endif

    var body: some View {
        Form {
            if let server = model.server {
                Section("Server") {
                    CopyableRow(title: "Address", value: server.baseURL.absoluteString)
                    if let name = status?.serverName {
                        LabeledContent("Name", value: name)
                    }
                    if let version = status?.apiVersion {
                        LabeledContent("API version", value: "\(version)")
                    }
                    if server.tlsPin != nil {
                        LabeledContent("Certificate", value: "Self-signed, pinned")
                    }
                }
                Section("Account") {
                    LabeledContent("Username", value: model.me?.user.username ?? server.username)
                    if let me = model.me {
                        LabeledContent("Name", value: me.user.displayName)
                        LabeledContent("Role", value: me.user.isAdmin ? "Admin" : "Member")
                        LabeledContent("This device", value: me.device.name)
                    }
                }
            }
            #if os(macOS)
            Section {
                Toggle("Open at Login", isOn: $openAtLogin)
                    .onChange(of: openAtLogin) { _, on in setOpenAtLogin(on) }
            } footer: {
                Text("Nubilo backs up Photos and shows the menu bar only while it runs.")
            }
            #endif
            Section {
                Button("Sign Out", role: .destructive) { confirmSignOut = true }
                    .disabled(signingOut)
            } footer: {
                Text("Signing out removes this device from the server and deletes its key. Calendar and Contacts accounts added by a profile stay until you remove the profile.")
            }
        }
        .formStyle(.grouped)
        .navigationTitle("Settings")
        .confirmationDialog("Sign out of Nubilo?", isPresented: $confirmSignOut) {
            Button("Sign Out", role: .destructive) {
                signingOut = true
                Task {
                    await model.signOut()
                    ImageLoader.shared.removeAll()
                    signingOut = false
                }
            }
        }
        .task {
            status = try? await model.client?.status()
            await model.refreshMe()
        }
    }

    #if os(macOS)
    private func setOpenAtLogin(_ on: Bool) {
        let service = SMAppService.mainApp
        guard on != (service.status == .enabled) else { return }
        do {
            if on { try service.register() } else { try service.unregister() }
        } catch {
            model.alert = "Open at Login: \(describe(error))"
            openAtLogin = service.status == .enabled
        }
    }
    #endif
}
