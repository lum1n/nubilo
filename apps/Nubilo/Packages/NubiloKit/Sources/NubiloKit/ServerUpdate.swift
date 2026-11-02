import Foundation
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif

/// The home server's self-update state (`GET /api/v1/admin/update`, admins
/// only). Servers without the updater answer 404; those calls throw
/// `ServerUpdateUnsupported`.
public struct ServerUpdateStatus: Codable, Sendable, Equatable {
    public struct Release: Codable, Sendable, Equatable {
        public var version: String
        public var notes: String?
        /// Unix ms.
        public var publishedAt: Int64?
    }

    public struct Outcome: Codable, Sendable, Equatable {
        public var from: String
        public var to: String
        /// "updated", "rolled_back" or "failed".
        public var result: String
        public var message: String?
        /// Unix ms.
        public var at: Int64?

        public var succeeded: Bool { result == "updated" }
    }

    public var current: String
    public var available: Release?
    public var auto: Bool
    /// "idle", "checking", "downloading", "preflight", "installing" or
    /// "restarting".
    public var state: String
    /// 0...1 while downloading, if the server knows.
    public var progress: Double?
    /// Why the available release cannot be installed here; empty if it can.
    public var blocked: String?
    /// Unix ms of the last check.
    public var checkedAt: Int64?
    /// Unix ms of the next automatic install attempt.
    public var nextAutoAt: Int64?
    public var last: Outcome?

    public var isBusy: Bool { state != "idle" }
    public var blockedReason: String? {
        guard let b = blocked?.trimmingCharacters(in: .whitespacesAndNewlines), !b.isEmpty else { return nil }
        return b
    }
}

/// The server has no updater (older version), or the user is not an admin.
public struct ServerUpdateUnsupported: Error, Equatable {
    public var status: Int
}

extension NubiloClient {
    public func updateStatus() async throws -> ServerUpdateStatus {
        try await updateCall("GET", "/api/v1/admin/update")
    }

    /// Asks the server to look for a release now.
    public func checkForUpdate() async throws -> ServerUpdateStatus {
        try await updateCall("POST", "/api/v1/admin/update/check")
    }

    /// Starts installing the available release. The server restarts while
    /// installing; poll `updateStatus()` for the outcome.
    public func installUpdate() async throws -> ServerUpdateStatus {
        try await updateCall("POST", "/api/v1/admin/update/install")
    }

    public func setAutoUpdate(_ on: Bool) async throws -> ServerUpdateStatus {
        try await updateCall("PUT", "/api/v1/admin/update/settings", json: ["auto": on])
    }

    private func updateCall(_ method: String, _ path: String, json: [String: Bool]? = nil) async throws -> ServerUpdateStatus {
        do {
            return try await call(method, path, json: json)
        } catch let NubiloError.http(status, _) where status == 403 || status == 404 {
            throw ServerUpdateUnsupported(status: status)
        }
    }
}
