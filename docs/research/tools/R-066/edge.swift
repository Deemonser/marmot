import AppKit

// Track one vertical edge across a frame sequence. The reference's page change
// pushes the content sideways, so the incoming panel's right edge is a landmark
// whose x against time IS the curve.
let arguments = CommandLine.arguments
guard arguments.count >= 4, let bandTop = Int(arguments[2]), let bandBottom = Int(arguments[3]) else {
    print("usage: edge <dir> <bandTop> <bandBottom>")
    exit(1)
}
let dir = arguments[1]
let files = ((try? FileManager.default.contentsOfDirectory(atPath: dir)) ?? [])
    .filter { $0.hasSuffix(".png") }.sorted()
for file in files {
    guard let image = NSImage(contentsOfFile: "\(dir)/\(file)"),
          let cg = image.cgImage(forProposedRect: nil, context: nil, hints: nil),
          let data = cg.dataProvider?.data,
          let bytes = CFDataGetBytePtr(data) else { continue }
    let rowBytes = cg.bytesPerRow
    // The incoming panel is lighter than the window's dark ground. Walk each row
    // in the band from the left and record the last column that is still panel.
    var edges: [Int] = []
    var y = max(0, bandTop)
    while y < min(cg.height, bandBottom) {
        var lastBright = -1
        var x = 0
        while x < cg.width {
            let pixel = bytes[y * rowBytes + x * 4]
            if pixel > 70 { lastBright = x } else if lastBright >= 0 && x > lastBright + 12 { break }
            x += 1
        }
        if lastBright >= 0 { edges.append(lastBright) }
        y += 4
    }
    if edges.isEmpty { print("\(file)\t-"); continue }
    edges.sort()
    print("\(file)\t\(edges[edges.count / 2])")
}
