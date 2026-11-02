import SwiftUI
import NubiloKit

/// Shares an album with other users on the server, read-only or read-write.
struct ShareAlbumView: View {
    let album: Album

    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @State private var users: [User] = []
    @State private var groups: [UserGroup] = []
    /// username → "ro" | "rw"
    @State private var access: [String: String] = [:]
    /// group id → "ro" | "rw"
    @State private var groupAccess: [String: String] = [:]
    @State private var loading = true
    @State private var error: String?

    var body: some View {
        NavigationStack {
            Form {
                if !groups.isEmpty {
                    Section {
                        ForEach(groups) { group in
                            Picker(selection: binding(for: group)) {
                                Text("Not shared").tag("")
                                Text("Can view").tag("ro")
                                Text("Can add photos").tag("rw")
                            } label: {
                                VStack(alignment: .leading) {
                                    Text(group.name)
                                    Text("\(group.members.count) member\(group.members.count == 1 ? "" : "s")")
                                        .font(.caption).foregroundStyle(.secondary)
                                }
                            }
                        }
                    } header: {
                        Text("Groups")
                    } footer: {
                        Text("Everyone in the group gets this access, including people added later.")
                    }
                }
                Section {
                    ForEach(users) { user in
                        Picker(selection: binding(for: user)) {
                            Text("Not shared").tag("")
                            Text("Can view").tag("ro")
                            Text("Can add photos").tag("rw")
                        } label: {
                            VStack(alignment: .leading) {
                                Text(user.displayName)
                                Text("@\(user.username)").font(.caption).foregroundStyle(.secondary)
                            }
                        }
                    }
                } footer: {
                    Text("People you share with see the album under Shared with Me.")
                }
            }
            .formStyle(.grouped)
            .overlay {
                if loading {
                    ProgressView()
                } else if users.isEmpty && groups.isEmpty {
                    ContentUnavailableView("Nobody to share with", systemImage: "person.2",
                                           description: Text("Invite people under People first."))
                }
            }
            .navigationTitle("Share “\(album.name)”")
            .inlineNavigationTitle()
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") { dismiss() }
                }
            }
            .task { await load() }
            .errorAlert($error)
        }
        .macSheetFrame()
    }

    private func binding(for user: User) -> Binding<String> {
        Binding(
            get: { access[user.username] ?? "" },
            set: { level in Task { await update(user, to: level) } }
        )
    }

    private func binding(for group: UserGroup) -> Binding<String> {
        Binding(
            get: { groupAccess[group.id] ?? "" },
            set: { level in Task { await update(group, to: level) } }
        )
    }

    private func update(_ group: UserGroup, to level: String) async {
        guard let client = model.client else { return }
        let previous = groupAccess[group.id]
        groupAccess[group.id] = level.isEmpty ? nil : level
        do {
            let shares = level.isEmpty
                ? try await client.unshare(collection: album.id, groupID: group.id)
                : try await client.share(collection: album.id, groupID: group.id, access: level)
            apply(shares)
        } catch {
            groupAccess[group.id] = previous
            self.error = describe(error)
        }
    }

    private func load() async {
        guard let client = model.client else { return }
        defer { loading = false }
        do {
            async let directory = client.userDirectory()
            async let shares = client.shares(collection: album.id)
            async let allGroups = client.groups()
            let me = model.me?.user.id
            users = try await directory.filter { $0.id != me }
            groups = try await allGroups
            apply(try await shares)
        } catch {
            self.error = describe(error)
        }
    }

    private func update(_ user: User, to level: String) async {
        guard let client = model.client else { return }
        let previous = access[user.username]
        access[user.username] = level.isEmpty ? nil : level
        do {
            let shares = level.isEmpty
                ? try await client.unshare(collection: album.id, username: user.username)
                : try await client.share(collection: album.id, username: user.username, access: level)
            apply(shares)
        } catch {
            access[user.username] = previous
            self.error = describe(error)
        }
    }

    private func apply(_ shares: [Share]) {
        var out: [String: String] = [:]
        var byGroup: [String: String] = [:]
        for share in shares {
            if share.principalType == "group" {
                byGroup[share.principalId] = share.access
            } else if let name = share.username {
                out[name] = share.access
            }
        }
        access = out
        groupAccess = byGroup
    }
}
