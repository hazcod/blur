import Foundation
import CoreGraphics

struct ScreenTextDisplayGeometry: Equatable, Sendable {
    let id: UInt32
    let frame: CGRect
    let pixelWidth: Int
    let pixelHeight: Int
}

struct ScreenTextRegionPlan: Sendable {
    let displayID: UInt32
    let display: ScreenTextDisplayGeometry
    let sourceRect: CGRect
    let width: Int
    let height: Int
}

func validScreenTextRegion(_ region: ScreenTextRect) -> Bool {
    [region.x, region.y, region.width, region.height].allSatisfy(\.isFinite) &&
    abs(region.x) <= 1000000 && abs(region.y) <= 1000000 &&
    region.width >= 1 && region.height >= 1 && region.width <= 32768 && region.height <= 32768
}

func screenTextCGRect(_ rect: ScreenTextRect) -> CGRect {
    CGRect(x: rect.x, y: rect.y, width: rect.width, height: rect.height)
}

// ScreenCaptureKit sourceRect is display-local top-left POINTS, not AppKit
// bottom-left coordinates or physical pixels. Inclusion and source crop are
// configured together BEFORE sampling. Never crop a whole-window image later.
func screenTextRegionPlan(_ region: ScreenTextRect, window: CGRect, displays: [ScreenTextDisplayGeometry]) throws -> ScreenTextRegionPlan {
    guard validScreenTextRegion(region), displays.count > 0, displays.count <= 64,
          !window.isNull, !window.isInfinite, window.width >= 1, window.height >= 1,
          window.contains(screenTextCGRect(region)) else { throw ScreenTextFailure(status: "not_visible") }
    let matching = displays.filter { $0.id > 0 && !$0.frame.isNull && !$0.frame.isInfinite && $0.frame.contains(window) && $0.frame.contains(screenTextCGRect(region)) }
    guard matching.count == 1, let display = matching.first, display.frame.width > 0, display.frame.height > 0,
          display.pixelWidth > 0, display.pixelHeight > 0 else { throw ScreenTextFailure(status: "not_visible") }
    let scaleX = Double(display.pixelWidth) / display.frame.width
    let scaleY = Double(display.pixelHeight) / display.frame.height
    guard scaleX.isFinite, scaleY.isFinite, scaleX >= 0.5, scaleX <= 8, abs(scaleX - scaleY) < 0.01 else { throw ScreenTextFailure(status: "not_visible") }
    let scale = min(2, scaleX, 4096 / region.width, 4096 / region.height, sqrt(8388608 / (region.width * region.height)))
    let source = screenTextCGRect(region).offsetBy(dx: -display.frame.minX, dy: -display.frame.minY)
    guard CGRect(origin: .zero, size: display.frame.size).contains(source) else { throw ScreenTextFailure(status: "not_visible") }
    return ScreenTextRegionPlan(displayID: display.id, display: display, sourceRect: source,
        width: max(1, Int((region.width * scale).rounded(.down))), height: max(1, Int((region.height * scale).rounded(.down))))
}
