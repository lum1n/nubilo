#if os(macOS)
import Foundation

/// Runs the `nubilo` binary bundled with the Mac app.
///
/// It lives in Contents/Helpers rather than Contents/MacOS because the app's
/// own executable is "Nubilo", and APFS is case-insensitive by default.
public struct NubiloCLI: Sendable {
    public let executable: URL

    public struct Output: Sendable {
        public var status: Int32
        public var stdout: Data
        public var stderr: Data

        public var errorText: String {
            let text = String(decoding: stderr, as: UTF8.self).trimmingCharacters(in: .whitespacesAndNewlines)
            return text.isEmpty ? "nubilo exited with status \(status)" : text
        }
    }

    public init(executable: URL) { self.executable = executable }

    /// The bundled binary, or `NUBILO_CLI` for development builds.
    public static func bundled() -> NubiloCLI? {
        var candidates: [URL] = []
        if let env = ProcessInfo.processInfo.environment["NUBILO_CLI"] {
            candidates.append(URL(fileURLWithPath: env))
        }
        candidates.append(Bundle.main.bundleURL.appendingPathComponent("Contents/Helpers/nubilo"))
        if let aux = Bundle.main.url(forAuxiliaryExecutable: "nubilo") {
            candidates.append(aux)
        }
        return candidates.first { FileManager.default.isExecutableFile(atPath: $0.path) }.map { NubiloCLI(executable: $0) }
    }

    /// Runs to completion. Pipes are drained concurrently so a chatty stderr
    /// cannot block the process on a full pipe buffer.
    public func run(_ arguments: [String]) async throws -> Output {
        let exe = executable
        return try await withCheckedThrowingContinuation { continuation in
            DispatchQueue.global(qos: .userInitiated).async {
                let process = Process()
                process.executableURL = exe
                process.arguments = arguments
                let out = Pipe()
                let err = Pipe()
                process.standardOutput = out
                process.standardError = err
                process.standardInput = FileHandle.nullDevice
                do {
                    try process.run()
                } catch {
                    continuation.resume(throwing: error)
                    return
                }
                let errData = Locked(Data())
                let group = DispatchGroup()
                group.enter()
                DispatchQueue.global().async {
                    errData.set(err.fileHandleForReading.readDataToEndOfFile())
                    group.leave()
                }
                let outData = out.fileHandleForReading.readDataToEndOfFile()
                group.wait()
                process.waitUntilExit()
                continuation.resume(returning: Output(status: process.terminationStatus, stdout: outData, stderr: errData.current))
            }
        }
    }

    /// `run`, with launch failures folded into nil.
    public func output(_ arguments: [String]) async -> Output? {
        try? await run(arguments)
    }

    /// Starts a long-running subcommand (e.g. `agent ui`) and returns at once.
    public func launch(_ arguments: [String]) throws -> Process {
        let process = Process()
        process.executableURL = executable
        process.arguments = arguments
        process.standardInput = FileHandle.nullDevice
        process.standardOutput = FileHandle.nullDevice
        process.standardError = FileHandle.nullDevice
        try process.run()
        return process
    }
}

/// `nubilo doctor --json` output (internal/doctor).
public struct DoctorReport: Decodable, Sendable {
    public struct Check: Decodable, Sendable, Identifiable {
        public var id: String
        public var title: String
        public var status: String
        public var detail: String?
        public var fix: String?
    }

    public var role: String
    public var dataDir: String
    public var checks: [Check]
    public var ok: Int
    public var warn: Int
    public var fail: Int
    public var healthy: Bool
}

/// `nubilo --json status` output.
public struct ServerSummary: Decodable, Sendable {
    public var dataDir: String
    public var listen: String
    public var headSeq: Int64
    public var devices: Int
    public var version: String
}

/// One `nubilo setup --json --step N` line (internal/setup.StepResult).
public struct SetupStepResult: Decodable, Sendable {
    public var step: Int
    public var name: String
    public var ok: Bool
    public var message: String?
    public var data: [String: JSONValue]?
    public var error: String?
}

/// Any JSON value; setup step data is heterogeneous.
public enum JSONValue: Decodable, Sendable, Equatable {
    case string(String)
    case number(Double)
    case bool(Bool)
    case null
    case other

    public init(from decoder: Decoder) throws {
        let c = try decoder.singleValueContainer()
        if c.decodeNil() { self = .null }
        else if let b = try? c.decode(Bool.self) { self = .bool(b) }
        else if let n = try? c.decode(Double.self) { self = .number(n) }
        else if let s = try? c.decode(String.self) { self = .string(s) }
        else { self = .other }
    }

    public var string: String? {
        if case let .string(s) = self { return s }
        return nil
    }
}

extension JSONDecoder {
    static let snakeCase: JSONDecoder = {
        let d = JSONDecoder()
        d.keyDecodingStrategy = .convertFromSnakeCase
        return d
    }()
}
#endif
