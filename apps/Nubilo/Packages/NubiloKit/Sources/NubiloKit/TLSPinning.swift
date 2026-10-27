import Foundation
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif

#if canImport(Security)
import Security
#if canImport(CryptoKit)
import CryptoKit
#endif

/// Trusts certificates the system trusts (a public domain with Let's Encrypt)
/// and, for a self-signed LAN server, exactly the leaf certificate pinned on
/// first use. `onFirstUse` receives the SHA-256 of a new leaf so the app can
/// store it in `PairedServer.tlsPin`.
public final class PinningDelegate: NSObject, URLSessionDelegate, @unchecked Sendable {
    private let lock = NSLock()
    private var pin: String?
    private let onFirstUse: (@Sendable (String) -> Void)?

    public init(pin: String?, onFirstUse: (@Sendable (String) -> Void)? = nil) {
        self.pin = pin
        self.onFirstUse = onFirstUse
    }

    public func urlSession(_ session: URLSession, didReceive challenge: URLAuthenticationChallenge,
                           completionHandler: @escaping (URLSession.AuthChallengeDisposition, URLCredential?) -> Void) {
        guard challenge.protectionSpace.authenticationMethod == NSURLAuthenticationMethodServerTrust,
              let trust = challenge.protectionSpace.serverTrust else {
            return completionHandler(.performDefaultHandling, nil)
        }
        if SecTrustEvaluateWithError(trust, nil) {
            return completionHandler(.useCredential, URLCredential(trust: trust))
        }
        guard let chain = SecTrustCopyCertificateChain(trust) as? [SecCertificate], let leaf = chain.first else {
            return completionHandler(.cancelAuthenticationChallenge, nil)
        }
        let der = SecCertificateCopyData(leaf) as Data
        let digest = SHA256.hash(data: der).map { String(format: "%02x", $0) }.joined()
        lock.lock()
        let current = pin
        if current == nil { pin = digest }
        lock.unlock()
        if current == nil {
            onFirstUse?(digest)
            return completionHandler(.useCredential, URLCredential(trust: trust))
        }
        if current == digest {
            return completionHandler(.useCredential, URLCredential(trust: trust))
        }
        completionHandler(.cancelAuthenticationChallenge, nil)
    }
}

extension URLSessionTransport {
    /// System trust only and no URL cache (extensions without a pinned certificate).
    public static func uncached() -> URLSessionTransport {
        let cfg = URLSessionConfiguration.default
        cfg.waitsForConnectivity = false
        cfg.urlCache = nil
        cfg.requestCachePolicy = .reloadIgnoringLocalCacheData
        return URLSessionTransport(session: URLSession(configuration: cfg))
    }

    /// A transport that pins self-signed LAN certificates (see PinningDelegate).
    /// `cached: false` turns the URL cache off (extensions, which run under
    /// tight memory limits and store downloaded files themselves).
    public static func pinned(_ pin: String?, cached: Bool = true, onFirstUse: (@Sendable (String) -> Void)? = nil) -> URLSessionTransport {
        let cfg = URLSessionConfiguration.default
        // Not waitsForConnectivity: signed requests expire after 60 s, so a
        // request held until the network returns would be rejected anyway.
        cfg.waitsForConnectivity = false
        cfg.urlCache = cached ? URLCache(memoryCapacity: 32 << 20, diskCapacity: 500 << 20) : nil
        if !cached { cfg.requestCachePolicy = .reloadIgnoringLocalCacheData }
        let session = URLSession(configuration: cfg, delegate: PinningDelegate(pin: pin, onFirstUse: onFirstUse), delegateQueue: nil)
        return URLSessionTransport(session: session)
    }
}
#endif
