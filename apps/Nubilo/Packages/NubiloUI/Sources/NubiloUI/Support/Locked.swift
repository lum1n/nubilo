import Foundation

/// A value shared across threads behind a lock (URLSession delegate callbacks,
/// process pipes).
final class Locked<Value>: @unchecked Sendable {
    private var value: Value
    private let lock = NSLock()

    init(_ value: Value) { self.value = value }

    var current: Value {
        lock.lock(); defer { lock.unlock() }
        return value
    }

    func set(_ newValue: Value) {
        lock.lock(); defer { lock.unlock() }
        value = newValue
    }

    func mutate(_ body: (inout Value) -> Void) {
        lock.lock(); defer { lock.unlock() }
        body(&value)
    }
}
