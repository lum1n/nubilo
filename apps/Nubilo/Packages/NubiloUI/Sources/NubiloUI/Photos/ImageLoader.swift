import SwiftUI
import ImageIO
import NubiloKit

/// Loads photo renditions through the client.
///
/// - URLs carry the rendition's content hash, so the client's URL cache keeps
///   the bytes for good and a second visit does not touch the network.
/// - Bytes are decoded and scaled down off the main actor, ready to draw.
/// - Decoded images stay in memory up to a byte budget; originals, for
///   zooming in, only the last few.
/// - A download nobody waits for any more (its tile scrolled away) is
///   cancelled.
@MainActor
final class ImageLoader {
    static let shared = ImageLoader()

    private let cache = NSCache<NSString, PlatformImage>()
    private let originals = NSCache<NSString, PlatformImage>()
    private var inFlight: [String: Flight] = [:]

    /// One download and decode, shared by every view that wants it.
    private final class Flight: @unchecked Sendable {
        let task: Task<CGImage?, Never>
        var waiters = 0
        var cancelled = 0

        init(task: Task<CGImage?, Never>) { self.task = task }
    }

    init() {
        cache.totalCostLimit = 150 << 20
        originals.countLimit = 3
    }

    /// The longest side a rendition is decoded at.
    static func maxPixels(_ rendition: PhotoRendition) -> Int {
        switch rendition {
        case .thumb: 512
        case .preview: 2048
        case .original: 4096
        }
    }

    /// An image already in memory, without asking the server.
    func cached(_ photo: Photo, _ rendition: PhotoRendition) -> PlatformImage? {
        store(for: rendition).object(forKey: key(photo, rendition) as NSString)
    }

    func image(_ photo: Photo, _ rendition: PhotoRendition, client: NubiloClient) async -> PlatformImage? {
        let key = key(photo, rendition)
        let memory = store(for: rendition)
        if let hit = memory.object(forKey: key as NSString) { return hit }

        let flight: Flight
        if let running = inFlight[key], !running.task.isCancelled {
            flight = running
        } else {
            let max = Self.maxPixels(rendition)
            flight = Flight(task: Task.detached(priority: .userInitiated) {
                guard let data = try? await client.photoData(photo, rendition: rendition), !Task.isCancelled else { return nil }
                return Self.decode(data, maxPixels: max)
            })
            inFlight[key] = flight
        }
        flight.waiters += 1

        let decoded = await withTaskCancellationHandler {
            await flight.task.value
        } onCancel: {
            Task { @MainActor in
                flight.cancelled += 1
                if flight.cancelled >= flight.waiters { flight.task.cancel() }
            }
        }

        if inFlight[key] === flight { inFlight[key] = nil }
        if let hit = memory.object(forKey: key as NSString) { return hit }
        guard let decoded else { return nil }
        let image = Self.platformImage(decoded)
        memory.setObject(image, forKey: key as NSString, cost: decoded.bytesPerRow * decoded.height)
        return image
    }

    func removeAll() {
        cache.removeAllObjects()
        originals.removeAllObjects()
    }

    private func key(_ photo: Photo, _ rendition: PhotoRendition) -> String {
        "\(rendition.rawValue)/\(photo.id)/\(photo.version(of: rendition) ?? "")"
    }

    private func store(for rendition: PhotoRendition) -> NSCache<NSString, PlatformImage> {
        rendition == .original ? originals : cache
    }

    /// Decodes at most `maxPixels` on the long side, upright, and fully, so
    /// drawing it later on the main thread does no work. A 48 MP original
    /// decoded in full would take about 200 MB.
    nonisolated private static func decode(_ data: Data, maxPixels: Int) -> CGImage? {
        guard let source = CGImageSourceCreateWithData(data as CFData, nil) else { return nil }
        let options: [CFString: Any] = [
            kCGImageSourceCreateThumbnailFromImageAlways: true,
            kCGImageSourceCreateThumbnailWithTransform: true,
            kCGImageSourceShouldCacheImmediately: true,
            kCGImageSourceThumbnailMaxPixelSize: maxPixels,
        ]
        return CGImageSourceCreateThumbnailAtIndex(source, 0, options as CFDictionary)
    }

    private static func platformImage(_ cg: CGImage) -> PlatformImage {
        #if os(iOS)
        return UIImage(cgImage: cg)
        #else
        return NSImage(cgImage: cg, size: NSSize(width: cg.width, height: cg.height))
        #endif
    }
}

/// A photo rendition loaded from the server, filling its frame. A video the
/// server made no thumbnail for shows a placeholder rather than downloading
/// the whole file.
struct RemoteImage: View {
    let photo: Photo
    var rendition = PhotoRendition.thumb
    var contentMode = ContentMode.fill

    @Environment(AppModel.self) private var model
    @Environment(\.photoLoadingPaused) private var paused
    @State private var image: PlatformImage?
    @State private var failed = false

    private var skipped: Bool {
        photo.fallsBackToOriginal(rendition) && (photo.isVideo || photo.kind == "raw")
    }

    var body: some View {
        ZStack {
            if let image {
                Image(platformImage: image)
                    .resizable()
                    .aspectRatio(contentMode: contentMode)
            } else if skipped || failed {
                Rectangle().fill(.quaternary)
                    .overlay {
                        Image(systemName: photo.isVideo ? "video" : "photo")
                            .foregroundStyle(.secondary)
                    }
            } else {
                Rectangle().fill(.quaternary)
            }
        }
        .task(id: "\(rendition.rawValue)/\(photo.id)/\(photo.version(of: rendition) ?? "")/\(paused)") {
            if let hit = ImageLoader.shared.cached(photo, rendition) {
                image = hit
                return
            }
            guard !paused, !skipped, let client = model.client else { return }
            let loaded = await ImageLoader.shared.image(photo, rendition, client: client)
            guard !Task.isCancelled else { return }
            image = loaded
            failed = loaded == nil
        }
    }
}

private struct PhotoLoadingPausedKey: EnvironmentKey {
    static let defaultValue = false
}

extension EnvironmentValues {
    /// Tiles show only images already in memory and fetch nothing, e.g.
    /// while the timeline scrubber flies the grid past thousands of photos.
    var photoLoadingPaused: Bool {
        get { self[PhotoLoadingPausedKey.self] }
        set { self[PhotoLoadingPausedKey.self] = newValue }
    }
}
