import SwiftUI
import NubiloKit

/// Members and devices: admin tools for admins, the user's own devices,
/// app passwords and storage for everyone.
struct PeopleView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        List {
            if model.isAdmin {
                Section("Everyone") {
                    NavigationLink { MembersView() } label: {
                        Label("Members", systemImage: "person.2")
                    }
                    NavigationLink { GroupsView() } label: {
                        Label("Groups", systemImage: "person.3")
                    }
                    NavigationLink { InviteView() } label: {
                        Label("Invite Someone", systemImage: "qrcode")
                    }
                    NavigationLink { AllDevicesView() } label: {
                        Label("All Devices", systemImage: "laptopcomputer.and.iphone")
                    }
                }
            }
            Section("You") {
                NavigationLink { MyDevicesView() } label: {
                    Label("My Devices", systemImage: "iphone")
                }
                NavigationLink { AppPasswordsView() } label: {
                    Label("App Passwords", systemImage: "key")
                }
                NavigationLink { WebSignInView() } label: {
                    Label("Sign In on the Web", systemImage: "globe")
                }
                if let storage = model.me?.storage {
                    LabeledContent("Storage used") {
                        Text("\(storage.bytes.usage(of: storage.quotaBytes)) in \(storage.objects) items")
                    }
                }
            }
        }
        .navigationTitle("People")
        .refreshable { await model.refreshMe() }
    }
}
