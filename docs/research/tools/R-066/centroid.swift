import AppKit

// The x-centroid of bright pixels in a horizontal band. During a push both pages
// share the frame, so no single global shift describes it (cross-correlation went
// bimodal on exactly that). A centroid over a band where one page's content
// dominates does track that page.
let arguments = CommandLine.arguments
guard arguments.count >= 5, let bandTop = Int(arguments[2]), let bandBottom = Int(arguments[3]),
      let threshold = Int(arguments[4]) else {
    print("usage: centroid <dir> <bandTop> <bandBottom> <threshold>")
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
    var weighted = 0.0
    var mass = 0.0
    var y = max(0, bandTop)
    while y < min(cg.height, bandBottom) {
        var x = 0
        while x < cg.width {
            let value = Int(bytes[y * rowBytes + x * 4])
            if value > threshold {
                weighted += Double(x) * Double(value)
                mass += Double(value)
            }
            x += 2
        }
        y += 3
    }
    if mass == 0 { print("\(file)\t-"); continue }
    print("\(file)\t\(Int(weighted / mass))\t质量 \(Int(mass / 1000))k")
}
