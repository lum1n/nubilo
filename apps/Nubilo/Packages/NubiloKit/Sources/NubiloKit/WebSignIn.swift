import Foundation

/// What the sign-in QR code on the web app carries:
/// `nubilo://web-login?url=<server>&code=ABCD-EFGH`. A bare code typed by
/// hand ("abcd efgh", "ABCDEFGH") parses too, with no server.
public struct WebSignInLink: Equatable, Sendable {
    public var server: URL?
    /// Normalized "ABCD-EFGH".
    public var code: String

    /// Characters the server uses for codes (no 0/O, 1/I/L).
    static let alphabet = Set("ABCDEFGHJKMNPQRSTUVWXYZ23456789")

    public init?(parsing text: String) {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        if let comps = URLComponents(string: trimmed), comps.scheme?.lowercased() == "nubilo" {
            guard comps.host?.lowercased() == "web-login" else { return nil }
            let q = Dictionary((comps.queryItems ?? []).map { ($0.name, $0.value ?? "") }, uniquingKeysWith: { a, _ in a })
            guard let code = q["code"].flatMap(Self.normalize) else { return nil }
            let server = q["url"].flatMap(URL.init(string:))
            guard server == nil || server?.scheme == "https" else { return nil }
            self.server = server
            self.code = code
            return
        }
        guard let code = Self.normalize(trimmed) else { return nil }
        self.server = nil
        self.code = code
    }

    static func normalize(_ raw: String) -> String? {
        let chars = raw.uppercased().filter { $0 != "-" && $0 != " " }
        guard chars.count == 8, chars.allSatisfy(alphabet.contains) else { return nil }
        return String(chars.prefix(4)) + "-" + String(chars.suffix(4))
    }
}

/// A browser waiting for this user to approve its sign-in.
public struct WebSignInRequest: Codable, Sendable, Hashable {
    public var code: String
    public var userAgent: String
    public var ip: String
    public var createdAt: Int64
    public var expiresAt: Int64
}

/// A signed-in browser.
public struct WebSession: Codable, Sendable, Identifiable, Hashable {
    public var id: String
    public var deviceId: String
    public var userAgent: String
    public var ip: String
    public var createdAt: Int64
    public var lastSeen: Int64
    public var expiresAt: Int64
}

extension NubiloClient {
    /// Where the browser waiting on `code` is; 404 when unknown or expired.
    public func webSignIn(code: String) async throws -> WebSignInRequest {
        try await call("GET", "/api/v1/me/web-login?code=\(code.queryEscaped)")
    }

    public func approveWebSignIn(code: String) async throws {
        _ = try await raw("POST", "/api/v1/me/web-login", body: try Self.encoder.encode(["code": code]), contentType: "application/json")
    }

    public func webSessions() async throws -> [WebSession] {
        try await call("GET", "/api/v1/me/web-sessions", as: Wrapped<[WebSession]>.self).value("sessions")
    }

    public func endWebSession(_ id: String) async throws {
        _ = try await raw("DELETE", "/api/v1/me/web-sessions/\(id)")
    }
}

/// A short, readable name for a browser from its User-Agent.
public func describeBrowser(_ userAgent: String) -> String {
    let ua = userAgent
    let browser: String
    if ua.contains("Edg/") { browser = "Edge" }
    else if ua.contains("Firefox/") { browser = "Firefox" }
    else if ua.contains("Chrome/") { browser = "Chrome" }
    else if ua.contains("Safari/") { browser = "Safari" }
    else { browser = "A browser" }
    let system: String?
    if ua.contains("iPhone") { system = "iPhone" }
    else if ua.contains("iPad") { system = "iPad" }
    else if ua.contains("Android") { system = "Android" }
    else if ua.contains("Mac OS X") || ua.contains("Macintosh") { system = "Mac" }
    else if ua.contains("Windows") { system = "Windows" }
    else if ua.contains("Linux") { system = "Linux" }
    else { system = nil }
    return system.map { "\(browser) on \($0)" } ?? browser
}
