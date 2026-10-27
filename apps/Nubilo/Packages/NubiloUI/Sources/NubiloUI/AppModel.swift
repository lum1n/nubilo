import Foundation
import Observation
import NubiloKit

/// App-wide state: the paired server, its client and the signed-in user.
@MainActor
@Observable
public final class AppModel {
    public private(set) var server: PairedServer?
    public private(set) var client: NubiloClient?
    public private(set) var me: Me?
    /// A setup link that arrived through a `nubilo://` URL; onboarding picks it up.
    public var pendingLink: SetupLink?
    /// An app-level problem shown as an alert (bad deep link, revoked device).
    public var alert: String?

    private let store: ServerStore
    private let keys: any KeyStore

    public init(store: ServerStore = ServerStore(), keys: any KeyStore = KeychainKeyStore()) {
        self.store = store
        self.keys = keys
        if let saved = store.load() {
            activate(saved)
        }
    }

    public var isPaired: Bool { client != nil }
    public var isAdmin: Bool { me?.user.isAdmin ?? false }

    /// Stores a freshly paired server and switches to the main UI.
    public func completePairing(_ paired: PairedServer) throws {
        try store.save(paired)
        activate(paired)
    }

    private func activate(_ paired: PairedServer) {
        do {
            let client = try ClientFactory.make(for: paired, keys: keys, store: store)
            server = paired
            self.client = client
            Task {
                #if canImport(Photos) && canImport(SwiftData)
                await BackupRuntime.shared.connect(client: client, tlsPin: paired.tlsPin)
                #endif
                await refreshMe()
            }
        } catch {
            // The key is gone (restored backup, Keychain reset): pair again.
            store.clear()
            server = nil
            client = nil
            alert = "This device's key is missing, so it has to be set up again. (\(describe(error)))"
        }
    }

    public func refreshMe() async {
        guard let client else { return }
        do {
            let fresh = try await client.me()
            me = fresh
            await follow(publicURL: fresh.publicUrl)
        } catch let NubiloError.http(status, _) where status == 401 {
            alert = "The server no longer accepts this device. It may have been removed; sign out and set it up again."
        } catch {
            // Offline or server down: keep the last known state.
        }
    }

    /// Moves to the public address the server advertises (paired on the LAN,
    /// then the server got a domain), once a signed request through it answers
    /// for this device. The probe trusts only system-trusted certificates, so
    /// nothing is pinned on the way.
    private func follow(publicURL: String?) async {
        guard let current = server, let moved = current.movingTo(publicURL: publicURL),
              let key = try? keys.load(tag: current.keyTag) else { return }
        let probe = NubiloClient(baseURL: moved.baseURL,
                                 signer: RequestSigner(deviceID: current.deviceID, key: key),
                                 transport: URLSessionTransport())
        guard let check = try? await probe.me(), check.device.id == current.deviceID,
              server == current else { return }
        try? store.save(moved)
        activate(moved)
    }

    /// Handles `nubilo://setup?…` links (and https join links passed in).
    public func handle(url: URL) {
        if let server {
            alert = "This device is already set up with \(server.baseURL.host() ?? "a server"). Sign out first to use another invite."
            return
        }
        do {
            pendingLink = try SetupLink(parsing: url.absoluteString)
        } catch {
            alert = describe(error)
        }
    }

    /// Removes this device from the server (best effort), then deletes the key
    /// and the stored pairing.
    public func signOut() async {
        if let client, let id = me?.device.id ?? server?.deviceID {
            try? await client.revokeDevice(id)
        }
        #if canImport(Photos) && canImport(SwiftData)
        await BackupRuntime.shared.disconnect()
        #endif
        if let server { try? keys.delete(tag: server.keyTag) }
        store.clear()
        server = nil
        client = nil
        me = nil
    }
}
