import Foundation

/// Persists the paired server as JSON in Application Support (no secrets:
/// the private key is in the key store).
public struct ServerStore: Sendable {
    public let url: URL

    public init(directory: URL? = nil) {
        let base = directory ?? FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("Nubilo", isDirectory: true)
        url = base.appendingPathComponent("server.json")
    }

    public func load() -> PairedServer? {
        guard let data = try? Data(contentsOf: url) else { return nil }
        return try? JSONDecoder().decode(PairedServer.self, from: data)
    }

    public func save(_ s: PairedServer) throws {
        try FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
        let data = try JSONEncoder().encode(s)
        try data.write(to: url, options: [.atomic])
    }

    public func clear() { try? FileManager.default.removeItem(at: url) }
}
