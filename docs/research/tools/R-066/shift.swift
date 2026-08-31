import AppKit

// Horizontal shift of a frame against a reference, by best match. The reference
// pushes its page content sideways, so this is the translation curve directly --
// and unlike edge detection it does not care which feature it is looking at.
let arguments = CommandLine.arguments
guard arguments.count >= 5, let bandTop = Int(arguments[3]), let bandBottom = Int(arguments[4]) else {
    print("usage: shift <dir> <referenceFile> <bandTop> <bandBottom>")
    exit(1)
}
let dir = arguments[1]

func load(_ path: String) -> (Int, Int, Int, UnsafePointer<UInt8>)? {
    guard let image = NSImage(contentsOfFile: path),
          let cg = image.cgImage(forProposedRect: nil, context: nil, hints: nil),
          let data = cg.dataProvider?.data,
          let bytes = CFDataGetBytePtr(data) else { return nil }
    return (cg.width, cg.height, cg.bytesPerRow, bytes)
}

guard let reference = load("\(dir)/\(arguments[2])") else {
    print("cannot read reference")
    exit(1)
}
let (width, height, refRow, refBytes) = reference
let rows = stride(from: max(0, bandTop), to: min(height, bandBottom), by: 6).map { $0 }
let margin = 40

let files = ((try? FileManager.default.contentsOfDirectory(atPath: dir)) ?? [])
    .filter { $0.hasSuffix(".png") }.sorted()
for file in files {
    guard let (w, h, row, bytes) = load("\(dir)/\(file)"), w == width, h == height else {
        print("\(file)\t尺寸不同")
        continue
    }
    // Mean absolute difference, not the sum: without dividing by the number of
    // pixels actually compared, the winning shift is always the one with the least
    // overlap, and every frame reports the extreme. That is what the first version
    // did, and it reported -1656 for the reference against itself.
    var bestShift = 0
    var bestScore = Double.greatestFiniteMagnitude
    let minimumOverlap = width / 3
    var shift = -width + margin
    while shift < width - margin {
        var score = 0
        var compared = 0
        for y in rows {
            var x = margin
            while x < width - margin {
                let source = x - shift
                if source >= 0 && source < width {
                    let a = Int(bytes[y * row + x * 4])
                    let b = Int(refBytes[y * refRow + source * 4])
                    score += abs(a - b)
                    compared += 1
                }
                x += 3
            }
        }
        if compared >= minimumOverlap {
            let mean = Double(score) / Double(compared)
            if mean < bestScore { bestScore = mean; bestShift = shift }
        }
        shift += 4
    }
    print("\(file)\t\(bestShift)")
}
