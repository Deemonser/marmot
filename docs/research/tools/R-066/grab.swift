import AppKit
import ScreenCaptureKit
import CoreImage

// Capture one window at native 2x, keeping only frames that differ from the
// previous one. R-061's three traps, all still live:
//
// 1. CGWindowListCreateImage is gone; ScreenCaptureKit replaces it, and in a
//    plain CLI process it fails with CGS_REQUIRE_INIT unless NSApplication.shared
//    is touched first to establish the window-server connection.
// 2. Change detection must walk the bitmap by bytesPerRow. SCK bitmaps have row
//    padding, so a flat stride drifts into the padding and stops seeing changes.
// 3. A still window yields one frame; the loop can therefore be left running
//    while an operation is triggered separately.
@main
struct Grab {
  static func main() async throws {
    _ = NSApplication.shared

    let arguments = CommandLine.arguments
    guard arguments.count >= 4, let windowID = UInt32(arguments[1]), let seconds = Double(arguments[2]) else {
        print("usage: grab <windowID> <seconds> <outDir>")
        exit(1)
    }
    let outDir = arguments[3]
    try? FileManager.default.createDirectory(atPath: outDir, withIntermediateDirectories: true)

    let content = try await SCShareableContent.excludingDesktopWindows(false, onScreenWindowsOnly: false)
    guard let window = content.windows.first(where: { $0.windowID == windowID }) else {
        print("window \(windowID) not shareable")
        exit(1)
    }

    let configuration = SCStreamConfiguration()
    configuration.width = Int(window.frame.width * 2)
    configuration.height = Int(window.frame.height * 2)
    configuration.showsCursor = false
    let filter = SCContentFilter(desktopIndependentWindow: window)

    // Sample a coarse grid rather than every pixel: enough to catch any visible
    // change, cheap enough not to become the bottleneck of the capture loop itself
    // (R-061 §5 noted the loop was among the machine's top CPU consumers).
    func signature(_ image: CGImage) -> [UInt8] {
        guard let data = image.dataProvider?.data, let bytes = CFDataGetBytePtr(data) else { return [] }
        let rowBytes = image.bytesPerRow
        var out: [UInt8] = []
        out.reserveCapacity(64 * 64)
        let stepY = max(1, image.height / 64)
        let stepX = max(1, image.width / 64)
        var y = 0
        while y < image.height {
            var x = 0
            while x < image.width {
                out.append(bytes[y * rowBytes + x * 4])
                x += stepX
            }
            y += stepY
        }
        return out
    }

    let started = Date()
    var previous: [UInt8] = []
    var saved = 0
    var frames = 0
    while Date().timeIntervalSince(started) < seconds {
        guard let image = try? await SCScreenshotManager.captureImage(contentFilter: filter, configuration: configuration) else {
            continue
        }
        frames += 1
        let current = signature(image)
        if current == previous { continue }
        previous = current
        let elapsed = Int(Date().timeIntervalSince(started) * 1000)
        let path = "\(outDir)/f\(String(format: "%05d", elapsed)).png"
        let bitmap = NSBitmapImageRep(cgImage: image)
        if let png = bitmap.representation(using: .png, properties: [:]) {
            try? png.write(to: URL(fileURLWithPath: path))
            saved += 1
        }
    }
    print("frames=\(frames) saved=\(saved) fps=\(String(format: "%.1f", Double(frames) / seconds))")

  }
}
