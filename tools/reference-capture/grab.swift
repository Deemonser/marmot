import AppKit
import CoreGraphics
import Foundation
import ImageIO
import ScreenCaptureKit
import UniformTypeIdentifiers

// Grabs one window repeatedly for a fixed span. A frame sequence is the only way
// to read an animation's duration and easing off the real thing: a video would be
// recompressed, and a handful of manual screenshots land at the wrong moments.
let args = CommandLine.arguments
guard args.count >= 4, let windowID = UInt32(args[1]), let seconds = Double(args[2]) else {
    FileHandle.standardError.write("usage: grab <windowID> <seconds> <outDir>\n".data(using: .utf8)!)
    exit(2)
}
let outDir = args[3]
try? FileManager.default.createDirectory(atPath: outDir, withIntermediateDirectories: true)

// A plain CLI tool has no window-server connection, and ScreenCaptureKit trips
// CGS_REQUIRE_INIT without one. Touching NSApplication establishes it.
_ = NSApplication.shared

let done = DispatchSemaphore(value: 0)
Task {
    do {
        let content = try await SCShareableContent.excludingDesktopWindows(false, onScreenWindowsOnly: true)
        guard let window = content.windows.first(where: { $0.windowID == windowID }) else {
            FileHandle.standardError.write("window \(windowID) not found\n".data(using: .utf8)!)
            exit(3)
        }
        let filter = SCContentFilter(desktopIndependentWindow: window)
        let config = SCStreamConfiguration()
        // 2x: the measurement tooling is calibrated against native-resolution
        // captures, and change detection means idle frames cost nothing, so the
        // larger buffer only applies to the frames that actually move.
        // Always 2x. Every measurement tool here is calibrated against native
        // resolution, and switching to 1x for "timing only" runs cost more in
        // coordinate confusion than it saved in memory. Short captures keep the
        // buffer bounded instead.
        config.width = Int(window.frame.width * 2)
        config.height = Int(window.frame.height * 2)
        config.showsCursor = false
        config.captureResolution = .best

        // Frames are buffered and written afterwards: encoding a 2166x1490 PNG
        // inside the loop held the rate to 8fps, which is three frames of a
        // 400ms animation. Capped so the buffer cannot run away.
        // Only frames that differ from the last kept one are stored. An idle
        // window produces identical frames, so a long capture window costs
        // almost nothing and the operator can act whenever they like instead of
        // racing a countdown.
        let maxFrames = 260
        var frames: [CGImage] = []
        var stamps: [Double] = []
        frames.reserveCapacity(maxFrames)
        var lastSignature: [UInt8] = []
        let start = Date()
        // Rate limited rather than a busy loop. Capturing flat out put this
        // process among the top CPU consumers on an already loaded machine, which
        // makes it an observer that changes what it observes: the four durations
        // measured that way differed, and the differences may have been the
        // measurement's own load rather than the animation.
        let interval = 0.016
        var nextAt = Date()
        while Date().timeIntervalSince(start) < seconds && frames.count < maxFrames {
            let wait = nextAt.timeIntervalSinceNow
            if wait > 0 { try await Task.sleep(nanoseconds: UInt64(wait * 1_000_000_000)) }
            nextAt = Date().addingTimeInterval(interval)
            let image = try await SCScreenshotManager.captureImage(contentFilter: filter, configuration: config)
            guard let data = image.dataProvider?.data as Data? else { continue }
            // Sampled as a grid using bytesPerRow. A flat stride over the buffer
            // ignores each row's alignment padding and drifts into it, which is
            // why this missed nearly every frame at one resolution and worked by
            // luck at another.
            let bytesPerRow = image.bytesPerRow
            let width = image.width
            let height = image.height
            let step = 24
            var signature: [UInt8] = []
            signature.reserveCapacity((width / step + 1) * (height / step + 1))
            var row = 0
            while row < height {
                var column = 0
                while column < width {
                    let offset = row * bytesPerRow + column * 4 + 1 // green channel
                    if offset < data.count {
                        signature.append(data[offset])
                    }
                    column += step
                }
                row += step
            }
            if signature == lastSignature {
                continue
            }
            lastSignature = signature
            stamps.append(Date().timeIntervalSince(start))
            frames.append(image)
        }
        let captured = Date().timeIntervalSince(start)
        for (index, image) in frames.enumerated() {
            let path = String(format: "%@/f%04d.png", outDir, index)
            if let dest = CGImageDestinationCreateWithURL(URL(fileURLWithPath: path) as CFURL,
                                                          UTType.png.identifier as CFString, 1, nil) {
                CGImageDestinationAddImage(dest, image, nil)
                CGImageDestinationFinalize(dest)
            }
        }
        let lines = stamps.enumerated().map { String(format: "f%04d\t%.4f", $0.offset, $0.element) }
        try? lines.joined(separator: "\n").write(toFile: outDir + "/stamps.tsv", atomically: true, encoding: .utf8)
        if stamps.count > 1 {
            let fps = Double(stamps.count - 1) / (stamps.last! - stamps.first!)
            print(String(format: "captured %d frames over %.2fs = %.1f fps (written in %.1fs)",
                         stamps.count, captured, fps, Date().timeIntervalSince(start) - captured))
        }
        done.signal()
    } catch {
        FileHandle.standardError.write("capture failed: \(error)\n".data(using: .utf8)!)
        exit(4)
    }
}
done.wait()
