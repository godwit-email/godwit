// Draws the Windows app icon, assets/icon.png (1024 px): the logo in the
// macOS icon's light colours, on a rounded tile so it reads on a dark taskbar.
// usage: swift scripts/winicon.swift (from the repository root)
import AppKit

let size = 1024.0, inset = 64.0, radius = 180.0
let tile = NSColor(srgbRed: 0.98, green: 0.98, blue: 0.98, alpha: 1) // icon.json's light fill
let mark = NSColor(srgbRed: 0.093, green: 0.093, blue: 0.093, alpha: 1) // its logo layer
guard let logo = NSImage(contentsOfFile: "assets/logo.svg") else { fatalError("logo.svg did not load") }

let rep = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: Int(size), pixelsHigh: Int(size), bitsPerSample: 8,
                           samplesPerPixel: 4, hasAlpha: true, isPlanar: false, colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0)!
NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: rep)
let box = NSRect(x: inset, y: inset, width: size - 2 * inset, height: size - 2 * inset)
tile.setFill()
NSBezierPath(roundedRect: box, xRadius: radius, yRadius: radius).fill()
// The logo is one colour: tint a copy, then draw it over the tile.
let tinted = NSImage(size: box.size, flipped: false) { r in
    logo.draw(in: r)
    mark.set()
    r.fill(using: .sourceAtop)
    return true
}
tinted.draw(in: box)
NSGraphicsContext.current = nil
try! rep.representation(using: .png, properties: [:])!.write(to: URL(fileURLWithPath: "assets/icon.png"))
print("wrote assets/icon.png")
