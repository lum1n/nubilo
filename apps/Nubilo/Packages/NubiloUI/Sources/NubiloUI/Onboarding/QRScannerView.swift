#if os(iOS)
import SwiftUI
import AVFoundation
import Vision
import VisionKit

/// The live scanner when the device supports it, otherwise an explanation.
struct QRScannerSection: View {
    let onCode: (String) -> Void
    @State private var cameraAllowed: Bool?

    var body: some View {
        Group {
            switch cameraAllowed {
            case nil:
                ProgressView()
            case false?:
                Text("Camera access is off. Allow it in Settings → Nubilo → Camera, or paste the link below.")
                    .foregroundStyle(.secondary)
            case true?:
                if DataScannerViewController.isSupported && DataScannerViewController.isAvailable {
                    QRScannerView(onCode: onCode)
                        .frame(height: 300)
                        .clipShape(RoundedRectangle(cornerRadius: 12))
                } else {
                    Text("This device cannot scan codes. Paste the link below instead.")
                        .foregroundStyle(.secondary)
                }
            }
        }
        .task {
            // DataScannerViewController.isAvailable stays false until access is granted.
            switch AVCaptureDevice.authorizationStatus(for: .video) {
            case .authorized: cameraAllowed = true
            case .notDetermined: cameraAllowed = await AVCaptureDevice.requestAccess(for: .video)
            default: cameraAllowed = false
            }
        }
    }
}

/// VisionKit's DataScannerViewController limited to QR codes.
struct QRScannerView: UIViewControllerRepresentable {
    let onCode: (String) -> Void

    func makeUIViewController(context: Context) -> DataScannerViewController {
        let scanner = DataScannerViewController(
            recognizedDataTypes: [.barcode(symbologies: [.qr])],
            qualityLevel: .balanced,
            recognizesMultipleItems: false,
            isHighlightingEnabled: true
        )
        scanner.delegate = context.coordinator
        return scanner
    }

    func updateUIViewController(_ scanner: DataScannerViewController, context: Context) {
        context.coordinator.onCode = onCode
        if !scanner.isScanning {
            try? scanner.startScanning()
        }
    }

    static func dismantleUIViewController(_ scanner: DataScannerViewController, coordinator: Coordinator) {
        scanner.stopScanning()
    }

    func makeCoordinator() -> Coordinator { Coordinator(onCode: onCode) }

    @MainActor
    final class Coordinator: NSObject, DataScannerViewControllerDelegate {
        var onCode: (String) -> Void
        // Each distinct code is reported once, so an invalid code does not
        // re-trigger its error while the camera keeps seeing it.
        private var lastText: String?

        init(onCode: @escaping (String) -> Void) { self.onCode = onCode }

        func dataScanner(_ dataScanner: DataScannerViewController, didAdd addedItems: [RecognizedItem], allItems: [RecognizedItem]) {
            for item in addedItems {
                if case let .barcode(code) = item, let text = code.payloadStringValue, text != lastText {
                    lastText = text
                    onCode(text)
                    return
                }
            }
        }
    }
}
#endif
