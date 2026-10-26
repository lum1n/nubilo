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
                Toggle("Back up this device's photos", isOn: $settings.enabled)
                Toggle("Include videos", isOn: $settings.includeVideos)
                Toggle("Only while charging", isOn: $settings.requiresPower)
                Toggle("Use mobile data", isOn: $settings.allowCellular)
            } footer: {
                Text("iOS decides when background backup runs, usually overnight while charging. Opening this screen and tapping Back Up Now is the quickest way to catch up.")
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
                    Task { authorization = await PHPhotoLibrary.requestAuthorization(for: .readWrite) }
                }
            } footer: {
                Text("Nubilo reads your photos to copy them to your server. Nothing is deleted from this device.")
            }
        case .limited:
            Section {
                Label("Only selected photos are backed up. Choose Full Access in Settings → Nubilo → Photos to back up everything.", systemImage: "photo.badge.exclamationmark")
            }
        case .denied, .restricted:
            Section {
                Label("Photos access is off. Turn it on in Settings → Nubilo → Photos.", systemImage: "lock")
            }
        default:
            EmptyView()
        }
    }

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
