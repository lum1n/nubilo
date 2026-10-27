import FileProvider
import UniformTypeIdentifiers
import NubiloKit

/// The Nubilo location in the Files app: the user's own and shared folders,
/// read and written through the signed sync API with the app's device key
/// (shared through the Keychain; see SharedCredentials).
final class FileProviderExtension: NSObject, NSFileProviderReplicatedExtension {
    private let domain: NSFileProviderDomain
    private let lock = NSLock()
    private var cached: FilesSession?

    required init(domain: NSFileProviderDomain) {
        self.domain = domain
        super.init()
    }

    func invalidate() {
        lock.lock()
        cached = nil
        lock.unlock()
    }

    /// One session (one URLSession) per extension instance. It is rebuilt
    /// after an authentication error, which also picks up a new pairing.
    private func session() throws -> FilesSession {
        lock.lock()
        defer { lock.unlock() }
        if let cached { return cached }
        guard let creds = SharedCredentials(), let client = try? creds.client() else {
            throw NSFileProviderError(.notAuthenticated)
        }
        let s = FilesSession(client: client)
        cached = s
        return s
    }

    /// Maps `error` and drops the session when the server stopped accepting it.
    fileprivate func failed(_ error: Error, _ identifier: NSFileProviderItemIdentifier) -> Error {
        let mapped = fileProviderError(error, identifier)
        if (mapped as NSError).domain == NSFileProviderErrorDomain,
           (mapped as NSError).code == NSFileProviderError.notAuthenticated.rawValue {
            invalidate()
        }
        return mapped
    }

    func item(for identifier: NSFileProviderItemIdentifier, request: NSFileProviderRequest,
              completionHandler: @escaping (NSFileProviderItem?, Error?) -> Void) -> Progress {
        let progress = Progress(totalUnitCount: 1)
        Task {
            defer { progress.completedUnitCount = 1 }
            if identifier == .rootContainer {
                completionHandler(FileProviderItem.root, nil)
                return
            }
            do {
                completionHandler(FileProviderItem(try await session().item(identifier.rawValue)), nil)
            } catch {
                completionHandler(nil, failed(error, identifier))
            }
        }
        return progress
    }

    func fetchContents(for itemIdentifier: NSFileProviderItemIdentifier, version requestedVersion: NSFileProviderItemVersion?,
                       request: NSFileProviderRequest,
                       completionHandler: @escaping (URL?, NSFileProviderItem?, Error?) -> Void) -> Progress {
        let progress = Progress(totalUnitCount: 1)
        let domain = domain
        Task {
            defer { progress.completedUnitCount = 1 }
            do {
                let s = try session()
                let item = try await s.item(itemIdentifier.rawValue)
                let data = try await s.contents(of: item)
                let dir = (try? NSFileProviderManager(for: domain)?.temporaryDirectoryURL()) ?? FileManager.default.temporaryDirectory
                let url = dir.appendingPathComponent(UUID().uuidString)
                try data.write(to: url)
                completionHandler(url, FileProviderItem(item), nil)
            } catch {
                completionHandler(nil, nil, failed(error, itemIdentifier))
            }
        }
        return progress
    }

    func createItem(basedOn itemTemplate: NSFileProviderItem, fields: NSFileProviderItemFields, contents url: URL?,
                    options: NSFileProviderCreateItemOptions = [], request: NSFileProviderRequest,
                    completionHandler: @escaping (NSFileProviderItem?, NSFileProviderItemFields, Bool, Error?) -> Void) -> Progress {
        let progress = Progress(totalUnitCount: 1)
        Task {
            defer { progress.completedUnitCount = 1 }
            do {
                let s = try session()
                let parent = remoteID(itemTemplate.parentItemIdentifier)
                let type = itemTemplate.contentType ?? .data
                // Re-imports (e.g. after the location was reset) offer items
                // the server may already have; reuse them instead of copying.
                if options.contains(.mayAlreadyExist),
                   let existing = try await s.child(named: itemTemplate.filename, in: parent),
                   (existing.kind == .folder) == type.conforms(to: .folder) {
                    completionHandler(FileProviderItem(existing), [], false, nil)
                    return
                }
                let created: RemoteItem
                if type.conforms(to: .folder) {
                    created = try await s.createFolder(named: itemTemplate.filename, in: parent)
                } else if type.conforms(to: .symbolicLink) || type.conforms(to: .aliasFile) {
                    throw CocoaError(.featureUnsupported)
                } else {
                    let data = try url.map { try Data(contentsOf: $0, options: .mappedIfSafe) } ?? Data()
                    created = try await s.createFile(named: itemTemplate.filename, in: parent, data: data,
                                                     mime: type.preferredMIMEType)
                }
                completionHandler(FileProviderItem(created), [], false, nil)
            } catch {
                completionHandler(nil, [], false, failed(error, itemTemplate.parentItemIdentifier))
            }
        }
        return progress
    }

    func modifyItem(_ item: NSFileProviderItem, baseVersion version: NSFileProviderItemVersion,
                    changedFields: NSFileProviderItemFields, contents newContents: URL?,
                    options: NSFileProviderModifyItemOptions = [], request: NSFileProviderRequest,
                    completionHandler: @escaping (NSFileProviderItem?, NSFileProviderItemFields, Bool, Error?) -> Void) -> Progress {
        let progress = Progress(totalUnitCount: 1)
        Task {
            defer { progress.completedUnitCount = 1 }
            do {
                let s = try session()
                let current = try await s.item(item.itemIdentifier.rawValue)
                let name = changedFields.contains(.filename) ? item.filename : nil
                let parent = changedFields.contains(.parentItemIdentifier) ? remoteID(item.parentItemIdentifier) : nil
                let updated: RemoteItem
                switch current.kind {
                case .folder:
                    updated = (name != nil || parent != nil) ? try await s.updateFolder(current, name: name, parentID: parent) : current
                case .file:
                    var data: Data?
                    if changedFields.contains(.contents), let newContents {
                        data = try Data(contentsOf: newContents, options: .mappedIfSafe)
                    }
                    if data == nil && name == nil && parent == nil {
                        updated = current
                    } else {
                        let mime = name.flatMap { UTType(filenameExtension: ($0 as NSString).pathExtension)?.preferredMIMEType }
                        updated = try await s.updateFile(current, data: data, name: name, parentID: parent, mime: mime)
                    }
                }
                // Dates, tags and favorite rank are not stored on the server.
                completionHandler(FileProviderItem(updated), [], false, nil)
            } catch {
                completionHandler(nil, [], false, failed(error, item.itemIdentifier))
            }
        }
        return progress
    }

    func deleteItem(identifier: NSFileProviderItemIdentifier, baseVersion version: NSFileProviderItemVersion,
                    options: NSFileProviderDeleteItemOptions = [], request: NSFileProviderRequest,
                    completionHandler: @escaping (Error?) -> Void) -> Progress {
        let progress = Progress(totalUnitCount: 1)
        Task {
            defer { progress.completedUnitCount = 1 }
            do {
                let s = try session()
                try await s.delete(try await s.item(identifier.rawValue))
                completionHandler(nil)
            } catch FilesError.notFound {
                completionHandler(nil) // already gone
            } catch {
                completionHandler(failed(error, identifier))
            }
        }
        return progress
    }

    func enumerator(for containerItemIdentifier: NSFileProviderItemIdentifier, request: NSFileProviderRequest) throws -> NSFileProviderEnumerator {
        FileProviderEnumerator(container: containerItemIdentifier, session: { [weak self] in
            guard let self else { throw NSFileProviderError(.notAuthenticated) }
            return try self.session()
        }, mapError: { [weak self] error, id in
            self?.failed(error, id) ?? fileProviderError(error, id)
        })
    }

    private func remoteID(_ identifier: NSFileProviderItemIdentifier) -> String {
        identifier == .rootContainer ? RemoteItem.rootID : identifier.rawValue
    }
}

/// Maps Nubilo errors to the ones the Files app knows how to show.
func fileProviderError(_ error: Error, _ identifier: NSFileProviderItemIdentifier) -> Error {
    switch error {
    case FilesError.notFound:
        return NSError.fileProviderErrorForNonExistentItem(withIdentifier: identifier)
    case FilesError.anchorExpired:
        return NSFileProviderError(.syncAnchorExpired)
    case FilesError.readOnly, FilesError.filesNeedAFolder:
        return NSError(domain: NSCocoaErrorDomain, code: NSFileWriteNoPermissionError,
                       userInfo: [NSLocalizedDescriptionKey: error.localizedDescription])
    case NubiloError.notPaired:
        return NSFileProviderError(.notAuthenticated)
    case let e as NubiloError where e.status == 401 || e.status == 403:
        return NSFileProviderError(.notAuthenticated)
    case let e as NubiloError where e.status == 404:
        return NSError.fileProviderErrorForNonExistentItem(withIdentifier: identifier)
    case is URLError:
        return NSFileProviderError(.serverUnreachable)
    default:
        return error
    }
}
