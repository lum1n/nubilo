import SwiftUI
import NubiloKit

/// Shares an album with other users on the server, read-only or read-write.
struct ShareAlbumView: View {
    let album: Album

    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @State private var users: [User] = []
    /// username → "ro" | "rw"
    @State private var access: [String: String] = [:]
    @State private var loading = true
    @State private var error: String?

    var body: some View {
        NavigationStack {
            Form {
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
                } else if users.isEmpty {
                    ContentUnavailableView("Nobody to share with", systemImage: "person.2",
                                           description: Text("Invite family members under Family first."))
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

    private func load() async {
        guard let client = model.client else { return }
        defer { loading = false }
        do {
            async let directory = client.userDirectory()
            async let shares = client.shares(collection: album.id)
            let me = model.me?.user.id
            users = try await directory.filter { $0.id != me }
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
        for share in shares {
            if let name = share.username { out[name] = share.access }
        }
        access = out
    }
}
