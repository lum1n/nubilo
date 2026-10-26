import Foundation
import Observation
import NubiloKit

/// Pages through a photo list (library or album) on demand.
@MainActor
@Observable
final class PhotoPager {
    typealias Fetch = (_ after: String?) async throws -> (items: [Photo], next: String?)

    private(set) var photos: [Photo] = []
    private(set) var loading = false
    private(set) var exhausted = false
    var error: String?

    private var next: String?
    private let fetch: Fetch

    init(fetch: @escaping Fetch) { self.fetch = fetch }

    func reload() async {
        photos = []
        next = nil
        exhausted = false
        await loadMore()
    }

    func loadMore() async {
        guard !loading, !exhausted else { return }
        loading = true
        defer { loading = false }
        do {
            let page = try await fetch(next)
            let known = Set(photos.map(\.id))
            photos.append(contentsOf: page.items.filter { !known.contains($0.id) })
            next = page.next
            exhausted = (page.next ?? "").isEmpty
        } catch {
            self.error = describe(error)
        }
    }
}
