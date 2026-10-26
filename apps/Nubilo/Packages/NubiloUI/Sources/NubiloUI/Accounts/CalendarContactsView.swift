import SwiftUI
import NubiloKit

/// Adds the server's CalDAV/CardDAV accounts to the system through a
/// configuration profile, so Calendar, Contacts and Reminders sync natively.
struct CalendarContactsView: View {
    @Environment(AppModel.self) private var model
    @State private var working = false
    @State private var opened = false
    @State private var error: String?

    var body: some View {
        Form {
            Section {
                Text("Nubilo keeps your calendars, contacts and reminders on your own server. The system apps sync them directly once the accounts are added, and keep working when this app is closed.")
                Text("Tapping the button creates a one-time profile with a new app password for this device and opens it in \(browserName). The link works once and expires after 15 minutes.")
                    .foregroundStyle(.secondary)
            }
            Section {
                Button {
                    Task { await createProfile() }
                } label: {
                    HStack {
                        Text("Set up Calendar, Contacts & Reminders")
                        if working { Spacer(); ProgressView() }
                    }
                }
                .disabled(working || model.client == nil)
            }
            Section(opened ? "Now finish in Settings" : "What happens next") {
                ForEach(Array(Self.steps.enumerated()), id: \.offset) { index, step in
                    HStack(alignment: .firstTextBaseline, spacing: 12) {
                        Text("\(index + 1).").monospacedDigit().foregroundStyle(.secondary)
                        Text(step)
                    }
                }
            }
            Section {
                Text("The profile is not signed, so it is shown as “Unverified”. That is expected for a self-hosted server.")
                    .font(.footnote)
                    .foregroundStyle(.secondary)
            }
        }
        .formStyle(.grouped)
        .navigationTitle("Calendar & Contacts")
        .errorAlert($error)
    }

    private var browserName: String {
        #if os(iOS)
        "Safari"
        #else
        "your browser"
        #endif
    }

    #if os(iOS)
    static let steps = [
        "Safari asks to download a configuration profile. Tap Allow, then Close.",
        "Open the Settings app. Tap “Profile Downloaded” near the top (or General → VPN & Device Management).",
        "Tap Install, enter your passcode, and confirm Install again.",
        "Open Calendar, Contacts or Reminders: the Nubilo accounts appear after a moment.",
    ]
    #else
    static let steps = [
        "Your browser downloads the profile. Open it from Downloads.",
        "Open System Settings → Privacy & Security → Profiles (or General → Device Management).",
        "Double-click the Nubilo profile and click Install.",
        "Open Calendar, Contacts or Reminders: the Nubilo accounts appear after a moment.",
    ]
    #endif

    private func createProfile() async {
        guard let client = model.client else { return }
        working = true
        defer { working = false }
        do {
            let profile = try await client.createProfile()
            guard let url = URL(string: profile.url) else { throw NubiloError.invalidResponse }
            ExternalURL.open(url)
            opened = true
        } catch {
            self.error = describe(error)
        }
    }
}
