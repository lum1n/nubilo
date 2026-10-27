#if os(iOS) && canImport(FileProvider)
import FileProvider
import NubiloKit

/// Nubilo as a location in the Files app (FilesExtension/). The app shares
/// its pairing with the extension and registers the location; the extension
/// does the rest.
enum FilesIntegration {
    static let domain = NSFileProviderDomain(identifier: NSFileProviderDomainIdentifier("nubilo"), displayName: "Nubilo")

    /// Shares the current pairing and registers the location. Called when a
    /// pairing becomes active and when the app comes to the foreground, so a
    /// TLS pin or a move to the public address reaches the extension too.
    static func connect(_ server: PairedServer, keys: any KeyStore) async {
        guard let shared = SharedCredentials(), let key = try? keys.load(tag: server.keyTag) else { return }
        try? shared.save(server, key: key)
        try? await NSFileProviderManager.add(domain)
        // Ask Files to pick up changes made on other devices.
        try? await NSFileProviderManager(for: domain)?.signalEnumerator(for: .workingSet)
    }

    /// Sign out: the location and the shared credentials go.
    static func disconnect() async {
        try? await NSFileProviderManager.remove(domain)
        SharedCredentials()?.clear()
    }
}
#endif
