import AppKit

// Window ids and logical sizes. CGWindowListCreateImage is gone from current
// macOS, but CGWindowListCopyWindowInfo -- the metadata half -- still works.
let options = CGWindowListOption(arrayLiteral: .optionAll)
guard let windows = CGWindowListCopyWindowInfo(options, kCGNullWindowID) as? [[String: Any]] else {
    print("no window list")
    exit(1)
}
for window in windows {
    let owner = window[kCGWindowOwnerName as String] as? String ?? "?"
    let name = window[kCGWindowName as String] as? String ?? ""
    let id = window[kCGWindowNumber as String] as? Int ?? -1
    guard let bounds = window[kCGWindowBounds as String] as? [String: Any] else { continue }
    let width = bounds["Width"] as? Double ?? 0
    let height = bounds["Height"] as? Double ?? 0
    if width < 200 { continue }
    let originX = bounds["X"] as? Double ?? 0
    let originY = bounds["Y"] as? Double ?? 0
    print("id=\(id)  \(Int(width))x\(Int(height))  at \(Int(originX)),\(Int(originY))  owner=\(owner)  title=\(name)")
}
