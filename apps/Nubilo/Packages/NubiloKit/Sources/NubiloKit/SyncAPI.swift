import Foundation
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif

// The parts of the sync protocol (docs/API.md "Sync") the app writes with:
// list and create collections, push object changes. Blobs go through
// NubiloClient.putBlob.

/// A collection as `/sync/v1/collections` lists it.
public struct SyncCollection: Decodable, Sendable, Hashable {
    public var id: String
    public var kind: String
    public var name: String
    public var parentId: String?
    public var deletedAt: Int64?
    public var revision: UInt64
    public var updatedAt: Int64
    /// The caller's access: "owner", "rw" or "ro" (empty from older servers).
    public var access: String?
    /// The owner's username on shared collections.
    public var owner: String?
    /// Calendar metadata; nil when absent or not a calendar's shape.
    public var calendar: CalendarMetadata?

    public struct CalendarMetadata: Decodable, Sendable, Hashable {
        public var color: String?
        public var mirror: Bool?
        public var mirrorDevice: String?
    }

    enum CodingKeys: String, CodingKey {
        case id, kind, name, parentId, deletedAt, revision, updatedAt, access, owner, metadata
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        kind = try c.decode(String.self, forKey: .kind)
        name = try c.decode(String.self, forKey: .name)
        parentId = try c.decodeIfPresent(String.self, forKey: .parentId)
        deletedAt = try c.decodeIfPresent(Int64.self, forKey: .deletedAt)
        revision = try c.decodeIfPresent(UInt64.self, forKey: .revision) ?? 0
        updatedAt = try c.decodeIfPresent(Int64.self, forKey: .updatedAt) ?? 0
        access = try c.decodeIfPresent(String.self, forKey: .access)
        owner = try c.decodeIfPresent(String.self, forKey: .owner)
        // Other kinds carry other metadata; never fail the listing over it.
        calendar = try? c.decodeIfPresent(CalendarMetadata.self, forKey: .metadata)
    }

    /// True for the caller's own, live, top-level collections.
    public var isOwnRoot: Bool {
        deletedAt == nil && (parentId ?? "").isEmpty && isOwn
    }

    public var isOwn: Bool { access == nil || access == "" || access == "owner" }
    public var isWritable: Bool { isOwn || access == "rw" }
}

/// One object change for `/sync/v1/push`.
public struct SyncChange: Encodable, Sendable {
    public var objectId: String
    public var collectionId: String
    public var kind: String
    public var op: String
    public var baseRevision: UInt64
    public var contentHash: String
    public var metadata: [String: String]?
    public var blobId: String
    public var size: Int64
    public var force: Bool

    public static func put(objectID: String, collectionID: String, kind: String, baseRevision: UInt64?,
                           blob hash: String, size: Int, metadata: [String: String]) -> SyncChange {
        SyncChange(objectId: objectID, collectionId: collectionID, kind: kind,
                   op: baseRevision == nil ? "create" : "update", baseRevision: baseRevision ?? 0,
                   contentHash: hash, metadata: metadata, blobId: hash, size: Int64(size), force: true)
    }

    public static func delete(objectID: String, collectionID: String, baseRevision: UInt64) -> SyncChange {
        SyncChange(objectId: objectID, collectionId: collectionID, kind: "", op: "delete", baseRevision: baseRevision,
                   contentHash: "", metadata: nil, blobId: "", size: 0, force: true)
    }
}

public struct SyncPushResult: Decodable, Sendable {
    public var objectId: String
    /// "ok", "conflict" or "error".
    public var status: String
    public var revision: UInt64?
    public var error: String?
}

extension NubiloClient {
    /// Collections the device can see (own and shared).
    public func syncCollections() async throws -> [SyncCollection] {
        struct Wrap: Decodable { var collections: [SyncCollection]? }
        return try await call("POST", "/sync/v1/collections", json: [String: String](), as: Wrap.self).collections ?? []
    }

    /// Finds or creates the caller's top-level collection `name` of `kind` and
    /// merges `metadata` into it. Turning `mirror` on makes this device the
    /// only one that may write to it (the server records it).
    public func ensureCollection(kind: String, name: String, parentID: String? = nil, metadata: [String: SyncJSON]? = nil) async throws -> SyncCollection {
        struct Req: Encodable {
            var kind: String
            var name: String
            var parentId: String?
            var metadata: [String: SyncJSON]?
        }
        return try await call("POST", "/sync/v1/collection", json: Req(kind: kind, name: name, parentId: parentID, metadata: metadata))
    }

    /// The journal position now (`/sync/v1/hello`).
    public func headSeq() async throws -> Int64 {
        struct Req: Encodable {
            var protocolMin = 1
            var protocolMax = 1
            var cursor: Int64 = 0
            var restoreHint = false
        }
        struct Res: Decodable { var headSeq: Int64 }
        return try await call("POST", "/sync/v1/hello", json: Req(), as: Res.self).headSeq
    }

    /// Live objects of one collection.
    public func objects(collectionID: String) async throws -> [SyncObject] {
        struct Req: Encodable {
            var collectionId: String
            var afterId: String
        }
        struct Res: Decodable {
            var objects: [SyncObject]? // null from older servers for empty folders
            var nextAfterId: String?
        }
        var out: [SyncObject] = []
        var after = ""
        while true {
            let page = try await call("POST", "/sync/v1/objects", json: Req(collectionId: collectionID, afterId: after), as: Res.self)
            out += page.objects ?? []
            guard let next = page.nextAfterId, !next.isEmpty, next != after else { return out }
            after = next
        }
    }

    public func object(id: String) async throws -> SyncObject {
        try await call("POST", "/sync/v1/object", json: ["id": id])
    }

    /// Renames and/or moves a collection; `parentID` "" makes it top-level, nil keeps it.
    public func updateCollection(id: String, name: String? = nil, parentID: String? = nil) async throws -> SyncCollection {
        struct Req: Encodable {
            var id: String
            var name: String?
            var parentId: String?
        }
        return try await call("POST", "/sync/v1/collection/update", json: Req(id: id, name: name, parentId: parentID))
    }

    /// Deletes a collection with its subfolders and objects.
    public func deleteCollection(id: String) async throws {
        try await raw("POST", "/sync/v1/collection/delete", body: try Self.encoder.encode(["id": id]), contentType: "application/json")
    }

    /// A blob's plaintext.
    public func blob(_ hash: String) async throws -> Data {
        try await raw("GET", Self.blobPath(hash))
    }

    /// Pushes up to 500 changes in one transaction.
    public func push(_ changes: [SyncChange]) async throws -> [SyncPushResult] {
        struct Req: Encodable {
            var idempotencyKey: String
            var changes: [SyncChange]
        }
        struct Res: Decodable { var results: [SyncPushResult] }
        return try await call("POST", "/sync/v1/push", json: Req(idempotencyKey: ULID.make(), changes: changes), as: Res.self).results
    }
}

/// One journal entry from `/sync/v1/changes`.
public struct SyncChangeRecord: Decodable, Sendable {
    public var seq: Int64
    public var objectId: String
    public var collectionId: String
    public var kind: String
    public var op: String
    public var revision: UInt64
    public var contentHash: String
    public var blobId: String?
    public var size: Int64
    public var deleted: Bool?
    /// The object's `uid` metadata (calendar events), if any.
    public var uid: String?
    /// The object's `name` and `mime` metadata (files), if any.
    public var name: String?
    public var mime: String?

    enum CodingKeys: String, CodingKey {
        case seq, objectId, collectionId, kind, op, revision, contentHash, blobId, size, deleted, metadata
    }

    struct Meta: Decodable {
        var uid: String?
        var name: String?
        var mime: String?
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        seq = try c.decode(Int64.self, forKey: .seq)
        objectId = try c.decode(String.self, forKey: .objectId)
        collectionId = try c.decodeIfPresent(String.self, forKey: .collectionId) ?? ""
        kind = try c.decodeIfPresent(String.self, forKey: .kind) ?? ""
        op = try c.decode(String.self, forKey: .op)
        revision = try c.decodeIfPresent(UInt64.self, forKey: .revision) ?? 0
        contentHash = try c.decodeIfPresent(String.self, forKey: .contentHash) ?? ""
        blobId = try c.decodeIfPresent(String.self, forKey: .blobId)
        size = try c.decodeIfPresent(Int64.self, forKey: .size) ?? 0
        deleted = try c.decodeIfPresent(Bool.self, forKey: .deleted)
        let meta = (try? c.decodeIfPresent(Meta.self, forKey: .metadata)) ?? nil
        uid = meta?.uid
        name = meta?.name
        mime = meta?.mime
    }

    public var isDelete: Bool { op == "delete" || deleted == true }
}

public struct SyncChangesPage: Decodable, Sendable {
    public var changes: [SyncChangeRecord]
    public var nextSeq: Int64
    public var hasMore: Bool
    /// The cursor is past the server's journal (a restored server): start over.
    public var needReconcile: Bool?

    enum CodingKeys: String, CodingKey {
        case changes, nextSeq, hasMore, needReconcile
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        // The server sends null, not [], when there is nothing new.
        changes = try c.decodeIfPresent([SyncChangeRecord].self, forKey: .changes) ?? []
        nextSeq = try c.decodeIfPresent(Int64.self, forKey: .nextSeq) ?? 0
        hasMore = try c.decodeIfPresent(Bool.self, forKey: .hasMore) ?? false
        needReconcile = try c.decodeIfPresent(Bool.self, forKey: .needReconcile)
    }
}

/// A live object as `/sync/v1/objects` and `/sync/v1/object` return it.
public struct SyncObject: Decodable, Sendable, Hashable {
    public var id: String
    public var collectionId: String
    public var kind: String
    public var revision: UInt64
    public var contentHash: String
    public var blobId: String?
    public var size: Int64
    public var updatedAt: Int64
    public var name: String?
    public var mime: String?

    enum CodingKeys: String, CodingKey {
        case id, collectionId, kind, revision, contentHash, blobId, size, updatedAt, metadata
    }

    struct Meta: Decodable {
        var name: String?
        var mime: String?
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        collectionId = try c.decode(String.self, forKey: .collectionId)
        kind = try c.decodeIfPresent(String.self, forKey: .kind) ?? ""
        revision = try c.decodeIfPresent(UInt64.self, forKey: .revision) ?? 0
        contentHash = try c.decodeIfPresent(String.self, forKey: .contentHash) ?? ""
        blobId = try c.decodeIfPresent(String.self, forKey: .blobId)
        size = try c.decodeIfPresent(Int64.self, forKey: .size) ?? 0
        updatedAt = try c.decodeIfPresent(Int64.self, forKey: .updatedAt) ?? 0
        let meta = (try? c.decodeIfPresent(Meta.self, forKey: .metadata)) ?? nil
        name = meta?.name
        mime = meta?.mime
    }
}

extension NubiloClient {
    /// Journal entries after `since`, optionally for one collection.
    public func changes(since: Int64, limit: Int = 500, collectionID: String? = nil) async throws -> SyncChangesPage {
        struct Req: Encodable {
            var sinceSeq: Int64
            var limit: Int
            var collectionId: String?
        }
        return try await call("POST", "/sync/v1/changes", json: Req(sinceSeq: since, limit: limit, collectionId: collectionID))
    }

    /// The live objects of one collection, replayed from its journal.
    public func liveObjects(collectionID: String) async throws -> [String: SyncChangeRecord] {
        var live: [String: SyncChangeRecord] = [:]
        var since: Int64 = 0
        while true {
            let page = try await changes(since: since, collectionID: collectionID)
            for ch in page.changes {
                if ch.isDelete { live[ch.objectId] = nil } else { live[ch.objectId] = ch }
            }
            guard page.hasMore, page.nextSeq > since else { return live }
            since = page.nextSeq
        }
    }
}

/// A JSON scalar for collection metadata patches.
public enum SyncJSON: Encodable, Sendable, Hashable {
    case string(String)
    case bool(Bool)

    public func encode(to encoder: Encoder) throws {
        var c = encoder.singleValueContainer()
        switch self {
        case let .string(s): try c.encode(s)
        case let .bool(b): try c.encode(b)
        }
    }
}

/// ULIDs, the server's object and collection id format: 48-bit millisecond
/// time and 80 random bits in Crockford base32.
public enum ULID {
    private static let alphabet = Array("0123456789ABCDEFGHJKMNPQRSTVWXYZ")

    public static func make(date: Date = Date()) -> String {
        var rng = SystemRandomNumberGenerator()
        return make(date: date, using: &rng)
    }

    static func make<G: RandomNumberGenerator>(date: Date, using rng: inout G) -> String {
        var ms = UInt64(max(0, date.timeIntervalSince1970 * 1000))
        var time = [Character](repeating: "0", count: 10)
        for i in stride(from: 9, through: 0, by: -1) {
            time[i] = alphabet[Int(ms & 31)]
            ms >>= 5
        }
        // 80 random bits = 16 characters of 5 bits.
        var random = ""
        random.reserveCapacity(16)
        for _ in 0..<16 {
            random.append(alphabet[Int(rng.next() & 31)])
        }
        return String(time) + random
    }
}
