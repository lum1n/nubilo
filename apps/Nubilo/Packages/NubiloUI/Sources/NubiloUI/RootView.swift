import SwiftUI
import NubiloKit

/// Onboarding until the device is paired, then the main UI: tabs on iOS, a
/// sidebar on macOS.
public struct RootView: View {
    @Environment(AppModel.self) private var model

    public init() {}

    public var body: some View {
        @Bindable var model = model
        Group {
            if model.isPaired {
                MainView()
            } else {
                OnboardingView()
            }
        }
        .errorAlert($model.alert, title: "Nubilo")
    }
}

enum AppSection: String, CaseIterable, Identifiable, Hashable {
    case library, albums, backup, calendarContacts, files, family, settings

    var id: String { rawValue }

    var title: String {
        switch self {
        case .library: "Library"
        case .albums: "Albums"
        case .backup: "Backup"
        case .calendarContacts: "Calendar & Contacts"
        case .files: "Files"
        case .family: "Family"
        case .settings: "Settings"
        }
    }

    var systemImage: String {
        switch self {
        case .library: "photo.on.rectangle"
        case .albums: "rectangle.stack"
        case .backup: "arrow.up.circle"
        case .calendarContacts: "calendar"
        case .files: "folder"
        case .family: "person.2"
        case .settings: "gear"
        }
    }

    static var visible: [AppSection] { allCases }

    @MainActor @ViewBuilder
    var content: some View {
        switch self {
        case .library: GalleryView()
        case .albums: AlbumsView()
        case .backup: BackupStatusView()
        case .calendarContacts: CalendarContactsView()
        case .files: FilesView()
        case .family: FamilyView()
        case .settings: SettingsView()
        }
    }
}

struct MainView: View {
    #if os(macOS)
    @State private var selection: AppSection? = .library
    #endif

    var body: some View {
        #if os(iOS)
        TabView {
            PhotosTab()
                .tabItem { Label("Photos", systemImage: "photo.on.rectangle") }
            NavigationStack { AccountsView() }
                .tabItem { Label("Accounts", systemImage: "calendar") }
            NavigationStack { FamilyView() }
                .tabItem { Label("Family", systemImage: "person.2") }
            NavigationStack { SettingsView() }
                .tabItem { Label("Settings", systemImage: "gear") }
        }
        #else
        NavigationSplitView {
            List(AppSection.visible, selection: $selection) { section in
                Label(section.title, systemImage: section.systemImage).tag(section)
            }
            .navigationSplitViewColumnWidth(min: 180, ideal: 200)
        } detail: {
            NavigationStack {
                (selection ?? .library).content
            }
            .id(selection)
        }
        #endif
    }
}

#if os(iOS)
/// Library and albums behind a segmented control, with the backup screen in the toolbar.
struct PhotosTab: View {
    enum Mode: Hashable { case library, albums }
    @State private var mode = Mode.library

    var body: some View {
        NavigationStack {
            Group {
                switch mode {
                case .library: GalleryView()
                case .albums: AlbumsView()
                }
            }
            .safeAreaInset(edge: .top) {
                Picker("Show", selection: $mode) {
                    Text("Library").tag(Mode.library)
                    Text("Albums").tag(Mode.albums)
                }
                .pickerStyle(.segmented)
                .padding(.horizontal)
                .padding(.bottom, 6)
                .background(.bar)
            }
            .toolbar {
                ToolbarItem(placement: .topBarLeading) {
                    NavigationLink {
                        BackupStatusView()
                    } label: {
                        Label("Backup", systemImage: "arrow.up.circle")
                    }
                }
            }
        }
    }
}

/// Calendar, contacts, reminders and files.
struct AccountsView: View {
    var body: some View {
        List {
            NavigationLink {
                CalendarContactsView()
            } label: {
                Label("Calendar, Contacts & Reminders", systemImage: "calendar")
            }
            NavigationLink {
                FilesView()
            } label: {
                Label("Files", systemImage: "folder")
            }
        }
        .navigationTitle("Accounts")
    }
}
#endif
