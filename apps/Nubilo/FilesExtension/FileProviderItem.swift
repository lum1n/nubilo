import FileProvider
import UniformTypeIdentifiers
import NubiloKit

/// A RemoteItem (or the top level) as the Files app and Finder see it.
final class FileProviderItem: NSObject, NSFileProviderItem {
    let remote: RemoteItem?

    init(_ remote: RemoteItem) {
        self.remote = remote
    }

    private init(root: Void) {
        remote = nil
    }

    static let root = FileProviderItem(root: ())

    var itemIdentifier: NSFileProviderItemIdentifier {
        remote.map { NSFileProviderItemIdentifier($0.id) } ?? .rootContainer
    }

    var parentItemIdentifier: NSFileProviderItemIdentifier {
        guard let remote, remote.parentID != RemoteItem.rootID else { return .rootContainer }
        return NSFileProviderItemIdentifier(remote.parentID)
    }

    var filename: String { remote?.name ?? "Nubilo" }

    var contentType: UTType {
        guard let remote, remote.kind == .file else { return .folder }
        let ext = (remote.name as NSString).pathExtension
        return UTType(filenameExtension: ext) ?? remote.mime.flatMap { UTType(mimeType: $0) } ?? .data
    }

    var capabilities: NSFileProviderItemCapabilities {
        guard let remote else { return [.allowsReading, .allowsContentEnumerating, .allowsAddingSubItems] }
        switch (remote.kind, remote.writable) {
        case (.folder, true):
            return [.allowsReading, .allowsContentEnumerating, .allowsAddingSubItems, .allowsRenaming, .allowsReparenting, .allowsDeleting]
        case (.folder, false):
            return [.allowsReading, .allowsContentEnumerating]
        case (.file, true):
            return [.allowsReading, .allowsWriting, .allowsRenaming, .allowsReparenting, .allowsDeleting]
        case (.file, false):
            return [.allowsReading]
        }
    }

    var itemVersion: NSFileProviderItemVersion {
        guard let remote else { return NSFileProviderItemVersion(contentVersion: Data("root".utf8), metadataVersion: Data("root".utf8)) }
        let content = remote.kind == .file ? remote.contentHash : "folder"
        let meta = "\(remote.revision)|\(remote.parentID)|\(remote.name)"
        return NSFileProviderItemVersion(contentVersion: Data(content.utf8), metadataVersion: Data(meta.utf8))
    }

    var documentSize: NSNumber? {
        guard let remote, remote.kind == .file else { return nil }
        return NSNumber(value: remote.size)
    }

    var contentModificationDate: Date? { remote?.modified }
}
