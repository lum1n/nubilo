#if os(iOS)
import UIKit
import NubiloKit
import NubiloUI

/// UIKit hooks SwiftUI has no equivalent for: background task registration
/// (must happen before launch finishes) and background URLSession wake-ups.
final class AppDelegate: NSObject, UIApplicationDelegate {
    func application(
        _ application: UIApplication,
        didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]? = nil
    ) -> Bool {
        BackupScheduler.register {
            guard BackupSettings.load().enabled else { return nil }
            return await BackupRuntime.shared.readyEngine()
        }
        // Reattach to uploads that finished or are still running from an earlier launch.
        BackupRuntime.shared.uploader.resume()
        return true
    }

    func application(
        _ application: UIApplication,
        handleEventsForBackgroundURLSession identifier: String,
        completionHandler: @escaping () -> Void
    ) {
        guard identifier == BackgroundUploader.sessionIdentifier else {
            completionHandler()
            return
        }
        // Called once the session has delivered every pending event
        // (BackgroundUploader.urlSessionDidFinishEvents).
        BackupRuntime.shared.uploader.backgroundCompletion = completionHandler
        BackupRuntime.shared.uploader.resume()
    }
}
#endif
