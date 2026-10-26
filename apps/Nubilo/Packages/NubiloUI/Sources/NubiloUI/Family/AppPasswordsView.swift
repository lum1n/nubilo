import SwiftUI
import NubiloKit

/// App passwords used by Calendar/Contacts profiles and file apps.
struct AppPasswordsView: View {
    @Environment(AppModel.self) private var model
    @State private var passwords: [AppPassword] = []
    @State private var error: String?

    var body: some View {
        List {
            Section {
                ForEach(passwords) { password in
                    VStack(alignment: .leading, spacing: 2) {
                        Text(password.label.isEmpty ? "Untitled" : password.label)
                        Text(detail(password)).font(.caption).foregroundStyle(.secondary)
                    }
                    .contextMenu {
                        Button("Revoke", role: .destructive) { Task { await revoke(password) } }
                    }
                }
                .onDelete { offsets in
                    let doomed = offsets.map { passwords[$0] }
                    Task { for p in doomed { await revoke(p) } }
                }
            } footer: {
                Text("Revoking a password signs out the app or account that uses it, for example the Calendar account from a profile.")
            }
        }
        .overlay {
            if passwords.isEmpty {
                ContentUnavailableView("No app passwords", systemImage: "key")
            }
        }
        .navigationTitle("App Passwords")
        .refreshable { await load() }
        .task { await load() }
        .errorAlert($error)
    }

    private func detail(_ p: AppPassword) -> String {
        let used = p.lastUsed.map { $0 > 0 ? "used \($0.msDate.relative)" : "never used" } ?? "never used"
        return "\(p.scope) · \(used)"
    }

    private func load() async {
        guard let client = model.client else { return }
        do {
            passwords = try await client.appPasswords()
        } catch {
            self.error = describe(error)
        }
    }

    private func revoke(_ p: AppPassword) async {
        guard let client = model.client else { return }
        do {
            try await client.revokeAppPassword(p.id)
            await load()
        } catch {
            self.error = describe(error)
        }
    }
}
