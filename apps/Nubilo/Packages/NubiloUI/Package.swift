// swift-tools-version:5.9
import PackageDescription

// SwiftUI screens of the Nubilo app. Apple platforms only (not built on Linux).
let package = Package(
    name: "NubiloUI",
    platforms: [.iOS(.v17), .macOS(.v14)],
    products: [
        .library(name: "NubiloUI", targets: ["NubiloUI"]),
    ],
    dependencies: [
        .package(path: "../NubiloKit"),
    ],
    targets: [
        .target(
            name: "NubiloUI",
            dependencies: [.product(name: "NubiloKit", package: "NubiloKit")]
        ),
    ]
)
