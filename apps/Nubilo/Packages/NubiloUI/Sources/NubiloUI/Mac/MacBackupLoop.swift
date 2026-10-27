#if os(macOS) && canImport(Photos) && canImport(SwiftData)
import Foundation
import Photos
import NubiloKit

/// Photo backup on the Mac. macOS has no BGProcessingTask, so while the app
/// runs (it can open at login) this starts a pass at launch, a minute after
/// Photos.app reports a change, and every 30 minutes.
@MainActor
public final class MacBackupLoop: NSObject, PHPhotoLibraryChangeObserver {
    public static let shared = MacBackupLoop()

    private static let interval: TimeInterval = 30 * 60
    private static let settleDelay: TimeInterval = 60

    private var task: Task<Void, Never>?
    private var lastRun = Date.distantPast
    private var changedAt: Date?
    private var observing = false

    /// Starts the loop once; later calls do nothing.
    public func start() {
        guard task == nil else { return }
        // Reattach to uploads still running from an earlier launch.
        BackupRuntime.shared.uploader.resume()
        task = Task { [weak self] in
            while !Task.isCancelled {
                await self?.tick()
                try? await Task.sleep(for: .seconds(30))
            }
        }
    }

    /// Runs a pass on the next tick (backup turned on, access granted).
    public func nudge() {
        changedAt = .distantPast
    }

    private func tick() async {
        let now = Date()
        let changed = changedAt.map { now.timeIntervalSince($0) >= Self.settleDelay } ?? false
        guard changed || now.timeIntervalSince(lastRun) >= Self.interval else { return }
        changedAt = nil
        lastRun = now
        let settings = BackupSettings.load()
        let status = PHPhotoLibrary.authorizationStatus(for: .readWrite)
        guard settings.enabled, status == .authorized || status == .limited else { return }
        if !observing {
            // Registering only after access is granted, so it never prompts.
            PHPhotoLibrary.shared().register(self)
            observing = true
        }
        guard let engine = await BackupRuntime.shared.readyEngine() else { return }
        _ = await engine.runOnce(settings: settings, deadline: now.addingTimeInterval(Self.interval - 60))
    }

    nonisolated public func photoLibraryDidChange(_ changeInstance: PHChange) {
        Task { @MainActor in
            // Keep the first change time so a busy import still backs up within a minute or two.
            if self.changedAt == nil { self.changedAt = Date() }
        }
    }
}
#endif
