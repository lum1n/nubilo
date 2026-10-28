import Foundation

/// Persists the device's Ed25519 private key.
public protocol KeyStore: Sendable {
    func load(tag: String) throws -> Ed25519Key?
    func save(_ key: Ed25519Key, tag: String) throws
    func delete(tag: String) throws
}

extension KeyStore {
    /// Returns the stored key or creates and stores a new one.
    public func loadOrCreate(tag: String) throws -> Ed25519Key {
        if let k = try load(tag: tag) { return k }
        let k = Ed25519Key()
        try save(k, tag: tag)
        return k
    }
}

/// Keeps keys in memory (tests, previews).
public final class MemoryKeyStore: KeyStore, @unchecked Sendable {
    private var keys: [String: Data] = [:]
    private let lock = NSLock()

    public init() {}

    public func load(tag: String) throws -> Ed25519Key? {
        lock.lock(); defer { lock.unlock() }
        return try keys[tag].map { try Ed25519Key(rawRepresentation: $0) }
    }

    public func save(_ key: Ed25519Key, tag: String) throws {
        lock.lock(); defer { lock.unlock() }
        keys[tag] = key.rawRepresentation
    }

    public func delete(tag: String) throws {
        lock.lock(); defer { lock.unlock() }
        keys[tag] = nil
    }
}

#if canImport(Security)
import Security

/// Stores the raw Ed25519 seed as a generic password item that never syncs
/// and is readable after first unlock (background tasks), on this device only.
public struct KeychainKeyStore: KeyStore {
    public let service: String
    public let accessGroup: String?

    public init(service: String = "dev.nubilo.device-key", accessGroup: String? = nil) {
        self.service = service
        self.accessGroup = accessGroup
    }

    private func query(_ tag: String) -> [String: Any] {
        var q: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: tag,
        ]
        if let accessGroup {
            q[kSecAttrAccessGroup as String] = accessGroup
            #if os(macOS)
            // Access groups only exist in the data protection keychain on
            // macOS; the legacy file keychain ignores them.
            q[kSecUseDataProtectionKeychain as String] = true
            #endif
        }
        return q
    }

    public func load(tag: String) throws -> Ed25519Key? {
        var q = query(tag)
        q[kSecReturnData as String] = true
        q[kSecMatchLimit as String] = kSecMatchLimitOne
        var out: CFTypeRef?
        let status = SecItemCopyMatching(q as CFDictionary, &out)
        if status == errSecItemNotFound { return nil }
        guard status == errSecSuccess, let data = out as? Data else { throw KeychainError(status: status) }
        return try Ed25519Key(rawRepresentation: data)
    }

    public func save(_ key: Ed25519Key, tag: String) throws {
        try? delete(tag: tag)
        var q = query(tag)
        q[kSecValueData as String] = key.rawRepresentation
        q[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
        q[kSecAttrSynchronizable as String] = false
        let status = SecItemAdd(q as CFDictionary, nil)
        guard status == errSecSuccess else { throw KeychainError(status: status) }
    }

    public func delete(tag: String) throws {
        let status = SecItemDelete(query(tag) as CFDictionary)
        guard status == errSecSuccess || status == errSecItemNotFound else { throw KeychainError(status: status) }
    }
}

public struct KeychainError: Error, LocalizedError {
    public let status: OSStatus
    public var errorDescription: String? {
        (SecCopyErrorMessageString(status, nil) as String?) ?? "Keychain error \(status)"
    }
}
#endif
