import SwiftUI
import NubiloKit

/// Admin: groups of people. Sharing an album with a group shares it with
/// every member, including people added to the group later.
struct GroupsView: View {
    @Environment(AppModel.self) private var model
    @State private var groups: [UserGroup] = []
    @State private var users: [User] = []
    @State private var newName = ""
    @State private var creating = false
    @State private var error: String?

    var body: some View {
        List {
            Section {
                ForEach(groups) { group in
                    NavigationLink {
                        GroupDetailView(group: group, users: users) { await load() }
                    } label: {
                        VStack(alignment: .leading, spacing: 2) {
                            Text(group.name)
                            Text(memberNames(group)).font(.caption).foregroundStyle(.secondary).lineLimit(1)
                        }
                    }
                }
                .onDelete { offsets in
                    let doomed = offsets.map { groups[$0] }
                    Task {
                        for group in doomed { await delete(group) }
                    }
                }
            } footer: {
                Text("Deleting a group also stops everything shared with it.")
            }
        }
        .navigationTitle("Groups")
        .toolbar {
            ToolbarItem(placement: .primaryAction) {
                Button { creating = true } label: { Label("New Group", systemImage: "plus") }
            }
        }
        .alert("New Group", isPresented: $creating) {
            TextField("Name", text: $newName)
            Button("Create") { Task { await create() } }
            Button("Cancel", role: .cancel) { newName = "" }
        }
        .overlay {
            if groups.isEmpty {
                ContentUnavailableView("No groups", systemImage: "person.3",
                                       description: Text("Make a group to share albums with several people at once."))
            }
        }
        .refreshable { await load() }
        .task { await load() }
        .errorAlert($error)
    }

    private func memberNames(_ group: UserGroup) -> String {
        let names = group.members.compactMap { id in users.first { $0.id == id }?.displayName }
        return names.isEmpty ? "No members" : names.joined(separator: ", ")
    }

    private func load() async {
        guard let client = model.client else { return }
        do {
            async let g = client.groups()
            async let u = client.userDirectory()
            groups = try await g
            users = try await u
        } catch {
            self.error = describe(error)
        }
    }

    private func create() async {
        let name = newName.trimmingCharacters(in: .whitespaces)
        newName = ""
        guard let client = model.client, !name.isEmpty else { return }
        do {
            try await client.createGroup(name: name)
            await load()
        } catch let NubiloError.http(code, _) where code == 409 {
            error = "There is already a group called “\(name)”."
        } catch {
            self.error = describe(error)
        }
    }

    private func delete(_ group: UserGroup) async {
        guard let client = model.client else { return }
        do {
            try await client.deleteGroup(group.id)
            await load()
        } catch {
            self.error = describe(error)
        }
    }
}

/// Picks the members of one group.
private struct GroupDetailView: View {
    let group: UserGroup
    let users: [User]
    let changed: () async -> Void

    @Environment(AppModel.self) private var model
    @State private var members: Set<String> = []
    @State private var error: String?

    var body: some View {
        Form {
            Section("Members") {
                ForEach(users) { user in
                    Toggle(isOn: binding(for: user)) {
                        VStack(alignment: .leading) {
                            Text(user.displayName)
                            Text("@\(user.username)").font(.caption).foregroundStyle(.secondary)
                        }
                    }
                }
            }
        }
        .formStyle(.grouped)
        .navigationTitle(group.name)
        .onAppear { members = Set(group.members) }
        .errorAlert($error)
    }

    private func binding(for user: User) -> Binding<Bool> {
        Binding(
            get: { members.contains(user.id) },
            set: { on in
                let previous = members
                if on { members.insert(user.id) } else { members.remove(user.id) }
                let wanted = members
                Task {
                    guard let client = model.client else { return }
                    do {
                        try await client.updateGroup(group.id, members: Array(wanted))
                        await changed()
                    } catch {
                        members = previous
                        self.error = describe(error)
                    }
                }
            }
        )
    }
}
