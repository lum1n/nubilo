import Foundation
#if canImport(CryptoKit)
import CryptoKit
#else
import Crypto
#endif

/// A device's Ed25519 signing key. The private key never leaves the key store.
public protocol DeviceSigningKey: Sendable {
    var publicKeyRaw: Data { get }
    func sign(_ message: Data) throws -> Data
}

/// An in-memory Ed25519 key (CryptoKit on Apple platforms, swift-crypto elsewhere).
public struct Ed25519Key: DeviceSigningKey {
    public let privateKey: Curve25519.Signing.PrivateKey

    public init() { privateKey = Curve25519.Signing.PrivateKey() }

    public init(rawRepresentation: Data) throws {
        privateKey = try Curve25519.Signing.PrivateKey(rawRepresentation: rawRepresentation)
    }

    public var rawRepresentation: Data { privateKey.rawRepresentation }
    public var publicKeyRaw: Data { privateKey.publicKey.rawRepresentation }

    public func sign(_ message: Data) throws -> Data {
        try privateKey.signature(for: message)
    }
}

/// Signs requests the way the server's `auth.Canonical` expects (docs/API.md).
public struct RequestSigner: Sendable {
    public let deviceID: String
    public let key: DeviceSigningKey

    public init(deviceID: String, key: DeviceSigningKey) {
        self.deviceID = deviceID
        self.key = key
    }

    /// The canonical string: `nubilo-sig/v1`, device, ts, nonce, METHOD, request URI
    /// and hex SHA-256 of the body, joined by newlines.
    public static func canonical(deviceID: String, ts: Int64, nonce: String, method: String, path: String, body: Data) -> Data {
        let digest = SHA256.hash(data: body).map { String(format: "%02x", $0) }.joined()
        let s = ["nubilo-sig/v1", deviceID, String(ts), nonce, method.uppercased(), path, digest].joined(separator: "\n")
        return Data(s.utf8)
    }

    /// The `Authorization` header value. `path` is the request URI (path and query) exactly as sent.
    public func authorization(method: String, path: String, body: Data, ts: Int64 = RequestSigner.nowMS(), nonce: String = RequestSigner.newNonce()) throws -> String {
        let msg = Self.canonical(deviceID: deviceID, ts: ts, nonce: nonce, method: method, path: path, body: body)
        let sig = try key.sign(msg)
        return "Nubilo-Sig v1 device=\(deviceID),ts=\(ts),nonce=\(nonce),sig=\(sig.base64RawStd)"
    }

    public static func nowMS() -> Int64 { Int64(Date().timeIntervalSince1970 * 1000) }

    /// 32 hex characters of randomness.
    public static func newNonce() -> String {
        (0..<16).map { _ in String(format: "%02x", UInt8.random(in: 0...255)) }.joined()
    }
}

extension Data {
    /// Base64 without padding (Go's `base64.RawStdEncoding`).
    public var base64RawStd: String {
        var s = base64EncodedString()
        while s.hasSuffix("=") { s.removeLast() }
        return s
    }

    /// Base64url without padding.
    public var base64RawURL: String {
        base64RawStd.replacingOccurrences(of: "+", with: "-").replacingOccurrences(of: "/", with: "_")
    }

    /// Decodes standard or URL-safe base64 with or without padding.
    public init?(base64Lenient s: String) {
        var t = s.replacingOccurrences(of: "-", with: "+").replacingOccurrences(of: "_", with: "/")
        while t.count % 4 != 0 { t.append("=") }
        self.init(base64Encoded: t)
    }

    /// Lowercase hex.
    public var hex: String { map { String(format: "%02x", $0) }.joined() }
}

/// SHA-256 of data as lowercase hex (Nubilo blob IDs).
public func sha256Hex(_ data: Data) -> String {
    SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined()
}

/// Streams a file through SHA-256 without loading it into memory.
public func sha256Hex(fileAt url: URL, chunkSize: Int = 1 << 20) throws -> String {
    let fh = try FileHandle(forReadingFrom: url)
    defer { try? fh.close() }
    var hasher = SHA256()
    while true {
        let chunk = try fh.read(upToCount: chunkSize) ?? Data()
        if chunk.isEmpty { break }
        hasher.update(data: chunk)
    }
    return hasher.finalize().map { String(format: "%02x", $0) }.joined()
}

/// Verifies an Ed25519 signature (server identity checks, tests).
public func verifyEd25519(publicKey: Data, signature: Data, message: Data) -> Bool {
    guard let pk = try? Curve25519.Signing.PublicKey(rawRepresentation: publicKey) else { return false }
    return pk.isValidSignature(signature, for: message)
}
