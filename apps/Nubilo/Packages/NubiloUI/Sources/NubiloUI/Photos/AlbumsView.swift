import SwiftUI
import NubiloKit

/// Own albums and albums shared with the user.
struct AlbumsView: View {
    @Environment(AppModel.self) private var model
    @State private var albums: [Album] = []
    @State private var loaded = false
    @State private var creating = false
    @State private var newName = ""
    @State private var error: String?

    var body: some View {
        List {
            let own = albums.filter(\.isOwned)
            let shared = albums.filter { !$0.isOwned }
            if !own.isEmpty {
                Section("My Albums") {
                    ForEach(own) { album in
                        NavigationLink(value: album) { AlbumRow(album: album) }
                    }
                }
            }
            if !shared.isEmpty {
                Section("Shared with Me") {
                    ForEach(shared) { album in
                        NavigationLink(value: album) { AlbumRow(album: album) }
                    }
                }
            }
        }
        .overlay {
            if loaded && albums.isEmpty {
                ContentUnavailableView("No albums", systemImage: "rectangle.stack",
                                       description: Text("Create an album, or ask someone on your server to share one with you."))
            }
        }
        .navigationTitle("Albums")
        .navigationDestination(for: Album.self) { AlbumDetailView(album: $0) }
        .toolbar {
            ToolbarItem(placement: .primaryAction) {
                Button {
                    creating = true
                } label: {
                    Label("New Album", systemImage: "plus")
                }
            }
        }
        .alert("New Album", isPresented: $creating) {
            TextField("Name", text: $newName)
            Button("Create") { Task { await create() } }
            Button("Cancel", role: .cancel) { newName = "" }
        }
        .refreshable { await load() }
        .task { await load() }
        .errorAlert($error)
    }

    private func load() async {
        guard let client = model.client else { return }
        do {
            albums = try await client.albums().sorted { $0.name.localizedStandardCompare($1.name) == .orderedAscending }
        } catch {
            self.error = describe(error)
        }
        loaded = true
    }

    private func create() async {
        let name = newName.trimmingCharacters(in: .whitespaces)
        newName = ""
        guard !name.isEmpty, let client = model.client else { return }
        do {
            _ = try await client.createAlbum(name: name)
            await load()
        } catch {
            self.error = describe(error)
        }
    }
}

struct AlbumRow: View {
    let album: Album

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(album.name)
            if !album.isOwned {
                Text("From \(album.owner) · \(album.access == "rw" ? "can add photos" : "view only")")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
    }
}

/// The photos of one album.
struct AlbumDetailView: View {
    let album: Album

    @Environment(AppModel.self) private var model
    @State private var pager: PhotoPager?
    @State private var sharing = false

    var body: some View {
        Group {
            if let pager {
                PhotoGrid(pager: pager)
            } else {
                ProgressView()
            }
        }
        .navigationTitle(album.name)
        .navigationDestination(for: Photo.self) { PhotoPreviewView(photo: $0) }
        .toolbar {
            if album.isOwned {
                ToolbarItem(placement: .primaryAction) {
                    Button {
                        sharing = true
                    } label: {
                        Label("Share", systemImage: "person.crop.circle.badge.plus")
                    }
                }
            }
        }
        .sheet(isPresented: $sharing) {
            ShareAlbumView(album: album)
        }
        .task {
            guard pager == nil, let client = model.client else { return }
            let id = album.id
            let pager = PhotoPager { after in
                let page = try await client.albumItems(id, after: after, limit: 200)
                return (page.items, page.next)
            }
            self.pager = pager
            await pager.loadMore()
        }
    }
}
