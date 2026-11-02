import Foundation

extension Int64 {
    /// File-size style byte count ("1.2 GB").
    var byteString: String { ByteCountFormatter.string(fromByteCount: self, countStyle: .file) }

    /// "1.2 GB" or "1.2 GB of 50 GB" when `quota` is a limit.
    func usage(of quota: Int64?) -> String {
        guard let quota, quota > 0 else { return byteString }
        return "\(byteString) of \(quota.byteString)"
    }

    /// Unix milliseconds as a Date.
    var msDate: Date { Date(timeIntervalSince1970: Double(self) / 1000) }
}

extension Date {
    var relative: String { formatted(.relative(presentation: .named)) }
}

/// Text for an error shown to the user.
func describe(_ error: Error) -> String {
    if let e = error as? LocalizedError, let d = e.errorDescription { return d }
    return error.localizedDescription
}
