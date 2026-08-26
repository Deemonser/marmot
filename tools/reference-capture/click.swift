import AppKit
import CoreGraphics
import Foundation

// Activates the target app, then posts one click. Used to trigger the animation
// on a known schedule so the frame timestamps mean something.
let args = CommandLine.arguments
guard args.count >= 4, let x = Double(args[1]), let y = Double(args[2]) else {
    FileHandle.standardError.write("usage: click <x> <y> <bundleID>\n".data(using: .utf8)!)
    exit(2)
}
let bundleID = args[3]
// Activation is opt-in. Activating on every click meant the first click after
// focus moved was consumed bringing the window forward, so the interaction never
// happened -- which read as "synthetic clicks do not work".
if bundleID != "-" {
    if let app = NSRunningApplication.runningApplications(withBundleIdentifier: bundleID).first {
        app.activate(options: [])
        usleep(600_000)
    }
}
let point = CGPoint(x: x, y: y)
let down = CGEvent(mouseEventSource: nil, mouseType: .leftMouseDown, mouseCursorPosition: point, mouseButton: .left)
let up = CGEvent(mouseEventSource: nil, mouseType: .leftMouseUp, mouseCursorPosition: point, mouseButton: .left)
CGEvent(mouseEventSource: nil, mouseType: .mouseMoved, mouseCursorPosition: point, mouseButton: .left)?.post(tap: .cghidEventTap)
usleep(40_000)
down?.post(tap: .cghidEventTap)
usleep(30_000)
up?.post(tap: .cghidEventTap)
print("clicked at \(Int(x)),\(Int(y))")
