import Foundation
import Observation
import NubiloKit

/// Loads a photo list (library or album) and keeps it as a timeline.
///
/// With an index it shows the copy cached on disk at once, then asks the
/// server whether it changed: one small request when it did not, one
/// response with every photo when it did. Servers without the index are paged
/// through by id instead; a page can hold photos from any year, so the
/// timeline is rebuilt after each one. Loading runs in its own task, so it
/// carries on while a photo is open on top of the grid.
@MainActor
@Observable
final class PhotoPager {
    typealias Fetch = (_ after: String?) async throws -> (items: [Photo], next: String?)
    typealias IndexFetch = (_ etag: String?) async throws -> PhotoIndexResult

    private(set) var timeline = PhotoTimeline()
    private(set) var loading = false
    /// Everything the server has is in `timeline`.
    private(set) var exhausted = false
    var error: String?

    var photos: [Photo] { timeline.photos }
    /// Loading page by page (a server without the index): the list grows
    /// as it loads rather than arriving whole.
    var isPaging: Bool { !useIndex }

    private let fetch: Fetch
    private let index: IndexFetch?
    private let cache: PhotoIndexCache?
    private let cacheKey: String

    /// Cleared when the server turns out not to have the index.
    private var useIndex: Bool
    private var etag: String?
    private var triedCache = false

    // Paged loading.
    private var loaded: [Photo] = []
    private var known: Set<String> = []
    private var next: String?

    /// Bumped by `reload`, so results from an older load are dropped.
    private var generation = UUID()
    private var loadTask: Task<Void, Never>?

    init(fetch: @escaping Fetch, index: IndexFetch? = nil, cache: PhotoIndexCache? = nil, cacheKey: String = PhotoIndexCache.libraryKey) {
        self.fetch = fetch
        self.index = index
        self.cache = cache
        self.cacheKey = cacheKey
        useIndex = index != nil
    }

    /// Loads in the background, unless that is under way or done.
    func start() {
        guard loadTask == nil, !loading, !exhausted else { return }
        let generation = self.generation
        loadTask = Task { [weak self] in
            await self?.load(generation)
            if self?.generation == generation { self?.loadTask = nil }
        }
    }

    /// Asks the server again. Returns once there is something new to show;
    /// a paged load carries on in the background, so pull to refresh does
    /// not wait for the whole library.
    func reload() async {
        loadTask?.cancel()
        loadTask = nil
        generation = UUID()
        exhausted = false
        if useIndex {
            await load(generation)
        } else {
            loaded = []
            known = []
            next = nil
            await loadPages(generation, limit: 1)
            start()
        }
    }

    private func load(_ generation: UUID) async {
        if useIndex {
            await loadIndex(generation)
            if useIndex || generation != self.generation { return }
        }
        await loadPages(generation)
    }

    // MARK: Index

    private func loadIndex(_ generation: UUID) async {
        guard let index else { return }
        loading = true
        defer { if generation == self.generation { loading = false } }

        if !triedCache, let cache {
            triedCache = true
            let key = cacheKey
            // Reading and sorting 10 000+ photos takes long enough to stall
            // the first frames; do it off the main actor.
            let cached = await Task.detached(priority: .userInitiated) { () -> (String?, PhotoTimeline)? in
                guard let entry = cache.load(key) else { return nil }
                return (entry.etag, PhotoTimeline(photos: entry.photos))
            }.value
            guard generation == self.generation else { return }
            if let cached, timeline.isEmpty {
                etag = cached.0
                timeline = cached.1
            }
        }

        do {
            switch try await index(etag) {
            case .notModified:
                guard generation == self.generation else { return }
                exhausted = true
            case let .photos(photos, newETag):
                guard generation == self.generation else { return }
                let built = await Task.detached(priority: .userInitiated) { PhotoTimeline(photos: photos) }.value
                guard generation == self.generation else { return }
                timeline = built
                etag = newETag
                exhausted = true
                if let cache {
                    let key = cacheKey
                    Task.detached(priority: .utility) {
                        cache.save(PhotoIndexCache.Entry(etag: newETag, photos: photos), as: key)
                    }
                }
            }
        } catch is PhotoIndexUnavailable {
            useIndex = false
        } catch {
            guard generation == self.generation, !Task.isCancelled else { return }
            // Offline with a cached copy: show it without an alert. The
            // "Load the Rest" button offers a retry.
            if timeline.isEmpty { self.error = describe(error) }
        }
    }

    // MARK: Paged (servers without the index)

    private func loadPages(_ generation: UUID, limit: Int = .max) async {
        loading = true
        defer { if generation == self.generation { loading = false } }
        var pages = 0
        while !exhausted, pages < limit, !Task.isCancelled {
            pages += 1
            do {
                let page = try await fetch(next)
                // A reload started meanwhile: its load owns the state now.
                guard generation == self.generation else { return }
                let fresh = page.items.filter { known.insert($0.id).inserted }
                loaded.append(contentsOf: fresh)
                next = page.next
                exhausted = (page.next ?? "").isEmpty
                // Sorting tens of thousands of photos takes long enough to
                // stall scrolling; do it off the main actor.
                let snapshot = loaded
                let built = await Task.detached(priority: .userInitiated) { PhotoTimeline(photos: snapshot) }.value
                guard generation == self.generation else { return }
                timeline = built
            } catch {
                guard generation == self.generation else { return }
                if !Task.isCancelled { self.error = describe(error) }
                return
            }
        }
    }
}
