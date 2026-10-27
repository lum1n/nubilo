#if canImport(Security)
import Foundation
import Security

/// The paired server and device key, shared with the app's extensions (the
/// Files app integration) through a Keychain access group. The app writes
/// them whenever it activates a pairing; extensions only read.
public struct SharedCredentials: Sendable {
    public let accessGroup: String

    private static let service = "dev.nubilo.pairing"
    private static let account = "server"

    /// The group from the `NubiloKeychainGroup` Info.plist key
    /// ("$(AppIdentifierPrefix)dev.nubilo.shared"), or nil when unset.
    public static var bundleAccessGroup: String? {
        guard let g = Bundle.main.object(forInfoDictionaryKey: "NubiloKeychainGroup") as? String,
              !g.isEmpty, !g.contains("$(") else { return nil }
        return g
    }

    public init?(accessGroup: String? = SharedCredentials.bundleAccessGroup) {
        guard let accessGroup else { return nil }
        self.accessGroup = accessGroup
    }

    private var keys: KeychainKeyStore { KeychainKeyStore(service: "dev.nubilo.device-key.shared", accessGroup: accessGroup) }

    public func save(_ server: PairedServer, key: Ed25519Key) throws {
        try keys.save(key, tag: server.keyTag)
        let data = try JSONEncoder().encode(server)
        var q = query()
        SecItemDelete(q as CFDictionary)
        q[kSecValueData as String] = data
        q[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
        q[kSecAttrSynchronizable as String] = false
        let status = SecItemAdd(q as CFDictionary, nil)
        guard status == errSecSuccess else { throw KeychainError(status: status) }
    }

    public func load() throws -> (server: PairedServer, key: Ed25519Key)? {
        var q = query()
        q[kSecReturnData as String] = true
        q[kSecMatchLimit as String] = kSecMatchLimitOne
        var out: CFTypeRef?
        let status = SecItemCopyMatching(q as CFDictionary, &out)
        if status == errSecItemNotFound { return nil }
        guard status == errSecSuccess, let data = out as? Data else { throw KeychainError(status: status) }
        let server = try JSONDecoder().decode(PairedServer.self, from: data)
        guard let key = try keys.load(tag: server.keyTag) else { return nil }
        return (server, key)
    }

    public func clear() {
        if let current = try? load() { try? keys.delete(tag: current.server.keyTag) }
        SecItemDelete(query() as CFDictionary)
    }

    /// A client for the shared pairing, without a URL cache. It enforces the
    /// pin the app recorded; without one it trusts only system-trusted
    /// certificates (extensions never pin on first use).
    public func client() throws -> NubiloClient {
        guard let (server, key) = try load() else { throw NubiloError.notPaired }
        let transport = server.tlsPin == nil ? URLSessionTransport.uncached() : URLSessionTransport.pinned(server.tlsPin, cached: false)
        return NubiloClient(baseURL: server.baseURL, signer: RequestSigner(deviceID: server.deviceID, key: key), transport: transport)
    }

    private func query() -> [String: Any] {
        [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: Self.service,
            kSecAttrAccount as String: Self.account,
            kSecAttrAccessGroup as String: accessGroup,
        ]
    }
}
#endif
