import SwiftUI
import NubiloKit

/// Photos one at a time on black. Swipe or use the arrow keys to move between
/// them; pinch, double-tap or ⌘+ / ⌘− to zoom, and drag to look around a
/// zoomed photo. A tap hides the bars.
struct PhotoViewer: View {
    let photos: [Photo]

    @State private var currentID: String?
    @State private var zoomed = false
    @State private var zoomRequest: ZoomRequest?
    @State private var chromeHidden = false
    @FocusState private var focused: Bool

    private let indexByID: [String: Int]

    init(photos: [Photo], start: Photo) {
        let list = photos.contains { $0.id == start.id } ? photos : [start]
        self.photos = list
        indexByID = Dictionary(list.enumerated().map { ($1.id, $0) }, uniquingKeysWith: { a, _ in a })
        _currentID = State(initialValue: start.id)
    }

    private var index: Int? { currentID.flatMap { indexByID[$0] } }
    private var current: Photo? { index.map { photos[$0] } }

    var body: some View {
        ZStack {
            pages
                .background(.black)
                .ignoresSafeArea()
        }
        .overlay(alignment: .bottom) {
            if !chromeHidden, let current {
                PhotoInfoBar(photo: current, position: index.map { "\($0 + 1) of \(photos.count.formatted())" })
                    .transition(.move(edge: .bottom).combined(with: .opacity))
            }
        }
        #if os(macOS)
        .overlay {
            HStack {
                pageButton(-1, systemImage: "chevron.left", label: "Previous Photo")
                Spacer()
                pageButton(1, systemImage: "chevron.right", label: "Next Photo")
            }
            .padding()
        }
        #endif
        .focusable()
        .focusEffectDisabled()
        .focused($focused)
        .onKeyPress(.leftArrow) { step(-1) ? .handled : .ignored }
        .onKeyPress(.rightArrow) { step(1) ? .handled : .ignored }
        .onAppear { focused = true }
        .onChange(of: currentID) { zoomed = false }
        .navigationTitle(current?.takenAt?.formatted(date: .abbreviated, time: .omitted) ?? current?.name ?? "Photo")
        .inlineNavigationTitle()
        #if os(macOS)
        .toolbar {
            ToolbarItem(placement: .primaryAction) {
                ControlGroup {
                    Button { zoomRequest = ZoomRequest(step: .out) } label: { Label("Zoom Out", systemImage: "minus.magnifyingglass") }
                        .keyboardShortcut("-")
                    Button { zoomRequest = ZoomRequest(step: .fit) } label: { Label("Fit", systemImage: "arrow.down.right.and.arrow.up.left") }
                        .keyboardShortcut("0")
                        .disabled(!zoomed)
                    Button { zoomRequest = ZoomRequest(step: .in) } label: { Label("Zoom In", systemImage: "plus.magnifyingglass") }
                        .keyboardShortcut("+")
                }
            }
        }
        #else
        .toolbar(chromeHidden ? .hidden : .visible, for: .navigationBar)
        .toolbar(.hidden, for: .tabBar)
        .statusBarHidden(chromeHidden)
        #endif
    }

    private var pages: some View {
        ScrollViewReader { proxy in
            pageScroller
                // scrollPosition alone does not always reach a photo far
                // into the lazy stack.
                .onAppear { if let currentID { proxy.scrollTo(currentID) } }
        }
    }

    private var pageScroller: some View {
        ScrollView(.horizontal) {
            LazyHStack(spacing: 0) {
                ForEach(photos) { photo in
                    ZoomablePhoto(photo: photo, isCurrent: photo.id == currentID, request: zoomRequest,
                                  isZoomed: $zoomed) {
                        withAnimation(.easeInOut(duration: 0.2)) { chromeHidden.toggle() }
                    }
                    .containerRelativeFrame([.horizontal, .vertical])
                    .id(photo.id)
                }
            }
            .scrollTargetLayout()
        }
        .scrollTargetBehavior(.paging)
        .scrollPosition(id: $currentID)
        .scrollDisabled(zoomed)
        .scrollIndicators(.hidden)
    }

    /// Moves to the next (1) or previous (-1) photo. False at either end.
    @discardableResult
    private func step(_ delta: Int) -> Bool {
        guard let index, photos.indices.contains(index + delta) else { return false }
        withAnimation(.snappy) { currentID = photos[index + delta].id }
        return true
    }

    #if os(macOS)
    private func pageButton(_ delta: Int, systemImage: String, label: String) -> some View {
        let enabled = index.map { photos.indices.contains($0 + delta) } ?? false
        return Button { _ = step(delta) } label: {
            Image(systemName: systemImage)
                .font(.title2.weight(.semibold))
                .frame(width: 36, height: 36)
                .background(.regularMaterial, in: Circle())
        }
        .buttonStyle(.plain)
        .accessibilityLabel(label)
        .opacity(enabled && !chromeHidden ? 1 : 0)
        .disabled(!enabled)
    }
    #endif
}

/// A zoom asked for from the toolbar or keyboard. A new one each time, so
/// asking twice in a row still zooms twice.
struct ZoomRequest: Equatable {
    enum Step { case `in`, out, fit }
    var step: Step
    var id = UUID()
}

/// One photo that zooms around the pinch point. Shows the thumbnail at once,
/// then the preview, and loads the original once zoomed in far enough for the
/// preview to look soft.
private struct ZoomablePhoto: View {
    let photo: Photo
    let isCurrent: Bool
    let request: ZoomRequest?
    @Binding var isZoomed: Bool
    let onTap: () -> Void

    @Environment(AppModel.self) private var model
    @State private var image: PlatformImage?
    @State private var hasOriginal = false
    @State private var failed = false

    @State private var scale: CGFloat = 1
    @State private var offset: CGSize = .zero
    /// Scale and offset when the current pinch began.
    @State private var pinchStart: Transform?
    /// Where the offset would be with no drag, while dragging.
    @State private var panAnchor: CGSize?

    private struct Transform {
        var scale: CGFloat
        var offset: CGSize
    }

    static let maxScale: CGFloat = 8
    static let doubleTapScale: CGFloat = 2.5

    var body: some View {
        GeometryReader { geo in
            let fitted = fittedSize(in: geo.size)
            content
                .frame(width: fitted.width, height: fitted.height)
                .scaleEffect(scale)
                .offset(offset)
                .frame(width: geo.size.width, height: geo.size.height)
                .contentShape(Rectangle())
                .onTapGesture(count: 2) { location in
                    withAnimation(.spring(duration: 0.35)) {
                        if scale > 1.01 {
                            scale = 1
                            offset = .zero
                        } else {
                            zoom(to: Self.doubleTapScale, around: location, in: geo.size)
                            settle(in: geo.size, fitted: fitted)
                        }
                    }
                }
                .onTapGesture(perform: onTap)
                .gesture(magnify(in: geo.size, fitted: fitted))
                .simultaneousGesture(pan(in: geo.size, fitted: fitted), including: scale > 1.01 ? .all : .subviews)
                .onChange(of: request) {
                    guard isCurrent, let request else { return }
                    withAnimation(.spring(duration: 0.35)) {
                        apply(request.step, in: geo.size, fitted: fitted)
                    }
                }
        }
        .clipped()
        .onChange(of: scale > 1.01) { _, zoomed in
            if isCurrent { isZoomed = zoomed }
        }
        .onChange(of: isCurrent) { _, current in
            if !current {
                scale = 1
                offset = .zero
            }
        }
        .task(id: photo.id) { await loadFirst() }
        .task(id: scale > 1.5) {
            if scale > 1.5, !hasOriginal, !photo.isVideo {
                await load(.original)
            }
        }
        .accessibilityElement()
        .accessibilityLabel(photo.name ?? "Photo")
        .accessibilityAddTraits(.isImage)
        .accessibilityZoomAction { action in
            withAnimation {
                scale = action.direction == .zoomIn ? min(scale * 2, Self.maxScale) : 1
                if scale == 1 { offset = .zero }
            }
        }
    }

    @ViewBuilder
    private var content: some View {
        if let image {
            Image(platformImage: image)
                .resizable()
                .interpolation(.high)
                .aspectRatio(contentMode: .fit)
        } else if failed {
            Image(systemName: photo.isVideo ? "video" : "photo")
                .font(.largeTitle)
                .foregroundStyle(.secondary)
        } else {
            ProgressView().tint(.white)
        }
    }

    /// The preview, or the original when the server made no preview (it
    /// would send the original anyway). Never a whole video.
    private func loadFirst() async {
        if image == nil {
            image = ImageLoader.shared.cached(photo, .preview) ?? ImageLoader.shared.cached(photo, .thumb)
        }
        guard photo.fallsBackToOriginal(.preview) else { return await load(.preview) }
        if photo.isVideo {
            if image == nil { failed = true }
            return
        }
        await load(.original)
    }

    private func load(_ rendition: PhotoRendition) async {
        guard let client = model.client else { return }
        if let loaded = await ImageLoader.shared.image(photo, rendition, client: client) {
            // A preview that arrives after the original must not replace it.
            if rendition != .original, hasOriginal { return }
            image = loaded
            if rendition == .original { hasOriginal = true }
        } else if image == nil, !Task.isCancelled {
            failed = true
        }
    }

    /// The photo's size when it fits the screen.
    private func fittedSize(in size: CGSize) -> CGSize {
        let aspect: CGFloat
        if let image, image.size.width > 0, image.size.height > 0 {
            aspect = image.size.width / image.size.height
        } else if let w = photo.width, let h = photo.height, w > 0, h > 0 {
            aspect = CGFloat(w) / CGFloat(h)
        } else {
            return size
        }
        guard size.width > 0, size.height > 0 else { return size }
        return size.width / size.height > aspect
            ? CGSize(width: size.height * aspect, height: size.height)
            : CGSize(width: size.width, height: size.width / aspect)
    }

    // MARK: Gestures

    private func magnify(in size: CGSize, fitted: CGSize) -> some Gesture {
        MagnifyGesture()
            .onChanged { value in
                let start = pinchStart ?? Transform(scale: scale, offset: offset)
                if pinchStart == nil { pinchStart = start }
                // A little past the limits while pinching; it springs back after.
                let target = min(max(start.scale * value.magnification, 0.7), Self.maxScale * 1.3)
                zoom(from: start, to: target, around: value.startLocation, in: size)
            }
            .onEnded { _ in
                pinchStart = nil
                withAnimation(.spring(duration: 0.35)) { settle(in: size, fitted: fitted) }
            }
    }

    private func pan(in size: CGSize, fitted: CGSize) -> some Gesture {
        DragGesture()
            .onChanged { value in
                // The pinch moves the photo itself; pick up from wherever it left it.
                guard pinchStart == nil else {
                    panAnchor = nil
                    return
                }
                let anchor = panAnchor ?? CGSize(width: offset.width - value.translation.width,
                                                 height: offset.height - value.translation.height)
                panAnchor = anchor
                offset = CGSize(width: anchor.width + value.translation.width,
                                height: anchor.height + value.translation.height)
            }
            .onEnded { value in
                guard let anchor = panAnchor else { return }
                panAnchor = nil
                // Carry on in the direction of the flick.
                withAnimation(.spring(duration: 0.45)) {
                    offset = CGSize(width: anchor.width + value.predictedEndTranslation.width,
                                    height: anchor.height + value.predictedEndTranslation.height)
                    settle(in: size, fitted: fitted)
                }
            }
    }

    private func apply(_ step: ZoomRequest.Step, in size: CGSize, fitted: CGSize) {
        let center = CGPoint(x: size.width / 2, y: size.height / 2)
        switch step {
        case .in: zoom(to: scale * 1.5, around: center, in: size)
        case .out: zoom(to: scale / 1.5, around: center, in: size)
        case .fit: scale = 1
        }
        settle(in: size, fitted: fitted)
    }

    private func zoom(to target: CGFloat, around point: CGPoint, in size: CGSize) {
        zoom(from: Transform(scale: scale, offset: offset), to: target, around: point, in: size)
    }

    /// Scales so the point under `point` (in view coordinates) stays put.
    private func zoom(from start: Transform, to target: CGFloat, around point: CGPoint, in size: CGSize) {
        let px = point.x - size.width / 2
        let py = point.y - size.height / 2
        let k = target / start.scale
        offset = CGSize(width: px - k * (px - start.offset.width),
                        height: py - k * (py - start.offset.height))
        scale = target
    }

    /// Back within the zoom limits, with no empty space past the photo's edges.
    private func settle(in size: CGSize, fitted: CGSize) {
        scale = min(max(scale, 1), Self.maxScale)
        guard scale > 1.01 else {
            scale = 1
            offset = .zero
            return
        }
        let limitX = max(0, (fitted.width * scale - size.width) / 2)
        let limitY = max(0, (fitted.height * scale - size.height) / 2)
        offset = CGSize(width: min(max(offset.width, -limitX), limitX),
                        height: min(max(offset.height, -limitY), limitY))
    }
}

/// Date, size and position of the photo on show.
private struct PhotoInfoBar: View {
    let photo: Photo
    let position: String?

    var body: some View {
        VStack(spacing: 2) {
            if let taken = photo.takenAt {
                Text(taken.formatted(.dateTime.weekday(.wide).day().month(.wide).year().hour().minute()))
                    .font(.subheadline.weight(.semibold))
            }
            Text(details)
                .font(.caption)
                .foregroundStyle(.secondary)
            if photo.kind == "video" {
                Text("Video: a still is shown; play it in Files or on the web.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
        .multilineTextAlignment(.center)
        .padding(.horizontal, 16)
        .padding(.vertical, 8)
        .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
        .padding(.bottom, 12)
        .padding(.horizontal)
    }

    private var details: String {
        var parts: [String] = []
        if let name = photo.name { parts.append(name) }
        if let w = photo.width, let h = photo.height { parts.append("\(w) × \(h)") }
        if let size = photo.size { parts.append(size.byteString) }
        if let position { parts.append(position) }
        return parts.joined(separator: " · ")
    }
}
