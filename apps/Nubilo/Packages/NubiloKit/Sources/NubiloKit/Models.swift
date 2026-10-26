import Foundation

// Wire models for docs/API.md. Decoded with `.convertFromSnakeCase`.

public struct ServerStatus: Codable, Sendable {
    public var ok: Bool
    public var apiVersion: Int?
    public var serverName: String?
    public var domain: String?
    public var features: [String]?
}

public struct User: Codable, Sendable, Identifiable, Hashable {
    public var id: String
    public var username: String
    public var displayName: String
    public var role: String?
    public var status: String?
    public var createdAt: Int64?

    public var isAdmin: Bool { role == "admin" }
}

public struct DeviceInfo: Codable, Sendable, Identifiable, Hashable {
    public var id: String
    public var name: String
    public var role: String
    public var userId: String?
    public var createdAt: Int64
    public var lastSeen: Int64?
    public var revoked: Bool
}

public struct Storage: Codable, Sendable, Hashable {
    public var objects: Int
    public var bytes: Int64
}

public struct Me: Codable, Sendable {
    public var user: User
    public var device: DeviceInfo
    public var storage: Storage
    public var urls: [String: String]
}

public struct AppPassword: Codable, Sendable, Identifiable, Hashable {
    public var id: String
    public var label: String
    public var scope: String
    public var createdAt: Int64
    public var lastUsed: Int64?
}

public struct NewAppPassword: Codable, Sendable {
    public var id: String
    public var username: String
    public var password: String
}

public struct ProfileLink: Codable, Sendable {
    public var url: String
    public var expiresAt: Int64
}

public struct UploadToken: Codable, Sendable {
    public var token: String
    public var expiresAt: Int64
}

public struct Share: Codable, Sendable, Hashable {
    public var collectionId: String
    public var principalType: String
    public var principalId: String
    public var username: String?
    public var access: String
}

public struct CollectionInfo: Codable, Sendable, Identifiable, Hashable {
    public var id: String
    public var kind: String
    public var name: String
    public var parentId: String?
    public var owner: String?
    public var ownerUserId: String?
    public var access: String?
    public var shares: [Share]?

    public var isOwned: Bool { access == "owner" }
}

public struct Photo: Codable, Sendable, Identifiable, Hashable {
    public var id: String
    public var name: String?
    public var mime: String?
    public var kind: String?
    public var width: Int?
    public var height: Int?
    public var takenAtMs: Int64?
    public var checksum: String?
    public var thumbHash: String?
    public var previewHash: String?
    public var livePairHash: String?
    public var collectionId: String?
    public var photoId: String?
    public var size: Int64?

    public var takenAt: Date? { takenAtMs.map { Date(timeIntervalSince1970: Double($0) / 1000) } }
}

public struct PhotoPage: Codable, Sendable {
    public var photos: [Photo]
    public var next: String?
}

public struct AlbumItemPage: Codable, Sendable {
    public var items: [Photo]
    public var next: String?
}

public struct Album: Codable, Sendable, Identifiable, Hashable {
    public var id: String
    public var name: String
    public var owner: String
    public var access: String
    public var createdAt: Int64
    public var updatedAt: Int64

    public var isOwned: Bool { access == "owner" }
}

public struct AdminUser: Codable, Sendable, Identifiable, Hashable {
    public var id: String
    public var username: String
    public var displayName: String
    public var role: String
    public var status: String
    public var createdAt: Int64
    public var devices: Int
    public var objects: Int
    public var bytes: Int64
}

public struct Invite: Codable, Sendable, Identifiable, Hashable {
    public var id: String
    public var role: String
    public var userId: String?
    public var maxUses: Int
    public var uses: Int?
    public var expiresAt: Int64
}

public struct NewInvite: Codable, Sendable {
    public var id: String
    public var code: String
    public var url: String
    public var setupUrl: String
    public var expiresAt: Int64
    public var role: String
}

public struct RedeemResponse: Codable, Sendable {
    public var pairingId: String
    public var challenge: String
    public var serverId: String
    public var user: User
}

public struct PairComplete: Codable, Sendable {
    public var deviceId: String
    public var userId: String?
    public var createdAt: Int64
    public var serverPublicKey: String
}

public struct BlobPut: Codable, Sendable {
    public var blobId: String
    public var size: Int64
}
