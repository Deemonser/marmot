import AppKit

// Poll one window's frame at high frequency. The page transition in the
// reference turns out to be a window resize, so the curve to copy is height
// against time -- which needs no pixels at all.
let arguments = CommandLine.arguments
guard arguments.count >= 3, let target = Int(arguments[1]), let seconds = Double(arguments[2]) else {
    print("usage: height <windowID> <seconds>")
    exit(1)
}
let options = CGWindowListOption(arrayLiteral: .optionAll)
let started = Date()
var last = -1.0
while Date().timeIntervalSince(started) < seconds {
    guard let windows = CGWindowListCopyWindowInfo(options, kCGNullWindowID) as? [[String: Any]] else { break }
    for window in windows {
        guard (window[kCGWindowNumber as String] as? Int) == target,
              let bounds = window[kCGWindowBounds as String] as? [String: Any] else { continue }
        let height = bounds["Height"] as? Double ?? 0
        let y = bounds["Y"] as? Double ?? 0
        if height != last {
            last = height
            print("\(Int(Date().timeIntervalSince(started) * 1000))\t\(height)\t\(y)")
        }
    }
    usleep(6000)
}
