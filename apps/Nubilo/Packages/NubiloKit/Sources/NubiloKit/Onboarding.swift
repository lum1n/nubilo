import Foundation

/// What a setup QR code or invite link carries.
public struct SetupLink: Equatable, Sendable {
    public var server: URL
    public var invite: String
    /// Server Ed25519 public key (base64url) to pin, when the link has one.
    public var serverKey: String?
    /// Username of the existing account the invite adds this device to; nil
    /// when the invite creates a new account (or the link does not say).
    public var account: String?

    public init(server: URL, invite: String, serverKey: String? = nil, account: String? = nil) {
        self.server = server
        self.invite = invite
        self.serverKey = serverKey
        self.account = account
    }

    /// Parses `nubilo://setup?url=…&invite=…&fp=…[&user=…]` or `https://<host>/join#<code>`.
    public init(parsing text: String) throws {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard let comps = URLComponents(string: trimmed), let scheme = comps.scheme?.lowercased() else {
            throw NubiloError.invalidLink("unreadable")
        }
        switch scheme {
        case "nubilo":
            let q = Dictionary((comps.queryItems ?? []).map { ($0.name, $0.value ?? "") }, uniquingKeysWith: { a, _ in a })
            guard let u = q["url"], let server = URL(string: u), server.scheme == "https" else {
                throw NubiloError.invalidLink("missing https server URL")
            }
            guard let inv = q["invite"], !inv.isEmpty else { throw NubiloError.invalidLink("missing invite") }
            self.init(server: server, invite: inv,
                      serverKey: q["fp"].flatMap { $0.isEmpty ? nil : $0 },
                      account: q["user"].flatMap { $0.isEmpty ? nil : $0 })
        case "https":
            guard comps.path == "/join", let code = comps.fragment, !code.isEmpty, let host = comps.host else {
                throw NubiloError.invalidLink("expected https://<server>/join#<code>")
            }
            var base = URLComponents()
            base.scheme = "https"
            base.host = host
            base.port = comps.port
            guard let server = base.url else { throw NubiloError.invalidLink("bad host") }
            self.init(server: server, invite: code)
        default:
            throw NubiloError.invalidLink("unsupported scheme \(scheme)")
        }
    }
}

/// A server this device is paired with. Persisted by the app; the private
/// key lives in the Keychain under `keyTag`.
public struct PairedServer: Codable, Equatable, Sendable {
    public var baseURL: URL
    public var deviceID: String
    public var userID: String
    public var username: String
    public var serverKey: String // base64 (raw std), pinned at pairing
    public var keyTag: String
    /// SHA-256 of the server's TLS leaf certificate, pinned on first use when
    /// the server is reached on a LAN address with a self-signed certificate.
    public var tlsPin: String?

    public init(baseURL: URL, deviceID: String, userID: String, username: String, serverKey: String, keyTag: String, tlsPin: String? = nil) {
        self.baseURL = baseURL
        self.deviceID = deviceID
        self.userID = userID
        self.username = username
        self.serverKey = serverKey
        self.keyTag = keyTag
        self.tlsPin = tlsPin
    }
}

extension PairedServer {
    /// This server with `baseURL` moved to the public address the server
    /// advertises, or nil when there is nothing to move to. The caller must
    /// check that a signed request through the new address answers for this
    /// device before saving it. The TLS pin is kept: it only applies to
    /// certificates the system does not trust (the LAN one).
    public func movingTo(publicURL text: String?) -> PairedServer? {
        guard let text, let url = URL(string: text.trimmingCharacters(in: .whitespaces)),
              url.scheme == "https", let host = url.host(), !host.isEmpty else { return nil }
        func key(_ u: URL) -> String {
            "\(u.host()?.lowercased() ?? ""):\(u.port ?? 443)"
        }
        guard key(url) != key(baseURL) else { return nil }
        var moved = self
        moved.baseURL = url
        return moved
    }
}

public enum Onboarding {
    /// Redeems an invite and completes pairing with `key`:
    /// `invites/redeem` → sign the challenge → `pair/complete`, checking the
    /// server identity against the link's fingerprint when present.
    public static func join(
        link: SetupLink,
        username: String,
        displayName: String,
        deviceName: String,
        key: DeviceSigningKey,
        keyTag: String,
        transport: HTTPTransport = URLSessionTransport()
    ) async throws -> PairedServer {
        let anon = NubiloClient(baseURL: link.server, signer: nil, transport: transport)
        struct Redeem: Encodable {
            var code, username, displayName, deviceName, publicKey: String
        }
        let r: RedeemResponse = try await anon.call("POST", "/api/v1/invites/redeem", json: Redeem(
            code: link.invite, username: username, displayName: displayName,
            deviceName: deviceName, publicKey: key.publicKeyRaw.base64RawStd
        ), auth: .none)
        if let fp = link.serverKey, !sameKey(fp, r.serverId) {
            throw NubiloError.serverKeyMismatch
        }
        guard let challenge = Data(base64Lenient: r.challenge) else { throw NubiloError.invalidResponse }
        let sig = try key.sign(challenge)
        let done: PairComplete = try await anon.call("POST", "/api/v1/pair/complete", json: [
            "pairing_id": r.pairingId, "signature": sig.base64RawStd,
        ], auth: .none)
        if !sameKey(done.serverPublicKey, r.serverId) {
            throw NubiloError.serverKeyMismatch
        }
        return PairedServer(
            baseURL: link.server, deviceID: done.deviceId, userID: done.userId ?? r.user.id,
            username: r.user.username, serverKey: done.serverPublicKey, keyTag: keyTag
        )
    }

    static func sameKey(_ a: String, _ b: String) -> Bool {
        guard let x = Data(base64Lenient: a), let y = Data(base64Lenient: b) else { return false }
        return x == y
    }
}
