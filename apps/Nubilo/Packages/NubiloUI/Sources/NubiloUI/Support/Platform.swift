import SwiftUI
#if os(iOS)
import UIKit
public typealias PlatformImage = UIImage
#elseif os(macOS)
import AppKit
public typealias PlatformImage = NSImage
#endif

extension Image {
    init(platformImage: PlatformImage) {
        #if os(iOS)
        self.init(uiImage: platformImage)
        #else
        self.init(nsImage: platformImage)
        #endif
    }
}

enum Clipboard {
    static func copy(_ text: String) {
        #if os(iOS)
        UIPasteboard.general.string = text
        #else
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(text, forType: .string)
        #endif
    }
}

enum ExternalURL {
    /// Opens `url` outside the app. iOS hands https URLs to Safari, which is
    /// the only place configuration profiles can be downloaded and installed.
    @MainActor
    static func open(_ url: URL) {
        #if os(iOS)
        UIApplication.shared.open(url)
        #else
        NSWorkspace.shared.open(url)
        #endif
    }
}

enum ThisDevice {
    /// A default name for this device. On iOS 16+ this is the model name
    /// ("iPhone") unless the app has the user-assigned-device-name entitlement.
    @MainActor
    static var defaultName: String {
        #if os(iOS)
        UIDevice.current.name
        #else
        Host.current().localizedName ?? "Mac"
        #endif
    }
}

extension View {
    /// Username-style text input: no autocapitalisation or autocorrection.
    func plainTextInput() -> some View {
        #if os(iOS)
        self.textInputAutocapitalization(.never).autocorrectionDisabled()
        #else
        self.autocorrectionDisabled()
        #endif
    }

    /// macOS sheets size to their content, which is tiny for forms and lists.
    func macSheetFrame() -> some View {
        #if os(macOS)
        self.frame(minWidth: 420, minHeight: 440)
        #else
        self
        #endif
    }

    func inlineNavigationTitle() -> some View {
        #if os(iOS)
        self.navigationBarTitleDisplayMode(.inline)
        #else
        self
        #endif
    }
}
