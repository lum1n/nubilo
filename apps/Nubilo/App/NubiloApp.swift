import SwiftUI
import NubiloKit
import NubiloUI

@main
struct NubiloApp: App {
    #if os(iOS)
    @UIApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate
    #endif
    @Environment(\.scenePhase) private var scenePhase
    @State private var model = AppModel()
    #if os(macOS)
    @State private var server = ServerController()

    init() {
        // Photo backup runs in the app on the Mac (no background tasks).
        MainActor.assumeIsolated {
            MacBackupLoop.shared.start()
            MacCalendarMirror.shared.start()
        }
    }
    #endif

    var body: some Scene {
        WindowGroup(id: "main") {
            RootView()
                .environment(model)
                .onOpenURL { model.handle(url: $0) }
        }
        .onChange(of: scenePhase) { _, phase in
            switch phase {
            case .background:
                #if os(iOS)
                BackupScheduler.schedule()
                #endif
            case .active:
                Task { await model.refreshMe() }
            default:
                break
            }
        }

        #if os(macOS)
        Window("Set Up Nubilo Server", id: "setup") {
            SetupWizardView()
                .environment(server)
        }
        .windowResizability(.contentSize)

        MenuBarExtra("Nubilo", systemImage: "cloud") {
            MenuBarView()
                .environment(server)
        }
        .menuBarExtraStyle(.window)
        #endif
    }
}
