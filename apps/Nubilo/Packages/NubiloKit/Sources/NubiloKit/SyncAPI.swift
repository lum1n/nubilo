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
    /// The caller's access: "owner", "rw" or "ro" (empty from older servers).
    public var access: String?
    /// Calendar metadata; nil when absent or not a calendar's shape.
    public var calendar: CalendarMetadata?

    public struct CalendarMetadata: Decodable, Sendable, Hashable {
        public var color: String?
        public var mirror: Bool?
        public var mirrorDevice: String?
    }

    enum CodingKeys: String, CodingKey {
        case id, kind, name, parentId, deletedAt, access, metadata
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        kind = try c.decode(String.self, forKey: .kind)
        name = try c.decode(String.self, forKey: .name)
        parentId = try c.decodeIfPresent(String.self, forKey: .parentId)
        deletedAt = try c.decodeIfPresent(Int64.self, forKey: .deletedAt)
        access = try c.decodeIfPresent(String.self, forKey: .access)
        // Other kinds carry other metadata; never fail the listing over it.
        calendar = try? c.decodeIfPresent(CalendarMetadata.self, forKey: .metadata)
    }

    /// True for the caller's own, live, top-level collections.
    public var isOwnRoot: Bool {
        deletedAt == nil && (parentId ?? "").isEmpty && (access == nil || access == "" || access == "owner")
    }
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
        struct Wrap: Decodable { var collections: [SyncCollection] }
        return try await call("POST", "/sync/v1/collections", json: [String: String](), as: Wrap.self).collections
    }

    /// Finds or creates the caller's top-level collection `name` of `kind` and
    /// merges `metadata` into it. Turning `mirror` on makes this device the
    /// only one that may write to it (the server records it).
    public func ensureCollection(kind: String, name: String, metadata: [String: SyncJSON]? = nil) async throws -> SyncCollection {
        struct Req: Encodable {
            var kind: String
            var name: String
            var metadata: [String: SyncJSON]?
        }
        return try await call("POST", "/sync/v1/collection", json: Req(kind: kind, name: name, metadata: metadata))
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
    public var op: String
    public var revision: UInt64
    public var contentHash: String
    public var deleted: Bool?
    /// The object's `uid` metadata (calendar events), if any.
    public var uid: String?

    enum CodingKeys: String, CodingKey {
        case seq, objectId, op, revision, contentHash, deleted, metadata
    }

    struct Meta: Decodable { var uid: String? }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        seq = try c.decode(Int64.self, forKey: .seq)
        objectId = try c.decode(String.self, forKey: .objectId)
        op = try c.decode(String.self, forKey: .op)
        revision = try c.decodeIfPresent(UInt64.self, forKey: .revision) ?? 0
        contentHash = try c.decodeIfPresent(String.self, forKey: .contentHash) ?? ""
        deleted = try c.decodeIfPresent(Bool.self, forKey: .deleted)
        uid = (try? c.decodeIfPresent(Meta.self, forKey: .metadata))??.uid
    }

    public var isDelete: Bool { op == "delete" || deleted == true }
}

public struct SyncChangesPage: Decodable, Sendable {
    public var changes: [SyncChangeRecord]
    public var nextSeq: Int64
    public var hasMore: Bool
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
