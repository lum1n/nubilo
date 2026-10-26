import SwiftUI
import CoreImage
import CoreImage.CIFilterBuiltins

/// A QR code for `text`, rendered with Core Image and scaled without smoothing.
struct QRCodeImage: View {
    let text: String

    var body: some View {
        if let cg = Self.render(text) {
            Image(decorative: cg, scale: 1)
                .interpolation(.none)
                .resizable()
                .scaledToFit()
                .accessibilityLabel("QR code")
        } else {
            Label("Could not draw the QR code", systemImage: "exclamationmark.triangle")
        }
    }

    private static let context = CIContext()

    static func render(_ text: String) -> CGImage? {
        let filter = CIFilter.qrCodeGenerator()
        filter.message = Data(text.utf8)
        filter.correctionLevel = "M"
        guard let output = filter.outputImage else { return nil }
        let scaled = output.transformed(by: CGAffineTransform(scaleX: 10, y: 10))
        return context.createCGImage(scaled, from: scaled.extent)
    }
}
