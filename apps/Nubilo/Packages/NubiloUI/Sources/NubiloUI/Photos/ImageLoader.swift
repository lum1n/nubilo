import SwiftUI
import NubiloKit

/// Loads photo renditions through the client. Decoded images stay in an
/// in-memory NSCache; the bytes also land in the client's 500 MB URLCache
/// when the server allows caching.
@MainActor
final class ImageLoader {
    static let shared = ImageLoader()

    private let cache = NSCache<NSString, PlatformImage>()
    // Data, not images: Task results must be Sendable and NSImage is not.
    private var inFlight: [String: Task<Data?, Never>] = [:]

    init() {
        cache.countLimit = 800
    }

    func image(_ id: String, rendition: String, client: NubiloClient) async -> PlatformImage? {
        let key = "\(rendition)/\(id)"
        if let hit = cache.object(forKey: key as NSString) { return hit }
        let task: Task<Data?, Never>
        if let running = inFlight[key] {
            task = running
        } else {
            task = Task { try? await client.photoData(id, rendition: rendition) }
            inFlight[key] = task
        }
        let data = await task.value
        inFlight[key] = nil
        if let hit = cache.object(forKey: key as NSString) { return hit }
        guard let data, let image = PlatformImage(data: data) else { return nil }
        cache.setObject(image, forKey: key as NSString)
        return image
    }

    func removeAll() { cache.removeAllObjects() }
}

/// A photo rendition ("thumb", "preview") loaded from the server.
struct RemoteImage: View {
    let id: String
    var rendition = "thumb"
    var contentMode = ContentMode.fill

    @Environment(AppModel.self) private var model
    @State private var image: PlatformImage?
    @State private var failed = false

    var body: some View {
        ZStack {
            if let image {
                Image(platformImage: image)
                    .resizable()
                    .aspectRatio(contentMode: contentMode)
            } else if failed {
                Image(systemName: "photo")
                    .foregroundStyle(.secondary)
            } else {
                Rectangle().fill(.quaternary)
            }
        }
        .task(id: "\(rendition)/\(id)") {
            guard let client = model.client else { return }
            let loaded = await ImageLoader.shared.image(id, rendition: rendition, client: client)
            image = loaded
            failed = loaded == nil
        }
    }
}
