import SwiftUI
import NubiloKit

/// Admin: create an invite shown as a QR code and a share link, and revoke open invites.
struct InviteView: View {
    @Environment(AppModel.self) private var model
    @State private var role = "member"
    @State private var hours = 72
    @State private var invite: NewInvite?
    @State private var open: [Invite] = []
    @State private var working = false
    @State private var error: String?

    var body: some View {
        Form {
            if let invite {
                created(invite)
            } else {
                Section {
                    Picker("Role", selection: $role) {
                        Text("Member").tag("member")
                        Text("Admin").tag("admin")
                    }
                    Picker("Valid for", selection: $hours) {
                        Text("1 day").tag(24)
                        Text("3 days").tag(72)
                        Text("1 week").tag(168)
                    }
                    Button {
                        Task { await create() }
                    } label: {
                        HStack {
                            Text("Create Invite")
                            if working { Spacer(); ProgressView() }
                        }
                    }
                    .disabled(working)
                } footer: {
                    Text("The invite creates a new account and works once. The person picks their own username when they join.")
                }
            }
            if !open.isEmpty {
                Section("Open invites") {
                    ForEach(open) { item in
                        LabeledContent {
                            Button("Revoke", role: .destructive) { Task { await revoke(item) } }
                                .buttonStyle(.borderless)
                        } label: {
                            Text(item.role.capitalized)
                            Text("Expires \(item.expiresAt.msDate.relative)")
                        }
                    }
                }
            }
        }
        .formStyle(.grouped)
        .navigationTitle("Invite")
        .task { await loadOpen() }
        .errorAlert($error)
    }

    @ViewBuilder
    private func created(_ invite: NewInvite) -> some View {
        Section {
            QRCodeImage(text: invite.setupUrl)
                .frame(maxWidth: 260, maxHeight: 260)
                .padding()
                .background(.white, in: RoundedRectangle(cornerRadius: 12))
                .frame(maxWidth: .infinity)
        } footer: {
            Text("Scan with the Nubilo app on the new device. Expires \(invite.expiresAt.msDate.relative).")
        }
        Section {
            if let url = URL(string: invite.url) {
                ShareLink(item: url, subject: Text("Join our Nubilo"), message: Text("Open this link on your iPhone or Mac to join.")) {
                    Label("Share Invite Link", systemImage: "square.and.arrow.up")
                }
            }
            CopyableRow(title: "Link", value: invite.url)
            Button("Create Another") { self.invite = nil }
        }
    }

    private func create() async {
        guard let client = model.client else { return }
        working = true
        defer { working = false }
        do {
            invite = try await client.createInvite(.init(role: role, ttlHours: hours, maxUses: 1))
            await loadOpen()
        } catch {
            self.error = describe(error)
        }
    }

    private func loadOpen() async {
        guard let client = model.client else { return }
        open = (try? await client.invites()) ?? []
    }

    private func revoke(_ item: Invite) async {
        guard let client = model.client else { return }
        do {
            try await client.revokeInvite(item.id)
            if invite?.id == item.id { invite = nil }
            await loadOpen()
        } catch {
            self.error = describe(error)
        }
    }
}
