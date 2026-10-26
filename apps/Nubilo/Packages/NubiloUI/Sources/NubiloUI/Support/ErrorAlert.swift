import SwiftUI

extension View {
    /// Shows `message` in an alert while it is non-nil.
    func errorAlert(_ message: Binding<String?>, title: String = "Something went wrong") -> some View {
        alert(
            title,
            isPresented: Binding(get: { message.wrappedValue != nil }, set: { if !$0 { message.wrappedValue = nil } }),
            actions: { Button("OK", role: .cancel) {} },
            message: { Text(message.wrappedValue ?? "") }
        )
    }
}

/// A row that shows a label and a value that can be copied.
struct CopyableRow: View {
    let title: String
    let value: String

    var body: some View {
        LabeledContent(title) {
            HStack {
                Text(value).textSelection(.enabled).lineLimit(1).truncationMode(.middle)
                Button {
                    Clipboard.copy(value)
                } label: {
                    Image(systemName: "doc.on.doc")
                }
                .buttonStyle(.borderless)
                .accessibilityLabel("Copy \(title)")
            }
        }
    }
}
