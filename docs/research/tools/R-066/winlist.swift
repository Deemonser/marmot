import AppKit

// Window ids and logical sizes. CGWindowListCreateImage is gone from current
// macOS, but CGWindowListCopyWindowInfo -- the metadata half -- still works.
// --geom <owner> prints "W H X Y id" for the first matching titled window, so a
// script never has to take apart the human-readable line.
let geomOwner: String? = CommandLine.arguments.count >= 3 && CommandLine.arguments[1] == "--geom"
    ? CommandLine.arguments[2] : nil
// --win <id> is the reliable form: an app can own several windows with the same
// title, and matching by owner picked a stale 848x152 one while the live window
// was 848x633.
let wantedID: Int? = CommandLine.arguments.count >= 3 && CommandLine.arguments[1] == "--win"
    ? Int(CommandLine.arguments[2]) : nil
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
    if let target = wantedID {
        if id == target, let bounds = window[kCGWindowBounds as String] as? [String: Any] {
            print("\(Int(bounds["Width"] as? Double ?? 0)) \(Int(bounds["Height"] as? Double ?? 0)) \(Int(bounds["X"] as? Double ?? 0)) \(Int(bounds["Y"] as? Double ?? 0))")
            exit(0)
        }
        continue
    }
    if let wanted = geomOwner {
        if owner == wanted && !name.isEmpty && width > 200 {
            let originX = bounds["X"] as? Double ?? 0
            let originY = bounds["Y"] as? Double ?? 0
            print("\(Int(width)) \(Int(height)) \(Int(originX)) \(Int(originY)) \(id)")
            exit(0)
        }
        continue
    }
    if width < 200 { continue }
    let originX = bounds["X"] as? Double ?? 0
    let originY = bounds["Y"] as? Double ?? 0
    print("id=\(id)  \(Int(width))x\(Int(height))  at \(Int(originX)),\(Int(originY))  owner=\(owner)  title=\(name)")
}
