import SwiftUI
import NubiloKit

/// Photos grouped by day, newest first, with a timeline scrubber on the
/// trailing edge and a menu to jump to a month. Pinch (or ⌘+ / ⌘− on the Mac)
/// changes the tile size. With a `selection` binding, taps toggle selection
/// instead of opening the photo.
struct PhotoGrid: View {
    @Bindable var pager: PhotoPager
    var selection: Binding<Set<String>>?

    @AppStorage("photos.tileSize") private var tileSize = PhotoGrid.tileSizes[1]
    @State private var scroll = TimelineScrollState()

    static let tileSizes: [Double] = [56, 96, 140, 200, 280]

    private var timeline: PhotoTimeline { pager.timeline }

    var body: some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 0, pinnedViews: .sectionHeaders) {
                    ForEach(timeline.days) { day in
                        Section {
                            LazyVGrid(columns: [GridItem(.adaptive(minimum: tileSize), spacing: 2)], spacing: 2) {
                                ForEach(day.photos) { cell($0) }
                            }
                            .padding(.bottom, 2)
                        } header: {
                            DayHeader(day: day, selection: selection)
                                .id(day.id)
                                .onAppear { scroll.visible.insert(day.id) }
                                .onDisappear { scroll.visible.remove(day.id) }
                        }
                    }
                }
                LoadStatus(pager: pager)
            }
            .overlay(alignment: .trailing) {
                if timeline.months.count > 1 {
                    TimelineScrubber(timeline: timeline, scroll: scroll) { month in
                        proxy.scrollTo(month.firstDayID, anchor: .top)
                    }
                }
            }
            .toolbar {
                ToolbarItem(placement: .primaryAction) {
                    JumpMenu(timeline: timeline) { month in
                        proxy.scrollTo(month.firstDayID, anchor: .top)
                    }
                }
                #if os(macOS)
                ToolbarItem(placement: .primaryAction) {
                    ControlGroup {
                        Button { zoom(by: -1) } label: { Label("Smaller", systemImage: "minus.magnifyingglass") }
                            .keyboardShortcut("-")
                            .disabled(tileSize <= Self.tileSizes.first!)
                        Button { zoom(by: 1) } label: { Label("Larger", systemImage: "plus.magnifyingglass") }
                            .keyboardShortcut("+")
                            .disabled(tileSize >= Self.tileSizes.last!)
                    }
                }
                #endif
            }
        }
        .simultaneousGesture(
            MagnifyGesture().onEnded { value in
                if value.magnification > 1.15 { zoom(by: 1) }
                if value.magnification < 0.87 { zoom(by: -1) }
            }
        )
        .overlay {
            if pager.photos.isEmpty {
                if pager.exhausted {
                    ContentUnavailableView("No photos yet", systemImage: "photo.on.rectangle",
                                           description: Text("Photos backed up from your devices appear here."))
                } else if pager.loading {
                    ProgressView()
                }
            }
        }
        .refreshable { await pager.reload() }
        .errorAlert($pager.error)
    }

    private func zoom(by step: Int) {
        let sizes = Self.tileSizes
        let current = sizes.firstIndex { $0 >= tileSize } ?? sizes.count - 1
        let next = min(max(current + step, 0), sizes.count - 1)
        withAnimation(.snappy) { tileSize = sizes[next] }
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

/// Which day headers are on screen, so the scrubber knows where the grid is.
/// Only the scrubber reads it: scrolling does not redraw the grid.
@MainActor
@Observable
final class TimelineScrollState {
    var visible: Set<String> = []

    func topDay(in timeline: PhotoTimeline) -> String? {
        visible.min { (timeline.index(ofDay: $0) ?? .max) < (timeline.index(ofDay: $1) ?? .max) }
    }
}

/// The pinned title of one day, with select and deselect while selecting.
private struct DayHeader: View {
    let day: PhotoTimeline.Day
    var selection: Binding<Set<String>>?

    var body: some View {
        HStack(alignment: .firstTextBaseline) {
            Text(PhotoDates.title(of: day))
                .font(.headline)
            Text(day.photos.count, format: .number)
                .font(.subheadline)
                .foregroundStyle(.secondary)
                .accessibilityLabel("\(day.photos.count) photos")
            Spacer()
            if let selection {
                let ids = day.photos.map(\.id)
                let all = ids.allSatisfy(selection.wrappedValue.contains)
                Button(all ? "Deselect" : "Select") {
                    if all {
                        selection.wrappedValue.subtract(ids)
                    } else {
                        selection.wrappedValue.formUnion(ids)
                    }
                }
                .font(.subheadline)
                .buttonStyle(.borderless)
            }
        }
        // Room for the scrubber on the trailing edge.
        .padding(.leading, 12)
        .padding(.trailing, 36)
        .padding(.vertical, 8)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(.bar)
        .accessibilityAddTraits(.isHeader)
    }
}

/// Progress while the rest of the library loads, and a retry when it stopped.
private struct LoadStatus: View {
    let pager: PhotoPager

    var body: some View {
        if !pager.photos.isEmpty && !pager.exhausted {
            HStack(spacing: 8) {
                if pager.loading {
                    ProgressView().controlSize(.small)
                    if pager.isPaging {
                        Text("Loading library… \(pager.photos.count.formatted()) photos so far")
                    } else {
                        Text("Checking for new photos…")
                    }
                } else {
                    Text("Not every photo has loaded.")
                    Button("Load the Rest") { pager.start() }
                        .buttonStyle(.borderless)
                }
            }
            .font(.footnote)
            .foregroundStyle(.secondary)
            .frame(maxWidth: .infinity)
            .padding()
        }
    }
}

/// Years, then months, with photo counts.
private struct JumpMenu: View {
    let timeline: PhotoTimeline
    let jump: (PhotoTimeline.Month) -> Void

    var body: some View {
        Menu {
            ForEach(timeline.years) { year in
                Menu("\(String(year.year)) (\(year.count.formatted()))") {
                    ForEach(year.months) { month in
                        Button("\(PhotoDates.monthName(of: month)) (\(month.count.formatted()))") { jump(month) }
                    }
                }
            }
            if let undated = timeline.months.last, undated.id == PhotoTimeline.undatedID {
                Button("No Date (\(undated.count.formatted()))") { jump(undated) }
            }
        } label: {
            Label("Jump to Date", systemImage: "calendar")
        }
        .disabled(timeline.months.isEmpty)
    }
}

/// A strip along the trailing edge: drag it to move through the years. Years
/// are spaced by how many photos they hold, like the grid itself.
private struct TimelineScrubber: View {
    let timeline: PhotoTimeline
    let scroll: TimelineScrollState
    let jump: (PhotoTimeline.Month) -> Void

    @State private var dragged: PhotoTimeline.Month?
    @State private var dragY: CGFloat = 0
    @State private var hovering = false

    private let inset: CGFloat = 16
    private let handleHeight: CGFloat = 32

    var body: some View {
        GeometryReader { geo in
            let track = max(geo.size.height - 2 * inset, 1)
            let current = scroll.topDay(in: timeline).flatMap(timeline.month(containing:))
            let handleY = dragged != nil
                ? dragY
                : inset + track * (current.map(timeline.fraction(of:)) ?? 0)
            let expanded = dragged != nil || hovering

            ZStack(alignment: .topTrailing) {
                if expanded {
                    ForEach(yearMarks(track: track)) { mark in
                        Text(String(mark.year))
                            .font(.caption2.weight(.semibold).monospacedDigit())
                            .padding(.horizontal, 6)
                            .padding(.vertical, 2)
                            .background(.regularMaterial, in: Capsule())
                            .offset(x: -30, y: mark.y - 8)
                    }
                    .transition(.opacity)
                    .allowsHitTesting(false)
                }

                // Only the handle takes touches: the rest of the strip is
                // over the grid's last column.
                Image(systemName: "chevron.up.chevron.down")
                    .font(.caption.weight(.bold))
                    .foregroundStyle(.secondary)
                    .frame(width: 24, height: handleHeight)
                    .background(.regularMaterial, in: Capsule())
                    .shadow(color: .black.opacity(0.15), radius: 2, y: 1)
                    .padding(.horizontal, 4)
                    .padding(.vertical, 6)
                    .contentShape(Rectangle())
                    .gesture(scrub(track: track))
                    .onHover { inside in withAnimation { hovering = inside } }
                    .accessibilityElement()
                    .accessibilityLabel("Timeline")
                    .accessibilityValue(current.map(PhotoDates.monthTitle(of:)) ?? "")
                    .accessibilityAdjustableAction { direction in
                        guard let current, let i = timeline.months.firstIndex(of: current) else { return }
                        let next = direction == .increment ? i + 1 : i - 1
                        if timeline.months.indices.contains(next) { jump(timeline.months[next]) }
                    }
                    .offset(y: handleY - handleHeight / 2 - 6)

                if let dragged {
                    Text(PhotoDates.monthTitle(of: dragged))
                        .font(.headline)
                        .fixedSize()
                        .padding(.horizontal, 12)
                        .padding(.vertical, 6)
                        .background(.regularMaterial, in: Capsule())
                        .shadow(color: .black.opacity(0.15), radius: 4, y: 2)
                        .offset(x: -40, y: handleY - 16)
                        .allowsHitTesting(false)
                }
            }
            .frame(width: geo.size.width, height: geo.size.height, alignment: .topTrailing)
            .coordinateSpace(.named(Self.space))
        }
        .frame(width: 32)
        #if os(iOS)
        .sensoryFeedback(.selection, trigger: dragged?.id)
        #endif
    }

    private static let space = "timelineScrubber"

    /// Dragging the handle: its position on the strip picks the month.
    private func scrub(track: CGFloat) -> some Gesture {
        DragGesture(minimumDistance: 0, coordinateSpace: .named(Self.space))
            .onChanged { value in
                let y = min(max(value.location.y, inset), inset + track)
                dragY = y
                guard let month = timeline.month(atFraction: Double((y - inset) / track)) else { return }
                if month.id != dragged?.id {
                    withAnimation(.easeOut(duration: 0.15)) { dragged = month }
                    jump(month)
                }
            }
            .onEnded { _ in
                withAnimation(.easeOut(duration: 0.3)) { dragged = nil }
            }
    }

    private struct YearMark: Identifiable {
        var year: Int
        var y: CGFloat
        var id: Int { year }
    }

    /// One mark per year, dropping years that would overlap the one above.
    private func yearMarks(track: CGFloat) -> [YearMark] {
        var marks: [YearMark] = []
        for year in timeline.years {
            guard let first = year.months.first else { continue }
            let y = inset + track * timeline.fraction(of: first)
            if let last = marks.last, y - last.y < 20 { continue }
            marks.append(YearMark(year: year.year, y: y))
        }
        return marks
    }
}

/// How the photo screens write dates.
enum PhotoDates {
    static func title(of day: PhotoTimeline.Day, calendar: Calendar = .current) -> String {
        guard let date = day.date else { return "No Date" }
        if calendar.isDateInToday(date) { return "Today" }
        if calendar.isDateInYesterday(date) { return "Yesterday" }
        if calendar.isDate(date, equalTo: .now, toGranularity: .year) {
            return date.formatted(.dateTime.weekday(.wide).day().month(.wide))
        }
        return date.formatted(.dateTime.weekday(.abbreviated).day().month(.wide).year())
    }

    static func monthName(of month: PhotoTimeline.Month) -> String {
        month.date?.formatted(.dateTime.month(.wide)) ?? "No Date"
    }

    static func monthTitle(of month: PhotoTimeline.Month) -> String {
        month.date?.formatted(.dateTime.month(.wide).year()) ?? "No Date"
    }
}

struct PhotoTile: View {
    let photo: Photo

    var body: some View {
        Color.clear
            .aspectRatio(1, contentMode: .fit)
            .overlay { RemoteImage(photo: photo) }
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
