// swift-tools-version:5.9
import PackageDescription

let package = Package(
    name: "NubiloKit",
    platforms: [.iOS(.v17), .macOS(.v14)],
    products: [
        .library(name: "NubiloKit", targets: ["NubiloKit"]),
    ],
    dependencies: [
        // CryptoKit's API on Linux, so the client core builds and tests off Apple platforms.
        .package(url: "https://github.com/apple/swift-crypto.git", "3.0.0"..<"5.0.0"),
    ],
    targets: [
        .target(
            name: "NubiloKit",
            dependencies: [
                .product(name: "Crypto", package: "swift-crypto", condition: .when(platforms: [.linux])),
            ]
        ),
        .testTarget(name: "NubiloKitTests", dependencies: ["NubiloKit"]),
    ]
)
