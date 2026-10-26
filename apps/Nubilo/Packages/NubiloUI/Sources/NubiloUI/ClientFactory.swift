import Foundation
import NubiloKit

/// Builds the signed, TLS-pinned client for a paired server. Shared by the UI
/// and by background launches that run before any view exists.
public enum ClientFactory {
    public static func make(
        for server: PairedServer,
        keys: any KeyStore = KeychainKeyStore(),
        store: ServerStore = ServerStore()
    ) throws -> NubiloClient {
        guard let key = try keys.load(tag: server.keyTag) else { throw NubiloError.notPaired }
        let signer = RequestSigner(deviceID: server.deviceID, key: key)
        // Only called when no pin is stored yet (self-signed LAN server seen for
        // the first time after pairing); later launches enforce the pin.
        let transport = URLSessionTransport.pinned(server.tlsPin) { pin in
            guard var saved = store.load(), saved.deviceID == server.deviceID else { return }
            saved.tlsPin = pin
            try? store.save(saved)
        }
        return NubiloClient(baseURL: server.baseURL, signer: signer, transport: transport)
    }
}
