#if canImport(Photos) && canImport(SwiftData)
import SwiftUI
import Photos
import NubiloKit

/// Camera-roll backup: permission, settings, progress and the last run.
struct BackupStatusView: View {
    @State private var settings = BackupSettings.load()
    @State private var counts = BackupCounts()
    @State private var lastRun: BackupRun?
    @State private var running = false
    @State private var authorization = PHPhotoLibrary.authorizationStatus(for: .readWrite)

    private var runtime: BackupRuntime { .shared }
    private var hasAccess: Bool { authorization == .authorized || authorization == .limited }

    var body: some View {
        Form {
            if let problem = runtime.storeError {
                Section {
                    Label("The backup database could not be opened: \(problem)", systemImage: "exclamationmark.triangle")
                }
            }
            accessSection
            Section {
                Toggle(Self.enableTitle, isOn: $settings.enabled)
                Toggle("Include videos", isOn: $settings.includeVideos)
                #if os(iOS)
                Toggle("Only while charging", isOn: $settings.requiresPower)
                Toggle("Use mobile data", isOn: $settings.allowCellular)
                #endif
            } footer: {
                Text(Self.scheduleNote)
            }
            Section("Status") {
                LabeledContent("Backed up", value: "\(counts.done)")
                LabeledContent("Waiting", value: "\(counts.pending)")
                LabeledContent("Uploading", value: "\(counts.uploading)")
                if counts.failed > 0 {
                    LabeledContent("Failed", value: "\(counts.failed)")
                }
                Button {
                    Task { await backUpNow() }
                } label: {
                    HStack {
                        Text(running ? "Backing up…" : "Back Up Now")
                        if running { Spacer(); ProgressView() }
                    }
                }
                .disabled(running || !hasAccess || runtime.engine == nil)
            }
            if let run = lastRun {
                Section("Last run") {
                    LabeledContent("Finished", value: (run.finished ?? run.started).relative)
                    LabeledContent("Added to library", value: "\(run.committed)")
                    LabeledContent("Uploads queued", value: "\(run.queued)")
                    ForEach(Array(run.errors.prefix(10).enumerated()), id: \.offset) { _, message in
                        Label(message, systemImage: "exclamationmark.circle")
                            .font(.footnote)
                            .foregroundStyle(.red)
                    }
                    if run.errors.count > 10 {
                        Text("and \(run.errors.count - 10) more").font(.footnote).foregroundStyle(.secondary)
                    }
                }
            }
        }
        .formStyle(.grouped)
        .navigationTitle("Photo Backup")
        .onChange(of: settings) { _, new in
            new.save()
            #if os(iOS)
            BackupScheduler.schedule()
            #else
            if new.enabled { MacBackupLoop.shared.nudge() }
            #endif
        }
        .task {
            // Uploads finish in the background session; poll while visible.
            while !Task.isCancelled {
                await refresh()
                try? await Task.sleep(for: .seconds(3))
            }
        }
    }

    @ViewBuilder
    private var accessSection: some View {
        switch authorization {
        case .notDetermined:
            Section {
                Button("Allow Access to Photos") {
                    Task {
                        authorization = await PHPhotoLibrary.requestAuthorization(for: .readWrite)
                        #if os(macOS)
                        MacBackupLoop.shared.nudge()
                        #endif
                    }
                }
            } footer: {
                Text("Nubilo reads your photos to copy them to your server. Nothing is deleted from this device.")
            }
        case .limited:
            Section {
                Label("Only selected photos are backed up. Choose Full Access in \(Self.photosSettingsPath) to back up everything.", systemImage: "photo.badge.exclamationmark")
            }
        case .denied, .restricted:
            Section {
                Label("Photos access is off. Turn it on in \(Self.photosSettingsPath).", systemImage: "lock")
            }
        default:
            EmptyView()
        }
    }

    #if os(iOS)
    private static let enableTitle = "Back up this device's photos"
    private static let photosSettingsPath = "Settings → Nubilo → Photos"
    private static let scheduleNote = "iOS decides when background backup runs, usually overnight while charging. Opening this screen and tapping Back Up Now is the quickest way to catch up."
    #else
    private static let enableTitle = "Back up the Photos library on this Mac"
    private static let photosSettingsPath = "System Settings → Privacy & Security → Photos → Nubilo"
    private static let scheduleNote = "While Nubilo runs, new photos back up about a minute after they reach Photos, and the library is checked every 30 minutes. Turn on Open at Login in Settings so it keeps running."
    #endif

    private func refresh() async {
        guard let engine = runtime.engine else { return }
        counts = await engine.counts()
        if let run = await engine.lastRun { lastRun = run }
    }

    private func backUpNow() async {
        guard let engine = await runtime.readyEngine() else { return }
        running = true
        defer { running = false }
        lastRun = await engine.runOnce(settings: settings, deadline: Date().addingTimeInterval(10 * 60))
        await refresh()
    }
}
#endif
