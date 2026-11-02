import SwiftUI
import NubiloKit

/// The user's photo library on the server.
struct GalleryView: View {
    @Environment(AppModel.self) private var model
    @State private var pager: PhotoPager?
    @State private var selecting = false
    @State private var selected: Set<String> = []
    @State private var addingToAlbum = false

    var body: some View {
        Group {
            if let pager {
                PhotoGrid(pager: pager, selection: selecting ? $selected : nil)
            } else {
                ProgressView()
            }
        }
        .navigationTitle(selecting ? "\(selected.count) Selected" : "Library")
        .navigationDestination(for: Photo.self) { PhotoViewer(photos: pager?.photos ?? [], start: $0) }
        .toolbar {
            if selecting {
                ToolbarItem(placement: .primaryAction) {
                    Button("Add to Album") { addingToAlbum = true }
                        .disabled(selected.isEmpty)
                }
                ToolbarItem(placement: .cancellationAction) {
                    Button("Done") { endSelection() }
                }
            } else {
                ToolbarItem(placement: .primaryAction) {
                    Button("Select") { selecting = true }
                }
            }
        }
        .sheet(isPresented: $addingToAlbum) {
            AddToAlbumSheet(photoIDs: Array(selected)) { endSelection() }
        }
        .task {
            if pager == nil, let client = model.client {
                pager = PhotoPager(
                    fetch: { after in
                        let page = try await client.photos(after: after, limit: 1000)
                        return (page.photos, page.next)
                    },
                    index: { etag in try await client.photoIndex(etag: etag) },
                    cache: .standard(for: client),
                    cacheKey: PhotoIndexCache.libraryKey
                )
            }
            pager?.start()
        }
    }

    private func endSelection() {
        selecting = false
        selected = []
    }
}
