import SwiftUI
import NubiloKit

/// Picks an album (own or shared with edit access) for selected photos.
struct AddToAlbumSheet: View {
    let photoIDs: [String]
    let onDone: () -> Void

    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @State private var albums: [Album] = []
    @State private var newName = ""
    @State private var working = false
    @State private var error: String?

    var body: some View {
        NavigationStack {
            List {
                Section("New Album") {
                    HStack {
                        TextField("Name", text: $newName)
                        Button("Create & Add") { Task { await createAndAdd() } }
                            .disabled(newName.trimmingCharacters(in: .whitespaces).isEmpty || working)
                    }
                }
                Section("Albums") {
                    ForEach(albums) { album in
                        Button {
                            Task { await add(to: album) }
                        } label: {
                            AlbumRow(album: album)
                        }
                        .disabled(working)
                    }
                }
            }
            .navigationTitle("Add \(photoIDs.count) to Album")
            .inlineNavigationTitle()
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
            }
            .task { await load() }
            .errorAlert($error)
        }
        .macSheetFrame()
    }

    private func load() async {
        guard let client = model.client else { return }
        do {
            albums = try await client.albums().filter { $0.isOwned || $0.access == "rw" }
        } catch {
            self.error = describe(error)
        }
    }

    private func add(to album: Album) async {
        guard let client = model.client else { return }
        working = true
        defer { working = false }
        do {
            try await client.addToAlbum(album.id, photoIDs: photoIDs)
            onDone()
            dismiss()
        } catch {
            self.error = describe(error)
        }
    }

    private func createAndAdd() async {
        guard let client = model.client else { return }
        working = true
        do {
            let album = try await client.createAlbum(name: newName.trimmingCharacters(in: .whitespaces))
            working = false
            await add(to: album)
        } catch {
            working = false
            self.error = describe(error)
        }
    }
}
