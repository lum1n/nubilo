import Foundation
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif

public enum NubiloError: Error, LocalizedError, Equatable {
    case http(status: Int, message: String)
    case invalidResponse
    case invalidLink(String)
    case serverKeyMismatch
    case notPaired

    public var errorDescription: String? {
        switch self {
        case let .http(status, message):
            return message.isEmpty ? "Server returned HTTP \(status)" : "\(message) (HTTP \(status))"
        case .invalidResponse: return "The server sent an unexpected response."
        case let .invalidLink(why): return "This is not a Nubilo setup link: \(why)"
        case .serverKeyMismatch: return "The server's identity does not match the setup code. Do not continue on this network."
        case .notPaired: return "This device is not set up yet."
        }
    }

    public var status: Int? {
        if case let .http(s, _) = self { return s }
        return nil
    }
}

/// Sends HTTP requests. URLSession in the app; a fake in tests.
public protocol HTTPTransport: Sendable {
    func send(_ request: URLRequest) async throws -> (Data, HTTPURLResponse)
}

public struct URLSessionTransport: HTTPTransport {
    public let session: URLSession

    public init(session: URLSession = .shared) { self.session = session }

    public func send(_ request: URLRequest) async throws -> (Data, HTTPURLResponse) {
        try await withCheckedThrowingContinuation { cont in
            session.dataTask(with: request) { data, resp, err in
                if let err { return cont.resume(throwing: err) }
                guard let http = resp as? HTTPURLResponse else { return cont.resume(throwing: NubiloError.invalidResponse) }
                cont.resume(returning: (data ?? Data(), http))
            }.resume()
        }
    }
}

/// How a request authenticates.
public enum RequestAuth: Sendable {
    case none
    case signed
    case bearer(String)
}

/// A client for one paired server (docs/API.md).
public final class NubiloClient: Sendable {
    public let baseURL: URL
    public let signer: RequestSigner?
    public let transport: HTTPTransport

    public init(baseURL: URL, signer: RequestSigner?, transport: HTTPTransport = URLSessionTransport()) {
        self.baseURL = baseURL
        self.signer = signer
        self.transport = transport
    }

    static let decoder: JSONDecoder = {
        let d = JSONDecoder()
        d.keyDecodingStrategy = .convertFromSnakeCase
        return d
    }()

    static let encoder: JSONEncoder = {
        let e = JSONEncoder()
        e.keyEncodingStrategy = .convertToSnakeCase
        e.outputFormatting = [.sortedKeys]
        return e
    }()

    /// Builds a request. `path` includes any query and is signed verbatim.
    public func request(_ method: String, _ path: String, body: Data = Data(), contentType: String? = nil, auth: RequestAuth = .signed) throws -> URLRequest {
        guard let url = URL(string: path, relativeTo: baseURL)?.absoluteURL else { throw NubiloError.invalidResponse }
        var req = URLRequest(url: url)
        req.httpMethod = method
        if !body.isEmpty || method == "POST" || method == "PUT" {
            req.httpBody = body
        }
        if let contentType { req.setValue(contentType, forHTTPHeaderField: "Content-Type") }
        switch auth {
        case .none:
            break
        case .signed:
            guard let signer else { throw NubiloError.notPaired }
            req.setValue(try signer.authorization(method: method, path: path, body: body), forHTTPHeaderField: "Authorization")
        case let .bearer(token):
            req.setValue("Bearer " + token, forHTTPHeaderField: "Authorization")
        }
        return req
    }

    @discardableResult
    public func raw(_ method: String, _ path: String, body: Data = Data(), contentType: String? = nil, auth: RequestAuth = .signed) async throws -> Data {
        let req = try request(method, path, body: body, contentType: contentType, auth: auth)
        let (data, resp) = try await transport.send(req)
        guard (200..<300).contains(resp.statusCode) else {
            let msg = String(decoding: data, as: UTF8.self).trimmingCharacters(in: .whitespacesAndNewlines)
            throw NubiloError.http(status: resp.statusCode, message: msg)
        }
        return data
    }

    public func call<T: Decodable>(_ method: String, _ path: String, json: (any Encodable)? = nil, auth: RequestAuth = .signed, as: T.Type = T.self) async throws -> T {
        var body = Data()
        if let json { body = try Self.encoder.encode(json) }
        let data = try await raw(method, path, body: body, contentType: json == nil ? nil : "application/json", auth: auth)
        return try Self.decoder.decode(T.self, from: data)
    }

    // MARK: Account

    public func status() async throws -> ServerStatus { try await call("GET", "/api/v1/status") }
    public func me() async throws -> Me { try await call("GET", "/api/v1/me") }
    public func devices(all: Bool = false) async throws -> [DeviceInfo] { try await call("GET", all ? "/api/v1/devices?all=1" : "/api/v1/devices") }
    public func revokeDevice(_ id: String) async throws { try await raw("POST", "/api/v1/devices/\(id)/revoke") }
    public func userDirectory() async throws -> [User] { try await call("GET", "/api/v1/users", as: Wrapped<[User]>.self).value("users") }

    public func appPasswords() async throws -> [AppPassword] {
        try await call("GET", "/api/v1/me/passwords", as: Wrapped<[AppPassword]>.self).value("passwords")
    }

    public func createAppPassword(label: String, scope: String) async throws -> NewAppPassword {
        try await call("POST", "/api/v1/me/passwords", json: ["label": label, "scope": scope])
    }

    public func revokeAppPassword(_ id: String) async throws { try await raw("DELETE", "/api/v1/me/passwords/\(id)") }

    /// A single-use URL of a .mobileconfig that adds CalDAV/CardDAV accounts.
    /// Open it in Safari (iOS only installs profiles downloaded there).
    public func createProfile(services: [String] = ["caldav", "carddav"]) async throws -> ProfileLink {
        try await call("POST", "/api/v1/me/profile", json: ["services": services])
    }

    public func uploadToken() async throws -> UploadToken { try await call("POST", "/api/v1/me/upload-token") }

    /// A single-use invite (1 hour) that adds another device to the signed-in user's account.
    public func createDeviceInvite() async throws -> NewInvite { try await call("POST", "/api/v1/me/device-invite") }

    // MARK: Collections and shares

    public func collections() async throws -> [CollectionInfo] {
        try await call("GET", "/api/v1/collections", as: Wrapped<[CollectionInfo]>.self).value("collections")
    }

    public func shares(collection id: String) async throws -> [Share] {
        try await call("GET", "/api/v1/collections/\(id)/shares", as: Wrapped<[Share]>.self).value("shares")
    }

    @discardableResult
    public func share(collection id: String, username: String, access: String) async throws -> [Share] {
        try await call("PUT", "/api/v1/collections/\(id)/shares", json: ["username": username, "access": access], as: Wrapped<[Share]>.self).value("shares")
    }

    @discardableResult
    public func unshare(collection id: String, username: String) async throws -> [Share] {
        try await call("DELETE", "/api/v1/collections/\(id)/shares?username=\(username.queryEscaped)", as: Wrapped<[Share]>.self).value("shares")
    }

    // MARK: Photos

    public func photos(after: String? = nil, limit: Int = 500) async throws -> PhotoPage {
        var path = "/api/v1/photos?limit=\(limit)"
        if let after, !after.isEmpty { path += "&after=\(after.queryEscaped)" }
        return try await call("GET", path)
    }

    /// Bytes of a rendition ("thumb", "preview", "original").
    public func photoData(_ id: String, rendition: String) async throws -> Data {
        try await raw("GET", "/api/v1/photos/\(id)/\(rendition)")
    }

    public func missingPhotos(hashes: [String], auth: RequestAuth = .signed) async throws -> [String] {
        try await call("POST", "/api/v1/photos/check", json: ["hashes": hashes], auth: auth, as: Wrapped<[String]>.self).value("missing")
    }

    /// Path to PUT a blob to (for background uploads with an upload token).
    public static func blobPath(_ hash: String) -> String { "/sync/v1/blob/\(hash)" }

    @discardableResult
    public func putBlob(_ data: Data, auth: RequestAuth = .signed) async throws -> BlobPut {
        let hash = sha256Hex(data)
        let out = try await raw("PUT", Self.blobPath(hash), body: data, contentType: "application/octet-stream", auth: auth)
        return try Self.decoder.decode(BlobPut.self, from: out)
    }

    public struct Commit: Encodable, Sendable {
        public var blobId: String
        public var name: String
        public var takenAt: Int64?
        public var localId: String?
        public var liveVideoBlob: String?

        public init(blobId: String, name: String, takenAt: Int64? = nil, localId: String? = nil, liveVideoBlob: String? = nil) {
            self.blobId = blobId
            self.name = name
            self.takenAt = takenAt
            self.localId = localId
            self.liveVideoBlob = liveVideoBlob
        }
    }

    public func commitPhoto(_ c: Commit, auth: RequestAuth = .signed) async throws -> Photo {
        try await call("POST", "/api/v1/photos/commit", json: c, auth: auth)
    }

    public func albums() async throws -> [Album] { try await call("GET", "/api/v1/albums", as: Wrapped<[Album]>.self).value("albums") }

    public func createAlbum(name: String) async throws -> Album { try await call("POST", "/api/v1/albums", json: ["name": name]) }

    public func albumItems(_ id: String, after: String? = nil, limit: Int = 500) async throws -> AlbumItemPage {
        var path = "/api/v1/albums/\(id)/items?limit=\(limit)"
        if let after, !after.isEmpty { path += "&after=\(after.queryEscaped)" }
        return try await call("GET", path)
    }

    @discardableResult
    public func addToAlbum(_ id: String, photoIDs: [String]) async throws -> [Photo] {
        try await call("POST", "/api/v1/albums/\(id)/items", json: ["photo_ids": photoIDs], as: Wrapped<[Photo]>.self).value("items")
    }

    // MARK: Admin

    public func adminUsers() async throws -> [AdminUser] {
        try await call("GET", "/api/v1/admin/users", as: Wrapped<[AdminUser]>.self).value("users")
    }

    public func setUser(_ id: String, role: String? = nil, status: String? = nil) async throws -> User {
        var body: [String: String] = [:]
        if let role { body["role"] = role }
        if let status { body["status"] = status }
        return try await call("PATCH", "/api/v1/admin/users/\(id)", json: body)
    }

    /// Sets a user's storage limit; 0 removes it.
    public func setQuota(_ id: String, bytes: Int64) async throws -> User {
        try await call("PATCH", "/api/v1/admin/users/\(id)", json: ["quota_bytes": bytes])
    }

    public func invites() async throws -> [Invite] {
        try await call("GET", "/api/v1/admin/invites", as: Wrapped<[Invite]>.self).value("invites")
    }

    public struct InviteRequest: Encodable, Sendable {
        public var role: String
        public var userId: String?
        public var ttlHours: Int?
        public var maxUses: Int?

        public init(role: String = "member", userId: String? = nil, ttlHours: Int? = nil, maxUses: Int? = nil) {
            self.role = role
            self.userId = userId
            self.ttlHours = ttlHours
            self.maxUses = maxUses
        }
    }

    public func createInvite(_ r: InviteRequest) async throws -> NewInvite { try await call("POST", "/api/v1/admin/invites", json: r) }

    public func revokeInvite(_ id: String) async throws { try await raw("DELETE", "/api/v1/admin/invites/\(id)") }

    public func adminRevokeDevice(_ id: String) async throws { try await raw("POST", "/api/v1/admin/devices/\(id)/revoke") }
}

/// Decodes `{"<key>": value}` envelopes.
public struct Wrapped<T: Decodable>: Decodable {
    let values: [String: T]

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: AnyKey.self)
        var out: [String: T] = [:]
        for k in c.allKeys {
            if let v = try? c.decode(T.self, forKey: k) { out[k.stringValue] = v }
        }
        values = out
    }

    func value(_ key: String) throws -> T {
        // Keys arrive converted from snake_case; single-word keys are unchanged.
        guard let v = values[key] ?? values.values.first else { throw NubiloError.invalidResponse }
        return v
    }
}

struct AnyKey: CodingKey {
    var stringValue: String
    var intValue: Int?
    init(stringValue: String) { self.stringValue = stringValue }
    init?(intValue: Int) { stringValue = String(intValue); self.intValue = intValue }
}

extension String {
    var queryEscaped: String {
        var allowed = CharacterSet.urlQueryAllowed
        allowed.remove(charactersIn: "&=+?#")
        return addingPercentEncoding(withAllowedCharacters: allowed) ?? self
    }
}
