import CoreGraphics
import Foundation

guard let list = CGWindowListCopyWindowInfo([.optionOnScreenOnly, .excludeDesktopElements], kCGNullWindowID) as? [[String: Any]] else {
    exit(1)
}
for w in list {
    guard let owner = w[kCGWindowOwnerName as String] as? String, owner.contains("DaisyDisk") else { continue }
    let id = w[kCGWindowNumber as String] as? Int ?? -1
    let bounds = w[kCGWindowBounds as String] as? [String: CGFloat] ?? [:]
    let name = w[kCGWindowName as String] as? String ?? ""
    print("id=\(id) name=\(name) x=\(bounds["X"] ?? 0) y=\(bounds["Y"] ?? 0) w=\(bounds["Width"] ?? 0) h=\(bounds["Height"] ?? 0)")
}
