#if os(iOS) && canImport(FileProvider)
import FileProvider
import NubiloKit

/// Nubilo as a location in the Files app (FilesExtension/). The app shares
/// its pairing with the extension and registers the location; the extension
/// does the rest.
@MainActor
enum FilesIntegration {
    static let domain = NSFileProviderDomain(identifier: NSFileProviderDomainIdentifier("nubilo"), displayName: "Nubilo")

    /// Why the last attempt to set up the location failed, or nil.
    static private(set) var problem: String?

    /// Whether this build contains the Files extension.
    static var extensionInstalled: Bool {
        guard let plugins = Bundle.main.builtInPlugInsURL else { return false }
        return FileManager.default.fileExists(atPath: plugins.appendingPathComponent("NubiloFiles.appex").path)
    }

    /// Whether the location is registered with the system.
    static func isRegistered() async -> Bool {
        guard let domains = try? await NSFileProviderManager.domains() else { return false }
        return domains.contains { $0.identifier == domain.identifier }
    }

    /// Shares the current pairing and registers the location. Called when a
    /// pairing becomes active and when the app comes to the foreground, so a
    /// TLS pin or a move to the public address reaches the extension too.
    /// Returns the problem, if any (also kept in `problem`).
    @discardableResult
    static func connect(_ server: PairedServer, keys: any KeyStore) async -> String? {
        guard extensionInstalled else {
            return fail("This build of the app does not contain the Files extension (NubiloFiles.appex).")
        }
        guard let shared = SharedCredentials() else {
            return fail("The app has no shared Keychain group (NubiloKeychainGroup is missing or unexpanded in Info.plist; is a signing team set?).")
        }
        let key: Ed25519Key
        do {
            guard let k = try keys.load(tag: server.keyTag) else { return fail("This device's key is missing from the Keychain.") }
            key = k
        } catch {
            return fail("This device's key could not be read: \(describe(error))")
        }
        do {
            try shared.save(server, key: key)
        } catch {
            return fail("The sign-in could not be shared with the Files extension (Keychain group \(shared.accessGroup)): \(describe(error))")
        }
        do {
            try await NSFileProviderManager.add(domain)
        } catch {
            return fail("Files did not accept the Nubilo location: \(describe(error)) [\((error as NSError).domain) \((error as NSError).code)]")
        }
        // Ask Files to pick up changes made on other devices.
        try? await NSFileProviderManager(for: domain)?.signalEnumerator(for: .workingSet)
        problem = nil
        return nil
    }

    /// Sign out: the location and the shared credentials go.
    static func disconnect() async {
        try? await NSFileProviderManager.remove(domain)
        SharedCredentials()?.clear()
        problem = nil
    }

    private static func fail(_ message: String) -> String {
        problem = message
        return message
    }
}
#endif
