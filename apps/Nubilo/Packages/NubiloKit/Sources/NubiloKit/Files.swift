import Foundation
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif

// Files as the iOS Files app integration (File Provider extension) sees
// them: folders are "files" collections, files are "file" objects. This part
// is platform-neutral so it can be tested on Linux; the extension maps
// RemoteItem to NSFileProviderItem.

/// A folder or file on the server.
public struct RemoteItem: Equatable, Sendable {
    public enum Kind: String, Codable, Sendable {
        case folder, file
    }

    /// Identifier of the top level, which lists the user's own and shared folders.
    public static let rootID = "root"

    /// "c:<collection id>" for folders, "o:<object id>" for files. Stable
    /// across renames and moves.
    public var id: String
    public var parentID: String
    public var name: String
    public var kind: Kind
    public var size: Int64
    /// Content version: the blob hash of a file, empty for folders.
    public var contentHash: String
    /// Metadata version: changes on rename, move or edit.
    public var revision: UInt64
    public var modified: Date
    public var blobID: String?
    public var mime: String?
    public var writable: Bool

    public var collectionID: String? { kind == .folder ? String(id.dropFirst(2)) : nil }
    public var objectID: String? { kind == .file ? String(id.dropFirst(2)) : nil }

    /// Extensions of images the server makes thumbnails of (not HEIC or RAW).
    public static let thumbnailExtensions: Set<String> = ["jpg", "jpeg", "png", "gif", "webp", "bmp", "tif", "tiff"]

    /// True for files the server can make a thumbnail of, by name or MIME type.
    public var hasThumbnail: Bool {
        guard kind == .file, size > 0 else { return false }
        return Self.mayHaveThumbnail(name: name, mime: mime)
    }

    public static func mayHaveThumbnail(name: String, mime: String?) -> Bool {
        let ext = (name as NSString).pathExtension.lowercased()
        if !ext.isEmpty { return thumbnailExtensions.contains(ext) }
        guard let mime = mime?.lowercased(), mime.hasPrefix("image/") else { return false }
        return thumbnailExtensions.contains(String(mime.dropFirst("image/".count)))
    }

    public static func folderID(_ collectionID: String) -> String { "c:" + collectionID }
    public static func fileID(_ objectID: String) -> String { "o:" + objectID }

    /// `col` as a folder. Shared top-level folders carry the owner's name.
    public static func folder(_ col: SyncCollection) -> RemoteItem {
        let parent = (col.parentId ?? "").isEmpty ? rootID : folderID(col.parentId!)
        var name = col.name
        if (col.parentId ?? "").isEmpty, !col.isOwn, let owner = col.owner, !owner.isEmpty {
            name += " (\(owner))"
        }
        return RemoteItem(id: folderID(col.id), parentID: parent, name: name, kind: .folder, size: 0, contentHash: "",
                          revision: col.revision, modified: Date(timeIntervalSince1970: TimeInterval(col.updatedAt) / 1000),
                          blobID: nil, mime: nil, writable: col.isWritable)
    }

    public static func file(_ o: SyncObject, writable: Bool) -> RemoteItem {
        RemoteItem(id: fileID(o.id), parentID: folderID(o.collectionId), name: o.name ?? o.id, kind: .file, size: o.size,
                   contentHash: o.contentHash, revision: o.revision,
                   modified: Date(timeIntervalSince1970: TimeInterval(o.updatedAt) / 1000),
                   blobID: o.blobId, mime: o.mime, writable: writable)
    }

    static func file(_ c: SyncChangeRecord, writable: Bool, at date: Date) -> RemoteItem {
        RemoteItem(id: fileID(c.objectId), parentID: folderID(c.collectionId), name: c.name ?? c.objectId, kind: .file,
                   size: c.size, contentHash: c.contentHash, revision: c.revision, modified: date,
                   blobID: c.blobId, mime: c.mime, writable: writable)
    }
}

public enum FilesError: Error, LocalizedError, Equatable {
    /// The change anchor is too old or the server was restored: enumerate again.
    case anchorExpired
    case notFound
    /// Files must live in a folder; the top level holds folders only.
    case filesNeedAFolder
    case readOnly
    /// The server predates the folder calls (`/sync/v1/objects` and friends).
    case serverTooOld
    /// The server makes no thumbnail of this file (not an image it decodes,
    /// or a server without thumbnails).
    case noThumbnail

    public var errorDescription: String? {
        switch self {
        case .anchorExpired: return "The file list changed too much; it will be loaded again."
        case .notFound: return "This item is no longer on the server."
        case .filesNeedAFolder: return "Put files into a folder, such as Files."
        case .readOnly: return "This folder is shared with you read-only."
        case .serverTooOld: return "The Nubilo server needs an update for the Files app."
        case .noThumbnail: return "There is no preview of this file."
        }
    }
}

/// Where change enumeration stopped: the journal position and the folders
/// seen then (folder renames, moves and deletes are not in the journal).
public struct FilesAnchor: Codable, Equatable, Sendable {
    public struct FolderStamp: Codable, Equatable, Sendable {
        public var revision: UInt64
        public var parentID: String
        public var name: String
    }

    public var seq: Int64
    public var folders: [String: FolderStamp]

    public func encoded() -> Data { (try? JSONEncoder().encode(self)) ?? Data() }

    public static func decode(_ data: Data) -> FilesAnchor? { try? JSONDecoder().decode(FilesAnchor.self, from: data) }
}

/// One batch of changes since an anchor.
public struct FilesChanges: Sendable {
    public var updated: [RemoteItem]
    public var deleted: [String]
    public var anchor: FilesAnchor
    public var hasMore: Bool
}

/// File operations against one server.
public struct FilesSession: Sendable {
    public let client: NubiloClient

    public init(client: NubiloClient) { self.client = client }

    /// Live "files" collections the device can see.
    /// Lists objects, telling an outdated server apart from a missing folder.
    private func objects(_ collectionID: String) async throws -> [SyncObject] {
        do {
            return try await client.objects(collectionID: collectionID)
        } catch let e as NubiloError where e.status == 404 || e.status == 405 {
            // An old server answers its router's plain "404 page not found";
            // a missing folder is "not found".
            if case let .http(_, message) = e, message.contains("page not found") || e.status == 405 {
                throw FilesError.serverTooOld
            }
            throw FilesError.notFound
        }
    }

    public func folders() async throws -> [SyncCollection] {
        try await client.syncCollections().filter { $0.kind == "files" && $0.deletedAt == nil }
    }

    public func children(of parentID: String) async throws -> [RemoteItem] {
        let cols = try await folders()
        if parentID == RemoteItem.rootID {
            return cols.filter { ($0.parentId ?? "").isEmpty }.map(RemoteItem.folder)
        }
        guard let colID = parentID.hasPrefix("c:") ? String(parentID.dropFirst(2)) : nil,
              let col = cols.first(where: { $0.id == colID }) else { throw FilesError.notFound }
        let subfolders = cols.filter { $0.parentId == colID }.map(RemoteItem.folder)
        let files = try await objects(colID).filter { $0.kind == "file" }
            .map { RemoteItem.file($0, writable: col.isWritable) }
        return subfolders + files
    }

    /// Every folder and file the device can see (the File Provider working set).
    public func allItems() async throws -> [RemoteItem] {
        let cols = try await folders()
        var out = cols.map(RemoteItem.folder)
        for col in cols {
            out += try await objects(col.id).filter { $0.kind == "file" }
                .map { RemoteItem.file($0, writable: col.isWritable) }
        }
        return out
    }

    /// The child of `parentID` named `name`, if there is one.
    public func child(named name: String, in parentID: String) async throws -> RemoteItem? {
        let wanted = davName(name)
        return try await children(of: parentID).first { $0.name == wanted }
    }

    public func item(_ id: String) async throws -> RemoteItem {
        if id.hasPrefix("c:") {
            guard let col = try await folders().first(where: { $0.id == String(id.dropFirst(2)) }) else { throw FilesError.notFound }
            return .folder(col)
        }
        guard id.hasPrefix("o:") else { throw FilesError.notFound }
        let obj: SyncObject
        do {
            obj = try await client.object(id: String(id.dropFirst(2)))
        } catch let e as NubiloError where e.status == 404 {
            throw FilesError.notFound
        }
        let writable = try await folders().first(where: { $0.id == obj.collectionId })?.isWritable ?? false
        return .file(obj, writable: writable)
    }

    public func contents(of item: RemoteItem) async throws -> Data {
        guard let hash = item.blobID ?? (item.contentHash.isEmpty ? nil : item.contentHash) else { return Data() }
        return try await client.blob(hash)
    }

    /// A JPEG thumbnail of file item `id` ("o:<object id>"), `size` pixels
    /// on the longer side (the server rounds up).
    public func thumbnail(of id: String, size: Int) async throws -> Data {
        guard id.hasPrefix("o:") else { throw FilesError.noThumbnail }
        do {
            return try await client.thumbnail(objectID: String(id.dropFirst(2)), size: size)
        } catch let e as NubiloError {
            throw Self.thumbnailError(e)
        }
    }

    /// Maps a thumbnail call's HTTP error; an older server without the call
    /// answers its router's "404 page not found" or 405.
    static func thumbnailError(_ e: NubiloError) -> Error {
        switch e.status {
        case 404:
            if case let .http(_, message) = e, message.contains("page not found") { return FilesError.noThumbnail }
            return FilesError.notFound
        case 405, 413, 415:
            return FilesError.noThumbnail
        default:
            return e
        }
    }

    public func createFolder(named name: String, in parentID: String) async throws -> RemoteItem {
        let parent: String? = parentID == RemoteItem.rootID ? nil : try collection(of: parentID)
        let col = try await client.ensureCollection(kind: "files", name: davName(name), parentID: parent)
        return .folder(col)
    }

    /// Uploads `data` as a new file in folder `parentID`.
    public func createFile(named name: String, in parentID: String, data: Data, mime: String?) async throws -> RemoteItem {
        guard parentID != RemoteItem.rootID else { throw FilesError.filesNeedAFolder }
        let colID = try collection(of: parentID)
        return try await writeFile(objectID: ULID.make(), revision: nil, collectionID: colID, name: davName(name),
                                   data: data, existingHash: nil, size: nil, mime: mime)
    }

    /// Changes a file's contents (when `data` is set), name and/or folder.
    public func updateFile(_ item: RemoteItem, data: Data?, name: String?, parentID: String?, mime: String?) async throws -> RemoteItem {
        guard let objectID = item.objectID else { throw FilesError.notFound }
        let target = parentID ?? item.parentID
        guard target != RemoteItem.rootID else { throw FilesError.filesNeedAFolder }
        return try await writeFile(objectID: objectID, revision: item.revision, collectionID: try collection(of: target),
                                   name: davName(name ?? item.name), data: data,
                                   existingHash: item.blobID ?? item.contentHash, size: item.size, mime: mime ?? item.mime)
    }

    /// Renames and/or moves a folder.
    public func updateFolder(_ item: RemoteItem, name: String?, parentID: String?) async throws -> RemoteItem {
        guard let id = item.collectionID else { throw FilesError.notFound }
        let parent: String? = parentID.map { $0 == RemoteItem.rootID ? "" : String($0.dropFirst(2)) }
        return .folder(try await client.updateCollection(id: id, name: name.map(davName), parentID: parent))
    }

    public func delete(_ item: RemoteItem) async throws {
        switch item.kind {
        case .folder:
            try await client.deleteCollection(id: item.collectionID ?? "")
        case .file:
            let res = try await client.push([.delete(objectID: item.objectID ?? "", collectionID: try collection(of: item.parentID),
                                                     baseRevision: item.revision)])
            try check(res)
        }
    }

    /// The anchor for "now": enumeration starts here after listing everything.
    public func currentAnchor() async throws -> FilesAnchor {
        // Folders first: a folder change between the two calls then shows up
        // again in the next enumeration instead of being missed.
        let stamps = Self.stamps(try await folders())
        return FilesAnchor(seq: try await client.headSeq(), folders: stamps)
    }

    /// Changes since `anchor`: files from the journal, folders by comparing
    /// the folder list with the one in the anchor.
    public func changes(since anchor: FilesAnchor) async throws -> FilesChanges {
        let cols = try await folders()
        let page = try await client.changes(since: anchor.seq, limit: 500)
        if page.needReconcile == true { throw FilesError.anchorExpired }
        return Self.changes(previous: anchor, folders: cols, page: page)
    }

    /// The pure part of `changes(since:)`, separated for tests.
    static func changes(previous: FilesAnchor, folders cols: [SyncCollection], page: SyncChangesPage) -> FilesChanges {
        let now = Date()
        let stamps = stamps(cols)
        var updated: [RemoteItem] = []
        var deleted: [String] = []
        for col in cols where previous.folders[col.id] != stamps[col.id] {
            updated.append(.folder(col))
        }
        for id in previous.folders.keys where stamps[id] == nil {
            deleted.append(RemoteItem.folderID(id))
        }
        let writable = Dictionary(cols.map { ($0.id, $0.isWritable) }, uniquingKeysWith: { a, _ in a })
        var latest: [String: SyncChangeRecord] = [:]
        for ch in page.changes {
            // Only files in folders the device sees now or saw before.
            guard writable[ch.collectionId] != nil || previous.folders[ch.collectionId] != nil else { continue }
            guard ch.kind == "file" || ch.kind.isEmpty || ch.isDelete else { continue }
            latest[ch.objectId] = ch
        }
        for ch in latest.values {
            if ch.isDelete || writable[ch.collectionId] == nil {
                deleted.append(RemoteItem.fileID(ch.objectId))
            } else if ch.kind == "file" {
                updated.append(.file(ch, writable: writable[ch.collectionId] ?? false, at: now))
            }
        }
        let next = FilesAnchor(seq: max(previous.seq, page.nextSeq), folders: stamps)
        return FilesChanges(updated: updated, deleted: deleted, anchor: next, hasMore: page.hasMore)
    }

    static func stamps(_ cols: [SyncCollection]) -> [String: FilesAnchor.FolderStamp] {
        Dictionary(cols.map { c in
            (c.id, FilesAnchor.FolderStamp(revision: c.revision, parentID: c.parentId ?? "", name: c.name))
        }, uniquingKeysWith: { a, _ in a })
    }

    private func collection(of folderItemID: String) throws -> String {
        guard folderItemID.hasPrefix("c:") else { throw FilesError.notFound }
        return String(folderItemID.dropFirst(2))
    }

    private func writeFile(objectID: String, revision: UInt64?, collectionID: String, name: String, data: Data?,
                           existingHash: String?, size: Int64?, mime: String?) async throws -> RemoteItem {
        var hash = existingHash ?? ""
        var length = Int(size ?? 0)
        if let data {
            hash = try await client.putBlob(data).blobId
            length = data.count
        }
        var meta = ["name": name]
        if let mime, !mime.isEmpty { meta["mime"] = mime }
        let res = try await client.push([.put(objectID: objectID, collectionID: collectionID, kind: "file",
                                              baseRevision: revision, blob: hash, size: length, metadata: meta)])
        try check(res)
        return try await item(RemoteItem.fileID(objectID))
    }

    private func check(_ res: [SyncPushResult]) throws {
        guard let r = res.first else { throw NubiloError.invalidResponse }
        if r.status == "ok" { return }
        if r.error == "unauthorized" { throw FilesError.readOnly }
        if r.error == "collection not found" || r.error == "collection deleted" { throw FilesError.notFound }
        throw NubiloError.http(status: 409, message: r.error ?? r.status)
    }

    /// The server's DAV name rules: no slashes, control characters or empty names.
    func davName(_ s: String) -> String {
        var out = String(s.map { c -> Character in
            c == "/" || c == "\\" || (c.unicodeScalars.first.map { $0.value < 32 } ?? false) ? "_" : c
        })
        out = out.trimmingCharacters(in: CharacterSet(charactersIn: " "))
        return out.isEmpty || out == "." || out == ".." ? "Untitled" : out
    }
}
