import FileProvider
import NubiloKit

/// Lists a folder, or reports changes for the working set. In a replicated
/// File Provider the system learns about changes through the working set,
/// so folders are only listed, never diffed.
final class FileProviderEnumerator: NSObject, NSFileProviderEnumerator {
    private let container: NSFileProviderItemIdentifier
    private let session: () throws -> FilesSession
    private let mapError: (Error, NSFileProviderItemIdentifier) -> Error
    /// Told about listed items (the extension remembers which have thumbnails).
    private let seen: ([RemoteItem]) -> Void

    init(container: NSFileProviderItemIdentifier, session: @escaping () throws -> FilesSession,
         mapError: @escaping (Error, NSFileProviderItemIdentifier) -> Error,
         seen: @escaping ([RemoteItem]) -> Void = { _ in }) {
        self.container = container
        self.session = session
        self.mapError = mapError
        self.seen = seen
    }

    func invalidate() {}

    func enumerateItems(for observer: NSFileProviderEnumerationObserver, startingAt page: NSFileProviderPage) {
        let container = container
        let session = session
        let mapError = mapError
        let seen = seen
        Task {
            do {
                // The working set is everything: after an expired anchor the
                // system lists it again and treats what is missing as deleted.
                let items: [RemoteItem]
                if container == .workingSet {
                    items = try await session().allItems()
                } else {
                    items = try await session().children(of: container == .rootContainer ? RemoteItem.rootID : container.rawValue)
                }
                seen(items)
                observer.didEnumerate(items.map(FileProviderItem.init))
                observer.finishEnumerating(upTo: nil)
            } catch {
                observer.finishEnumeratingWithError(mapError(error, container))
            }
        }
    }

    func enumerateChanges(for observer: NSFileProviderChangeObserver, from anchor: NSFileProviderSyncAnchor) {
        let container = container
        let session = session
        let mapError = mapError
        let seen = seen
        Task {
            guard container == .workingSet else {
                observer.finishEnumeratingChanges(upTo: anchor, moreComing: false)
                return
            }
            guard let previous = FilesAnchor.decode(anchor.rawValue) else {
                observer.finishEnumeratingWithError(NSFileProviderError(.syncAnchorExpired))
                return
            }
            do {
                let changes = try await session().changes(since: previous)
                if !changes.updated.isEmpty {
                    seen(changes.updated)
                    observer.didUpdate(changes.updated.map(FileProviderItem.init))
                }
                if !changes.deleted.isEmpty {
                    observer.didDeleteItems(withIdentifiers: changes.deleted.map { NSFileProviderItemIdentifier($0) })
                }
                observer.finishEnumeratingChanges(upTo: NSFileProviderSyncAnchor(changes.anchor.encoded()), moreComing: changes.hasMore)
            } catch {
                observer.finishEnumeratingWithError(mapError(error, container))
            }
        }
    }

    func currentSyncAnchor(completionHandler: @escaping (NSFileProviderSyncAnchor?) -> Void) {
        let session = session
        Task {
            let anchor = try? await session().currentAnchor()
            completionHandler(anchor.map { NSFileProviderSyncAnchor($0.encoded()) })
        }
    }
}
