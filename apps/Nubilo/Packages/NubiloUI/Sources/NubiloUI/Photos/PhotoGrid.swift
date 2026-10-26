import SwiftUI
import NubiloKit

/// A square-tile grid of photos that asks for more as the end scrolls into
/// view. With a `selection` binding, taps toggle selection instead of opening
/// the photo.
struct PhotoGrid: View {
    @Bindable var pager: PhotoPager
    var selection: Binding<Set<String>>?

    private let columns = [GridItem(.adaptive(minimum: 96), spacing: 2)]

    var body: some View {
        ScrollView {
            LazyVGrid(columns: columns, spacing: 2) {
                ForEach(pager.photos) { photo in
                    cell(photo)
                        .onAppear {
                            if photo.id == pager.photos.last?.id {
                                Task { await pager.loadMore() }
                            }
                        }
                }
            }
            if pager.loading {
                ProgressView().padding()
            }
        }
        .overlay {
            if pager.photos.isEmpty && pager.exhausted {
                ContentUnavailableView("No photos yet", systemImage: "photo.on.rectangle",
                                       description: Text("Photos backed up from your devices appear here."))
            }
        }
        .refreshable { await pager.reload() }
        .errorAlert($pager.error)
    }

    @ViewBuilder
    private func cell(_ photo: Photo) -> some View {
        if let selection {
            let isSelected = selection.wrappedValue.contains(photo.id)
            Button {
                if isSelected {
                    selection.wrappedValue.remove(photo.id)
                } else {
                    selection.wrappedValue.insert(photo.id)
                }
            } label: {
                PhotoTile(photo: photo)
                    .opacity(isSelected ? 0.7 : 1)
                    .overlay(alignment: .bottomTrailing) {
                        if isSelected {
                            Image(systemName: "checkmark.circle.fill")
                                .symbolRenderingMode(.multicolor)
                                .font(.title3)
                                .padding(4)
                        }
                    }
            }
            .buttonStyle(.plain)
            .accessibilityAddTraits(isSelected ? .isSelected : [])
        } else {
            NavigationLink(value: photo) {
                PhotoTile(photo: photo)
            }
            .buttonStyle(.plain)
        }
    }
}

struct PhotoTile: View {
    let photo: Photo

    var body: some View {
        Color.clear
            .aspectRatio(1, contentMode: .fit)
            .overlay { RemoteImage(id: photo.id) }
            .clipped()
            .contentShape(Rectangle())
            .overlay(alignment: .topTrailing) {
                if photo.kind == "video" {
                    Image(systemName: "video.fill")
                        .font(.caption)
                        .foregroundStyle(.white)
                        .shadow(radius: 2)
                        .padding(4)
                }
            }
            .accessibilityLabel(photo.name ?? "Photo")
    }
}
