import SwiftUI
import NubiloKit

/// Approves a browser sign-in at <server>/web/ and lists signed-in browsers.
struct WebSignInView: View {
    /// A code from a scanned QR code or opened link, if any.
    var initialCode: String?

    @Environment(AppModel.self) private var model
    @State private var typed = ""
    @State private var request: WebSignInRequest?
    @State private var sessions: [WebSession] = []
    @State private var checking = false
    @State private var approved = false
    @State private var scanning = false
    @State private var error: String?

    var body: some View {
        Form {
            if let request {
                approveSection(request)
            } else {
                Section {
                    TextField("Code, e.g. ABCD-EFGH", text: $typed)
                        .font(.body.monospaced())
                        .autocorrectionDisabled()
                        #if os(iOS)
                        .textInputAutocapitalization(.characters)
                        #endif
                        .onSubmit { Task { await look(up: typed) } }
                    Button("Continue") { Task { await look(up: typed) } }
                        .disabled(WebSignInLink(parsing: typed) == nil || checking)
                    #if os(iOS)
                    Button("Scan Code") { scanning = true }
                    #endif
                } header: {
                    Text("Sign in on a computer")
                } footer: {
                    Text("Open \(webURL) in a browser. It shows a code; scan it or type it here.")
                }
            }
            if approved {
                Section {
                    Label("Signed in. The browser continues by itself.", systemImage: "checkmark.circle.fill")
                        .foregroundStyle(.green)
                }
            }
            Section {
                if sessions.isEmpty {
                    Text("No browsers are signed in.").foregroundStyle(.secondary)
                }
                ForEach(sessions) { session in
                    HStack {
                        VStack(alignment: .leading, spacing: 2) {
                            Text(describeBrowser(session.userAgent))
                            Text("\(session.ip) · active \(session.lastSeen.msDate.relative)")
                                .font(.caption).foregroundStyle(.secondary)
                        }
                        Spacer()
                        Button("Sign Out", role: .destructive) { Task { await end(session) } }
                            .buttonStyle(.borderless)
                    }
                }
            } header: {
                Text("Signed-in browsers")
            } footer: {
                Text("A browser has the same access as the device that approved it, and is signed out when that device is removed.")
            }
        }
        .formStyle(.grouped)
        .navigationTitle("Sign In on the Web")
        #if os(iOS)
        .sheet(isPresented: $scanning) {
            NavigationStack {
                Form {
                    QRScannerSection { text in
                        scanning = false
                        Task { await look(up: text) }
                    }
                }
                .navigationTitle("Scan Code")
                .toolbar {
                    ToolbarItem(placement: .cancellationAction) { Button("Cancel") { scanning = false } }
                }
            }
        }
        #endif
        .task {
            if let initialCode { await look(up: initialCode) }
            await loadSessions()
        }
        .refreshable { await loadSessions() }
        .errorAlert($error)
    }

    private var webURL: String {
        let base = model.me?.publicUrl.flatMap { $0.isEmpty ? nil : $0 } ?? model.client?.baseURL.absoluteString ?? "your server"
        return base.trimmingCharacters(in: CharacterSet(charactersIn: "/")) + "/web/"
    }

    @ViewBuilder
    private func approveSection(_ request: WebSignInRequest) -> some View {
        Section {
            LabeledContent("Browser", value: describeBrowser(request.userAgent))
            LabeledContent("From", value: request.ip)
            LabeledContent("Started", value: request.createdAt.msDate.relative)
            LabeledContent("Code", value: request.code).font(.body.monospaced())
            Button("Sign In as \(model.me?.user.displayName ?? "Me")") { Task { await approve(request) } }
                .buttonStyle(.borderedProminent)
                .disabled(checking)
            Button("Cancel", role: .cancel) { self.request = nil; typed = "" }
        } header: {
            Text("Allow this browser?")
        } footer: {
            Text("Only allow it if you started this sign-in yourself, on a computer in front of you. Someone who sends you a code is trying to get into your account.")
        }
    }

    private func look(up text: String) async {
        guard let client = model.client else { return }
        guard let link = WebSignInLink(parsing: text) else {
            error = "That is not a sign-in code."
            return
        }
        if let server = link.server, !isThisServer(server) {
            error = "This code is for another server (\(server.host() ?? server.absoluteString))."
            return
        }
        checking = true
        defer { checking = false }
        do {
            request = try await client.webSignIn(code: link.code)
            approved = false
        } catch NubiloError.http(404, _) {
            error = "That code is unknown or has expired. Reload the page in the browser for a new one."
        } catch {
            self.error = describe(error)
        }
    }

    private func isThisServer(_ url: URL) -> Bool {
        var hosts: Set<String> = []
        if let h = model.client?.baseURL.host() { hosts.insert(h.lowercased()) }
        if let p = model.me?.publicUrl, let h = URL(string: p)?.host() { hosts.insert(h.lowercased()) }
        return url.host().map { hosts.contains($0.lowercased()) } ?? false
    }

    private func approve(_ request: WebSignInRequest) async {
        guard let client = model.client else { return }
        checking = true
        defer { checking = false }
        do {
            try await client.approveWebSignIn(code: request.code)
            self.request = nil
            typed = ""
            approved = true
            try? await Task.sleep(for: .seconds(3))
            await loadSessions()
        } catch NubiloError.http(404, _) {
            error = "That code has expired. Reload the page in the browser for a new one."
            self.request = nil
        } catch {
            self.error = describe(error)
        }
    }

    private func loadSessions() async {
        guard let client = model.client else { return }
        do {
            sessions = try await client.webSessions()
        } catch {
            self.error = describe(error)
        }
    }

    private func end(_ session: WebSession) async {
        guard let client = model.client else { return }
        do {
            try await client.endWebSession(session.id)
            await loadSessions()
        } catch {
            self.error = describe(error)
        }
    }
}
