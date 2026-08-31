import AppKit

// Synthetic click at a screen point, so an animation can be triggered at a known
// moment while the capture loop is already running.
let arguments = CommandLine.arguments
guard arguments.count >= 3, let x = Double(arguments[1]), let y = Double(arguments[2]) else {
    print("usage: click <x> <y>")
    exit(1)
}
// Optional pre-delay so the click can be fired at a known offset into a capture
// that is already running.
if arguments.count >= 4, let delayMs = Double(arguments[3]) {
    usleep(useconds_t(delayMs * 1000))
}
let point = CGPoint(x: x, y: y)
let down = CGEvent(mouseEventSource: nil, mouseType: .leftMouseDown, mouseCursorPosition: point, mouseButton: .left)
let up = CGEvent(mouseEventSource: nil, mouseType: .leftMouseUp, mouseCursorPosition: point, mouseButton: .left)
CGWarpMouseCursorPosition(point)
usleep(60_000)
down?.post(tap: .cghidEventTap)
usleep(40_000)
up?.post(tap: .cghidEventTap)
print("clicked \(x),\(y)")
