#if canImport(Photos) && canImport(SwiftData)
import Foundation
import Photos
import SwiftData
#if canImport(BackgroundTasks) && os(iOS)
import BackgroundTasks
#endif

// Camera-roll backup (docs/API.md "Backup loop for a phone"):
//
//   PhotoKit asset → export original to a file → stream SHA-256
//   → POST /api/v1/photos/check (batched) → background PUT /sync/v1/blob/<hash>
//   with an upload token → POST /api/v1/photos/commit.
//
// iOS gives third-party apps no always-on sync, so this is best effort: it
// runs while the app is open and in BGProcessingTask windows, and uploads
// continue in a background URLSession after the app is suspended.

/// Per-asset backup state.
@Model
public final class BackupAsset {
    @Attribute(.unique) public var localIdentifier: String
    public var status: String
    public var sha256: String?
    public var liveSHA256: String?
    public var fileName: String?
    public var takenAt: Date?
    public var modifiedAt: Date?
    public var serverID: String?
    public var lastError: String?
    public var attempts: Int
    public var updatedAt: Date

    public init(localIdentifier: String, takenAt: Date?, modifiedAt: Date?) {
        self.localIdentifier = localIdentifier
        self.status = BackupState.pending.rawValue
        self.takenAt = takenAt
        self.modifiedAt = modifiedAt
        self.attempts = 0
        self.updatedAt = Date()
    }

    public var state: BackupState { BackupState(rawValue: status) ?? .pending }
}

public enum BackupState: String, CaseIterable, Sendable {
    case pending    // known, not processed yet
    case uploading  // blob upload(s) handed to the background session
    case uploaded   // bytes on the server, commit outstanding
    case done       // in the server library
    case failed     // gave up after repeated errors (retried on the next full scan)
}

/// Settings shown on the backup screen.
public struct BackupSettings: Codable, Equatable, Sendable {
    public var enabled = false
    public var requiresPower = false
    public var allowCellular = false
    public var includeVideos = true

    public init() {}

    static let key = "dev.nubilo.backup.settings"

    public static func load() -> BackupSettings {
        guard let d = UserDefaults.standard.data(forKey: key), let s = try? JSONDecoder().decode(BackupSettings.self, from: d) else {
            return BackupSettings()
        }
        return s
    }

    public func save() {
        UserDefaults.standard.set(try? JSONEncoder().encode(self), forKey: Self.key)
    }
}

public struct BackupCounts: Equatable, Sendable {
    public var total = 0
    public var pending = 0
    public var uploading = 0
    public var done = 0
    public var failed = 0

    public init() {}
}

public struct BackupRun: Equatable, Sendable {
    public var started = Date()
    public var finished: Date?
    public var committed = 0
    public var queued = 0
    public var errors: [String] = []
    /// The server refused a photo because the account is over its storage limit.
    public var storageFull = false
}

/// Runs backup passes. All SwiftData access happens on this actor.
@ModelActor
public actor PhotoBackupEngine {
    private var client: NubiloClient?
    private var uploader: BackgroundUploader?
    private var running = false
    public private(set) var lastRun: BackupRun?

    /// The SwiftData container for BackupAsset, stored in Application Support.
    public static func makeContainer() throws -> ModelContainer {
        try ModelContainer(for: BackupAsset.self)
    }

    public func configure(client: NubiloClient, uploader: BackgroundUploader) {
        self.client = client
        self.uploader = uploader
    }

    /// Drops the client and all per-asset state (sign out, or pairing with
    /// another server whose library does not have these photos).
    public func reset() {
        client = nil
        try? modelContext.delete(model: BackupAsset.self)
        try? modelContext.save()
    }

    public func counts() -> BackupCounts {
        var c = BackupCounts()
        c.total = (try? modelContext.fetchCount(FetchDescriptor<BackupAsset>())) ?? 0
        c.pending = count(.pending)
        c.uploading = count(.uploading) + count(.uploaded)
        c.done = count(.done)
        c.failed = count(.failed)
        return c
    }

    private func count(_ state: BackupState) -> Int {
        let raw = state.rawValue
        return (try? modelContext.fetchCount(FetchDescriptor<BackupAsset>(predicate: #Predicate { $0.status == raw }))) ?? 0
    }

    /// Records camera-roll assets the store has not seen and re-queues edited ones.
    @discardableResult
    public func scan(includeVideos: Bool) throws -> Int {
        let opts = PHFetchOptions()
        opts.sortDescriptors = [NSSortDescriptor(key: "creationDate", ascending: true)]
        opts.includeAssetSourceTypes = [.typeUserLibrary, .typeiTunesSynced, .typeCloudShared]
        let types: [PHAssetMediaType] = includeVideos ? [.image, .video] : [.image]
        opts.predicate = NSPredicate(format: "mediaType IN %@", types.map(\.rawValue))
        let result = PHAsset.fetchAssets(with: opts)
        var known: [String: BackupAsset] = [:]
        for a in try modelContext.fetch(FetchDescriptor<BackupAsset>()) {
            known[a.localIdentifier] = a
        }
        var added = 0
        result.enumerateObjects { asset, _, _ in
            if let row = known[asset.localIdentifier] {
                if let m = asset.modificationDate, let prev = row.modifiedAt, m > prev, row.state == .done {
                    // Edited in Photos: back up the new rendition too.
                    row.status = BackupState.pending.rawValue
                    row.modifiedAt = m
                    row.updatedAt = Date()
                } else if row.state == .failed {
                    row.status = BackupState.pending.rawValue
                    row.attempts = 0
                }
                return
            }
            self.modelContext.insert(BackupAsset(localIdentifier: asset.localIdentifier, takenAt: asset.creationDate, modifiedAt: asset.modificationDate))
            added += 1
        }
        try modelContext.save()
        return added
    }

    /// One pass: scan, then hash, check, upload and commit until `deadline`.
    public func runOnce(settings: BackupSettings, deadline: Date) async -> BackupRun {
        guard let client, let uploader, !running else {
            var skipped = BackupRun()
            skipped.finished = skipped.started
            skipped.errors.append(running ? "A backup is already running." : "Not connected to a server.")
            return skipped
        }
        running = true
        var run = BackupRun()
        await pass(client: client, uploader: uploader, settings: settings, deadline: deadline, run: &run)
        run.finished = Date()
        lastRun = run
        running = false
        return run
    }

    private func pass(client: NubiloClient, uploader: BackgroundUploader, settings: BackupSettings, deadline: Date, run: inout BackupRun) async {
        guard PHPhotoLibrary.authorizationStatus(for: .readWrite) == .authorized
            || PHPhotoLibrary.authorizationStatus(for: .readWrite) == .limited else {
            run.errors.append("Photos access is off. Allow it in Settings → Nubilo → Photos.")
            return
        }
        do {
            try scan(includeVideos: settings.includeVideos)
        } catch {
            run.errors.append("scan: \(error.localizedDescription)")
            return
        }
        let token: UploadToken
        do {
            token = try await client.uploadToken()
        } catch {
            run.errors.append("upload token: \(error.localizedDescription)")
            return
        }
        await commitUploaded(client: client, run: &run)
        // The BGTask expiration handler cancels the surrounding Task. A full
        // account stops the pass so no more bytes are uploaded for nothing.
        while Date() < deadline, !Task.isCancelled, !run.storageFull {
            let batch = nextPending(limit: 25)
            if batch.isEmpty { break }
            var prepared: [(BackupAsset, ExportedAsset)] = []
            for row in batch {
                guard Date() < deadline else { break }
                do {
                    let exported = try await AssetExporter.export(localIdentifier: row.localIdentifier)
                    row.sha256 = exported.sha256
                    row.liveSHA256 = exported.liveVideo?.sha256
                    row.fileName = exported.fileName
                    prepared.append((row, exported))
                } catch {
                    fail(row, error, run: &run)
                }
            }
            if prepared.isEmpty { continue }
            let missing: Set<String>
            do {
                // Check the Live Photo movie too: a commit naming a missing movie blob fails.
                let hashes = prepared.flatMap { [$0.1.sha256] + ($0.1.liveVideo.map { [$0.sha256] } ?? []) }
                missing = Set(try await client.missingPhotos(hashes: hashes, auth: .bearer(token.token)))
            } catch {
                run.errors.append("check: \(error.localizedDescription)")
                prepared.forEach { $0.1.cleanUp() }
                break
            }
            for (row, exported) in prepared {
                if missing.contains(exported.sha256) || exported.liveVideo.map({ missing.contains($0.sha256) }) == true {
                    if let live = exported.liveVideo {
                        uploader.upload(file: live.url, hash: live.sha256, token: token.token, baseURL: client.baseURL, localIdentifier: row.localIdentifier, part: .liveVideo)
                    }
                    uploader.upload(file: exported.url, hash: exported.sha256, token: token.token, baseURL: client.baseURL, localIdentifier: row.localIdentifier, part: .original)
                    row.status = BackupState.uploading.rawValue
                    run.queued += 1
                } else {
                    exported.cleanUp()
                    if run.storageFull {
                        row.status = BackupState.uploaded.rawValue
                    } else {
                        await commit(row, client: client, run: &run)
                    }
                }
                row.updatedAt = Date()
            }
            try? modelContext.save()
        }
        try? modelContext.save()
    }

    /// Called by the uploader when a blob PUT finished.
    public func uploadFinished(localIdentifier: String, part: UploadPart, ok: Bool, error: String?) async {
        guard let row = find(localIdentifier) else { return }
        if !ok {
            row.status = BackupState.pending.rawValue
            row.lastError = error
            row.attempts += 1
            if row.attempts >= 5 { row.status = BackupState.failed.rawValue }
            try? modelContext.save()
            return
        }
        // The original marks the row uploaded. The Live Photo movie may finish
        // later; then a commit attempted after the original failed is retried.
        if part == .liveVideo {
            guard row.state == .uploaded else { return }
        }
        row.status = BackupState.uploaded.rawValue
        try? modelContext.save()
        if let client {
            var run = BackupRun()
            await commit(row, client: client, run: &run)
            try? modelContext.save()
        }
    }

    private func commitUploaded(client: NubiloClient, run: inout BackupRun) async {
        let uploaded = BackupState.uploaded.rawValue
        let rows = (try? modelContext.fetch(FetchDescriptor<BackupAsset>(predicate: #Predicate { $0.status == uploaded }))) ?? []
        for row in rows where !run.storageFull { await commit(row, client: client, run: &run) }
        try? modelContext.save()
    }

    private func commit(_ row: BackupAsset, client: NubiloClient, run: inout BackupRun) async {
        guard let hash = row.sha256 else {
            row.status = BackupState.pending.rawValue
            return
        }
        do {
            let p = try await client.commitPhoto(.init(
                blobId: hash, name: row.fileName ?? "IMG.JPG",
                takenAt: row.takenAt.map { Int64($0.timeIntervalSince1970 * 1000) },
                localId: row.localIdentifier, liveVideoBlob: row.liveSHA256))
            row.serverID = p.id
            row.status = BackupState.done.rawValue
            row.lastError = nil
            run.committed += 1
        } catch NubiloError.http(507, _) {
            // Over quota: keep the upload and retry once space is freed or the
            // limit raised, without counting it as a failure.
            row.status = BackupState.uploaded.rawValue
            row.lastError = "Storage full"
            if !run.storageFull {
                run.storageFull = true
                run.errors.append("Your storage is full. Free up space or ask an admin to raise your limit.")
            }
        } catch {
            // 403/400: a blob is not on the server yet; keep and retry later.
            row.status = BackupState.uploaded.rawValue
            row.lastError = error.localizedDescription
            row.attempts += 1
            if row.attempts >= 8 { row.status = BackupState.failed.rawValue }
        }
        row.updatedAt = Date()
    }

    private func nextPending(limit: Int) -> [BackupAsset] {
        let pending = BackupState.pending.rawValue
        var d = FetchDescriptor<BackupAsset>(predicate: #Predicate { $0.status == pending }, sortBy: [SortDescriptor(\.takenAt, order: .reverse)])
        d.fetchLimit = limit
        return (try? modelContext.fetch(d)) ?? []
    }

    private func find(_ id: String) -> BackupAsset? {
        var d = FetchDescriptor<BackupAsset>(predicate: #Predicate { $0.localIdentifier == id })
        d.fetchLimit = 1
        return try? modelContext.fetch(d).first
    }

    private func fail(_ row: BackupAsset, _ error: Error, run: inout BackupRun) {
        row.attempts += 1
        row.lastError = error.localizedDescription
        if row.attempts >= 5 { row.status = BackupState.failed.rawValue }
        run.errors.append(error.localizedDescription)
    }
}

// MARK: - Export

public struct ExportedFile: Sendable {
    public var url: URL
    public var sha256: String
}

public struct ExportedAsset: Sendable {
    public var url: URL
    public var sha256: String
    public var fileName: String
    public var liveVideo: ExportedFile?

    /// Removes the temp files (uploads keep their own until completion).
    public func cleanUp() {
        try? FileManager.default.removeItem(at: url)
        if let l = liveVideo { try? FileManager.default.removeItem(at: l.url) }
    }
}

public enum AssetExporter {
    /// Where originals wait for their background upload.
    public static var queueDirectory: URL {
        let d = FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask)[0].appendingPathComponent("UploadQueue", isDirectory: true)
        try? FileManager.default.createDirectory(at: d, withIntermediateDirectories: true)
        return d
    }

    /// Writes the original resource (and a Live Photo's movie) to files and hashes them.
    public static func export(localIdentifier: String) async throws -> ExportedAsset {
        guard let asset = PHAsset.fetchAssets(withLocalIdentifiers: [localIdentifier], options: nil).firstObject else {
            throw NubiloError.http(status: 404, message: "asset no longer in the library")
        }
        let resources = PHAssetResource.assetResources(for: asset)
        let primaryTypes: [PHAssetResourceType] = asset.mediaType == .video ? [.fullSizeVideo, .video] : [.fullSizePhoto, .photo]
        guard let primary = primaryTypes.lazy.compactMap({ t in resources.first { $0.type == t } }).first else {
            throw NubiloError.http(status: 422, message: "no original resource")
        }
        let main = try await write(primary)
        var live: ExportedFile?
        if asset.mediaSubtypes.contains(.photoLive),
           let movie = [PHAssetResourceType.fullSizePairedVideo, .pairedVideo].lazy.compactMap({ t in resources.first { $0.type == t } }).first {
            live = try await write(movie)
        }
        return ExportedAsset(url: main.url, sha256: main.sha256, fileName: primary.originalFilename, liveVideo: live)
    }

    private static func write(_ resource: PHAssetResource) async throws -> ExportedFile {
        let tmp = queueDirectory.appendingPathComponent(UUID().uuidString)
        let opts = PHAssetResourceRequestOptions()
        opts.isNetworkAccessAllowed = true // originals may live in iCloud
        try await withCheckedThrowingContinuation { (cont: CheckedContinuation<Void, Error>) in
            PHAssetResourceManager.default().writeData(for: resource, toFile: tmp, options: opts) { err in
                if let err { cont.resume(throwing: err) } else { cont.resume() }
            }
        }
        let hash = try sha256Hex(fileAt: tmp)
        // Name the file by hash so a retried upload reuses it.
        let final = queueDirectory.appendingPathComponent(hash)
        if FileManager.default.fileExists(atPath: final.path) {
            try? FileManager.default.removeItem(at: tmp)
        } else {
            try FileManager.default.moveItem(at: tmp, to: final)
        }
        return ExportedFile(url: final, sha256: hash)
    }
}

// MARK: - Background uploads

public enum UploadPart: String, Sendable {
    case original
    case liveVideo
}

/// Uploads blobs with a background URLSession so they continue while the app
/// is suspended. Upload tokens are used because a request signature would
/// expire before iOS gets around to sending it.
public final class BackgroundUploader: NSObject, URLSessionTaskDelegate, @unchecked Sendable {
    public static let sessionIdentifier = "dev.nubilo.photos.upload"

    /// Called for every finished upload (on an arbitrary queue).
    public var onFinish: (@Sendable (_ localIdentifier: String, _ part: UploadPart, _ ok: Bool, _ error: String?) -> Void)?
    /// Set from the app delegate's handleEventsForBackgroundURLSession.
    public var backgroundCompletion: (() -> Void)?
    /// The paired server's pinned TLS leaf (self-signed LAN servers); nil trusts the system only.
    public var tlsPin: String?

    private lazy var session: URLSession = {
        let cfg = URLSessionConfiguration.background(withIdentifier: Self.sessionIdentifier)
        cfg.sessionSendsLaunchEvents = true
        cfg.isDiscretionary = false
        return URLSession(configuration: cfg, delegate: self, delegateQueue: nil)
    }()

    public override init() {
        super.init()
    }

    /// Re-attaches to uploads started before the app was relaunched.
    public func resume() { _ = session }

    /// Cancels queued and running uploads (sign out).
    public func cancelAll() {
        session.getAllTasks { tasks in tasks.forEach { $0.cancel() } }
    }

    public func upload(file: URL, hash: String, token: String, baseURL: URL, localIdentifier: String, part: UploadPart) {
        guard let url = URL(string: NubiloClient.blobPath(hash), relativeTo: baseURL)?.absoluteURL else { return }
        var req = URLRequest(url: url)
        req.httpMethod = "PUT"
        req.setValue("Bearer " + token, forHTTPHeaderField: "Authorization")
        req.setValue("application/octet-stream", forHTTPHeaderField: "Content-Type")
        // Per request, because a background session's configuration is fixed at creation.
        req.allowsCellularAccess = BackupSettings.load().allowCellular
        let task = session.uploadTask(with: req, fromFile: file)
        task.taskDescription = [localIdentifier, part.rawValue, file.path].joined(separator: "\n")
        task.resume()
    }

    public func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: Error?) {
        let parts = (task.taskDescription ?? "").components(separatedBy: "\n")
        guard parts.count == 3, let part = UploadPart(rawValue: parts[1]) else { return }
        let status = (task.response as? HTTPURLResponse)?.statusCode ?? 0
        let ok = error == nil && (200..<300).contains(status)
        if ok { try? FileManager.default.removeItem(atPath: parts[2]) }
        onFinish?(parts[0], part, ok, error?.localizedDescription ?? (ok ? nil : "HTTP \(status)"))
    }

    public func urlSession(_ session: URLSession, didReceive challenge: URLAuthenticationChallenge,
                           completionHandler: @escaping (URLSession.AuthChallengeDisposition, URLCredential?) -> Void) {
        guard let pin = tlsPin else { return completionHandler(.performDefaultHandling, nil) }
        PinningDelegate(pin: pin).urlSession(session, didReceive: challenge, completionHandler: completionHandler)
    }

    public func urlSessionDidFinishEvents(forBackgroundURLSession session: URLSession) {
        DispatchQueue.main.async { [weak self] in
            self?.backgroundCompletion?()
            self?.backgroundCompletion = nil
        }
    }
}

// MARK: - Scheduling

#if canImport(BackgroundTasks) && os(iOS)
public enum BackupScheduler {
    public static let taskIdentifier = "dev.nubilo.photos.backup"

    /// Registers the BGProcessingTask handler. Call before the app finishes launching.
    public static func register(engine: @escaping @Sendable () async -> PhotoBackupEngine?) {
        _ = BGTaskScheduler.shared.register(forTaskWithIdentifier: taskIdentifier, using: nil) { task in
            guard let task = task as? BGProcessingTask else { return }
            schedule()
            let work = Task {
                guard let engine = await engine() else { return }
                // BG processing windows are a few minutes; stop early to save state.
                _ = await engine.runOnce(settings: BackupSettings.load(), deadline: Date().addingTimeInterval(240))
            }
            task.expirationHandler = { work.cancel() }
            Task {
                _ = await work.result
                task.setTaskCompleted(success: !work.isCancelled)
            }
        }
    }

    /// Asks iOS for the next processing window (honours the power/network settings).
    public static func schedule() {
        let s = BackupSettings.load()
        guard s.enabled else {
            BGTaskScheduler.shared.cancel(taskRequestWithIdentifier: taskIdentifier)
            return
        }
        let req = BGProcessingTaskRequest(identifier: taskIdentifier)
        req.requiresNetworkConnectivity = true
        req.requiresExternalPower = s.requiresPower
        req.earliestBeginDate = Date(timeIntervalSinceNow: 15 * 60)
        try? BGTaskScheduler.shared.submit(req)
    }
}
#endif
#endif
