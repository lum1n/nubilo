import SwiftUI
import NubiloKit

/// Admin: everyone on the server with storage and device counts.
struct MembersView: View {
    @Environment(AppModel.self) private var model
    @State private var users: [AdminUser] = []
    @State private var error: String?

    var body: some View {
        List(users) { user in
            MemberRow(user: user, isMe: user.id == model.me?.user.id)
                .contextMenu { actions(for: user) }
                #if os(iOS)
                .swipeActions { actions(for: user) }
                #endif
        }
        .navigationTitle("Members")
        .refreshable { await load() }
        .task { await load() }
        .errorAlert($error)
    }

    @ViewBuilder
    private func actions(for user: AdminUser) -> some View {
        if user.id != model.me?.user.id {
            if user.status == "disabled" {
                Button("Enable") { Task { await update(user, status: "active") } }
            } else {
                Button("Disable", role: .destructive) { Task { await update(user, status: "disabled") } }
            }
        }
        if user.role == "admin" {
            Button("Make Member") { Task { await update(user, role: "member") } }
        } else {
            Button("Make Admin") { Task { await update(user, role: "admin") } }
        }
    }

    private func load() async {
        guard let client = model.client else { return }
        do {
            users = try await client.adminUsers()
        } catch {
            self.error = describe(error)
        }
    }

    private func update(_ user: AdminUser, role: String? = nil, status: String? = nil) async {
        guard let client = model.client else { return }
        do {
            _ = try await client.setUser(user.id, role: role, status: status)
            await load()
            if user.id == model.me?.user.id { await model.refreshMe() }
        } catch let NubiloError.http(code, _) where code == 409 {
            error = "The server needs at least one active admin."
        } catch {
            self.error = describe(error)
        }
    }
}

struct MemberRow: View {
    let user: AdminUser
    let isMe: Bool

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack {
                Text(user.displayName).font(.headline)
                if isMe { Text("you").font(.caption).foregroundStyle(.secondary) }
                Spacer()
                if user.role == "admin" {
                    Text("Admin").font(.caption).padding(.horizontal, 6).padding(.vertical, 2)
                        .background(Color.accentColor.opacity(0.15), in: Capsule())
                }
                if user.status == "disabled" {
                    Text("Disabled").font(.caption).foregroundStyle(.red)
                }
            }
            Text("@\(user.username) · \(user.devices) device\(user.devices == 1 ? "" : "s") · \(user.bytes.byteString)")
                .font(.caption)
                .foregroundStyle(.secondary)
        }
        .padding(.vertical, 2)
    }
}
