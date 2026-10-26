#if canImport(Photos) && canImport(SwiftData)
import Foundation
import SwiftData
import NubiloKit

/// Process-wide photo backup plumbing. It must exist before any UI because
/// iOS launches the app in the background for BGProcessingTasks and for
/// finished background uploads.
public final class BackupRuntime: Sendable {
    public static let shared = BackupRuntime()

    /// nil when the SwiftData store could not be opened (see `storeError`).
    public let engine: PhotoBackupEngine?
    public let uploader: BackgroundUploader
    public let storeError: String?
    private let configured = Locked(false)

    init() {
        uploader = BackgroundUploader()
        do {
            let container = try PhotoBackupEngine.makeContainer()
            engine = PhotoBackupEngine(modelContainer: container)
            storeError = nil
        } catch {
            engine = nil
            storeError = describe(error)
        }
        uploader.onFinish = { [weak self] localIdentifier, part, ok, error in
            Task {
                await self?.readyEngine()?.uploadFinished(localIdentifier: localIdentifier, part: part, ok: ok, error: error)
            }
        }
    }

    /// Points the engine at `client`. Called by the UI after pairing and launch.
    public func connect(client: NubiloClient, tlsPin: String?) async {
        uploader.tlsPin = tlsPin
        await engine?.configure(client: client, uploader: uploader)
        configured.set(true)
    }

    /// The engine, configured from the stored pairing if the UI has not done it
    /// yet (background launches). nil when the device is not paired.
    public func readyEngine() async -> PhotoBackupEngine? {
        guard let engine else { return nil }
        if configured.current { return engine }
        guard let server = ServerStore().load(), let client = try? ClientFactory.make(for: server) else { return nil }
        await connect(client: client, tlsPin: server.tlsPin)
        return engine
    }

    /// Forgets the backup state of the old server (sign out).
    public func disconnect() async {
        var settings = BackupSettings.load()
        settings.enabled = false
        settings.save()
        uploader.cancelAll()
        await engine?.reset()
        configured.set(false)
    }
}
#endif
