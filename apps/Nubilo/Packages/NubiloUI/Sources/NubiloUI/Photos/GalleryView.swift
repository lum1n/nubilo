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
        .navigationDestination(for: Photo.self) { PhotoPreviewView(photo: $0) }
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
            guard pager == nil, let client = model.client else { return }
            let pager = PhotoPager { after in
                let page = try await client.photos(after: after, limit: 200)
                return (page.photos, page.next)
            }
            self.pager = pager
            await pager.loadMore()
        }
    }

    private func endSelection() {
        selecting = false
        selected = []
    }
}

/// One photo at preview size.
struct PhotoPreviewView: View {
    let photo: Photo

    var body: some View {
        RemoteImage(id: photo.id, rendition: "preview", contentMode: .fit)
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .background(.black)
            .safeAreaInset(edge: .bottom) {
                VStack(spacing: 2) {
                    if let taken = photo.takenAt {
                        Text(taken.formatted(date: .long, time: .shortened))
                    }
                    if let w = photo.width, let h = photo.height {
                        Text("\(w) × \(h)").foregroundStyle(.secondary)
                    }
                    if photo.kind == "video" {
                        Text("Video: a still is shown; play it in Files or on the web.").foregroundStyle(.secondary)
                    }
                }
                .font(.footnote)
                .padding(8)
                .frame(maxWidth: .infinity)
                .background(.bar)
            }
            .navigationTitle(photo.name ?? "Photo")
            .inlineNavigationTitle()
    }
}
