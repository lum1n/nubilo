import Foundation

/// The last photo index seen from the server, one file per library or album,
/// so the gallery shows at once on launch and asks the server only whether
/// it changed (`If-None-Match`).
public struct PhotoIndexCache: Sendable {
    public struct Entry: Codable, Sendable, Equatable {
        public var etag: String?
        public var photos: [Photo]

        public init(etag: String?, photos: [Photo]) {
            self.etag = etag
            self.photos = photos
        }
    }

    public let directory: URL

    public init(directory: URL) {
        self.directory = directory
    }

    /// Under Caches, kept apart per server and device so accounts never see
    /// each other's lists.
    public static func standard(for client: NubiloClient) -> PhotoIndexCache {
        let caches = FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask)[0]
        let identity = "\(client.baseURL.absoluteString)|\(client.signer?.deviceID ?? "")"
        let folder = String(sha256Hex(Data(identity.utf8)).prefix(16))
        return PhotoIndexCache(directory: caches.appendingPathComponent("PhotoIndex/\(folder)", isDirectory: true))
    }

    /// Every cached index on this device (sign out).
    public static func removeAll() {
        let caches = FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask)[0]
        try? FileManager.default.removeItem(at: caches.appendingPathComponent("PhotoIndex", isDirectory: true))
    }

    public static let libraryKey = "library"
    public static func albumKey(_ id: String) -> String { "album-\(id)" }

    public func load(_ key: String) -> Entry? {
        guard let data = try? Data(contentsOf: file(key)) else { return nil }
        return try? JSONDecoder().decode(Entry.self, from: data)
    }

    public func save(_ entry: Entry, as key: String) {
        guard let data = try? JSONEncoder().encode(entry) else { return }
        try? FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        try? data.write(to: file(key), options: [.atomic])
    }

    public func remove(_ key: String) {
        try? FileManager.default.removeItem(at: file(key))
    }

    private func file(_ key: String) -> URL {
        // Keys are ours ("library", "album-<id>"); hash them anyway so an odd
        // album id cannot leave the folder.
        directory.appendingPathComponent(String(sha256Hex(Data(key.utf8)).prefix(32)) + ".json")
    }
}
