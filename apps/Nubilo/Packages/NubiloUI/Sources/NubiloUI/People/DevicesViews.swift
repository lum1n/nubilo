import SwiftUI
import NubiloKit

struct DeviceRow: View {
    let device: DeviceInfo
    var isThisDevice = false

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            HStack {
                Text(device.name)
                if isThisDevice { Text("this device").font(.caption).foregroundStyle(.secondary) }
                if device.revoked { Text("Revoked").font(.caption).foregroundStyle(.red) }
            }
            Text(detail).font(.caption).foregroundStyle(.secondary)
        }
    }

    private var detail: String {
        var parts = [device.role]
        if let seen = device.lastSeen, seen > 0 {
            parts.append("seen \(seen.msDate.relative)")
        } else {
            parts.append("added \(device.createdAt.msDate.relative)")
        }
        return parts.joined(separator: " · ")
    }
}

/// The signed-in user's devices.
struct MyDevicesView: View {
    @Environment(AppModel.self) private var model
    @State private var devices: [DeviceInfo] = []
    @State private var confirm: DeviceInfo?
    @State private var adding = false
    @State private var error: String?

    var body: some View {
        List(devices.filter { !$0.revoked }) { device in
            let current = device.id == model.server?.deviceID
            DeviceRow(device: device, isThisDevice: current)
                .contextMenu {
                    if !current {
                        Button("Remove Device", role: .destructive) { confirm = device }
                    }
                }
                #if os(iOS)
                .swipeActions {
                    if !current {
                        Button("Remove", role: .destructive) { confirm = device }
                    }
                }
                #endif
        }
        .navigationTitle("My Devices")
        .toolbar {
            ToolbarItem(placement: .primaryAction) {
                Button {
                    adding = true
                } label: {
                    Label("Add a Device", systemImage: "plus")
                }
            }
        }
        .sheet(isPresented: $adding, onDismiss: { Task { await load() } }) {
            AddDeviceView()
        }
        .confirmationDialog("Remove \(confirm?.name ?? "device")?", isPresented: Binding(get: { confirm != nil }, set: { if !$0 { confirm = nil } }), presenting: confirm) { device in
            Button("Remove", role: .destructive) { Task { await revoke(device) } }
        } message: { _ in
            Text("It stops syncing immediately and has to be set up again with a new invite.")
        }
        .refreshable { await load() }
        .task { await load() }
        .errorAlert($error)
    }

    private func load() async {
        guard let client = model.client else { return }
        do {
            devices = try await client.devices()
        } catch {
            self.error = describe(error)
        }
    }

    private func revoke(_ device: DeviceInfo) async {
        guard let client = model.client else { return }
        do {
            try await client.revokeDevice(device.id)
            await load()
        } catch {
            self.error = describe(error)
        }
    }
}

/// Admin: every device on the server grouped by owner.
struct AllDevicesView: View {
    @Environment(AppModel.self) private var model
    @State private var devices: [DeviceInfo] = []
    @State private var names: [String: String] = [:]
    @State private var showRevoked = false
    @State private var confirm: DeviceInfo?
    @State private var error: String?

    var body: some View {
        List {
            ForEach(groups) { group in
                Section(names[group.owner] ?? (group.owner.isEmpty ? "Server" : group.owner)) {
                    ForEach(group.devices) { device in
                        DeviceRow(device: device, isThisDevice: device.id == model.server?.deviceID)
                            .contextMenu {
                                if !device.revoked && device.id != model.server?.deviceID {
                                    Button("Revoke", role: .destructive) { confirm = device }
                                }
                            }
                            #if os(iOS)
                            .swipeActions {
                                if !device.revoked && device.id != model.server?.deviceID {
                                    Button("Revoke", role: .destructive) { confirm = device }
                                }
                            }
                            #endif
                    }
                }
            }
        }
        .navigationTitle("All Devices")
        .toolbar {
            ToolbarItem(placement: .primaryAction) {
                Toggle("Show Revoked", isOn: $showRevoked)
            }
        }
        .confirmationDialog("Revoke \(confirm?.name ?? "device")?", isPresented: Binding(get: { confirm != nil }, set: { if !$0 { confirm = nil } }), presenting: confirm) { device in
            Button("Revoke", role: .destructive) { Task { await revoke(device) } }
        } message: { _ in
            Text("The device loses access immediately.")
        }
        .refreshable { await load() }
        .task { await load() }
        .errorAlert($error)
    }

    private struct OwnerGroup: Identifiable {
        let owner: String
        let devices: [DeviceInfo]
        var id: String { owner }
    }

    private var groups: [OwnerGroup] {
        let visible = devices.filter { showRevoked || !$0.revoked }
        return Dictionary(grouping: visible) { $0.userId ?? "" }
            .map { OwnerGroup(owner: $0.key, devices: $0.value.sorted { $0.name < $1.name }) }
            .sorted { (names[$0.owner] ?? $0.owner) < (names[$1.owner] ?? $1.owner) }
    }

    private func load() async {
        guard let client = model.client else { return }
        do {
            async let all = client.devices(all: true)
            async let users = client.adminUsers()
            devices = try await all
            names = Dictionary(try await users.map { ($0.id, $0.displayName) }, uniquingKeysWith: { a, _ in a })
        } catch {
            self.error = describe(error)
        }
    }

    private func revoke(_ device: DeviceInfo) async {
        guard let client = model.client else { return }
        do {
            try await client.adminRevokeDevice(device.id)
            await load()
        } catch {
            self.error = describe(error)
        }
    }
}

/// Adds another device to the signed-in user's account: a single-use invite
/// bound to them, shown as a QR code and a `nubilo://` link (AirDrop it to a Mac).
struct AddDeviceView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @State private var invite: NewInvite?
    @State private var error: String?

    var body: some View {
        NavigationStack {
            Form {
                if let invite {
                    Section {
                        QRCodeImage(text: invite.setupUrl)
                            .frame(maxWidth: 260, maxHeight: 260)
                            .padding()
                            .background(.white, in: RoundedRectangle(cornerRadius: 12))
                            .frame(maxWidth: .infinity)
                    } footer: {
                        Text("Scan with the Nubilo app on your other device, or send it the link. It signs in as you, works once and expires \(invite.expiresAt.msDate.relative).")
                    }
                    Section {
                        if let url = URL(string: invite.setupUrl) {
                            ShareLink(item: url) {
                                Label("Send Link", systemImage: "square.and.arrow.up")
                            }
                        }
                        CopyableRow(title: "Link", value: invite.setupUrl)
                    }
                } else {
                    ProgressView().frame(maxWidth: .infinity)
                }
            }
            .formStyle(.grouped)
            .navigationTitle("Add a Device")
            .inlineNavigationTitle()
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") { dismiss() }
                }
            }
            .task { await create() }
            .errorAlert($error)
        }
        .macSheetFrame()
    }

    private func create() async {
        guard invite == nil, let client = model.client else { return }
        do {
            invite = try await client.createDeviceInvite()
        } catch {
            self.error = describe(error)
        }
    }
}
