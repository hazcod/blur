import Foundation
import CoreGraphics

func testScreenTextRegions() async throws {
    let window = CGRect(x: -1400, y: -800, width: 1000, height: 700)
    let region = ScreenTextRect(x: -1380, y: -700, width: 960, height: 580)
    let display = ScreenTextDisplayGeometry(id: 7, frame: CGRect(x: -1600, y: -900, width: 1600, height: 900), pixelWidth: 3200, pixelHeight: 1800)
    let plan = try screenTextRegionPlan(region, window: window, displays: [display])
    try require(plan.displayID == 7 && plan.sourceRect == CGRect(x: 220, y: 200, width: 960, height: 580), "crop used global/bottom-left coordinates instead of display-local top-left points")
    try require(plan.width == 1920 && plan.height == 1160, "Retina scaling changed source authority or output size")
    let standard = ScreenTextDisplayGeometry(id: 8, frame: display.frame, pixelWidth: 1600, pixelHeight: 900)
    let standardPlan = try screenTextRegionPlan(region, window: window, displays: [standard])
    try require(standardPlan.sourceRect == plan.sourceRect && standardPlan.width == 960 && standardPlan.height == 580, "1x/2x display changed source region")
    for invalid in [
        ScreenTextRect(x: -1500, y: -700, width: 960, height: 580),
        ScreenTextRect(x: -1380, y: -700, width: 1001, height: 580),
        ScreenTextRect(x: -1380, y: -700, width: 0, height: 580),
        ScreenTextRect(x: Double.nan, y: -700, width: 960, height: 580)
    ] {
        do { _ = try screenTextRegionPlan(invalid, window: window, displays: [display]); throw NSError(domain: "ScreenTextRegionTests", code: 1) }
        catch is ScreenTextFailure { }
    }
    for displays in [[], [display, standard], [ScreenTextDisplayGeometry(id: 7, frame: CGRect(x: 0, y: 0, width: 1600, height: 900), pixelWidth: 3200, pixelHeight: 1800)]] {
        do { _ = try screenTextRegionPlan(region, window: window, displays: displays); throw NSError(domain: "ScreenTextRegionTests", code: 2) }
        catch is ScreenTextFailure { }
    }
    let giant = ScreenTextDisplayGeometry(id: 9, frame: CGRect(x: 0, y: 0, width: 10000, height: 8000), pixelWidth: 20000, pixelHeight: 16000)
    let large = try screenTextRegionPlan(ScreenTextRect(x: 100, y: 100, width: 9800, height: 7800), window: giant.frame, displays: [giant])
    try require(large.width <= 4096 && large.height <= 4096 && large.width * large.height <= 8388608, "crop exceeded image work bound")
    let valid = #"{"version":1,"kind":"recognize","request_id":"region","pid":7,"window_id":42,"max_text_bytes":256,"timeout_ms":1000,"lease_token":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","region":{"x":-1380,"y":-700,"width":960,"height":580}}"#
    let request = try decodeScreenTextRequest(Data(valid.utf8))
    try require(request.region == region, "strict region schema lost exact global authority")
    for invalid in [valid.replacingOccurrences(of: #""width":960"#, with: #""width":false"#), valid.replacingOccurrences(of: #""width":960"#, with: #""width":0"#), valid.replacingOccurrences(of: #""height":580"#, with: #""height":580,"display_id":7"#)] {
        do { _ = try decodeScreenTextRequest(Data(invalid.utf8)); throw NSError(domain: "ScreenTextRegionTests", code: 3) }
        catch is ScreenTextFailure { }
    }
    let image = try syntheticImage()
    let nativeWindow = ScreenTextWindow(pid: 7, windowID: 42, bundleID: "test.synthetic", title: "Synthetic", titleAvailable: true, titleTruncated: false, frame: ScreenTextRect(x: -1400, y: -800, width: 1000, height: 700), fullOnscreen: true, unobscured: true, permissionGranted: true).sealed()
    let environment = SyntheticScreenTextEnvironment(window: nativeWindow, image: image)
    let bound = ScreenTextRequest(kind: "recognize", requestID: "region-bound", pid: 7, windowID: 42, maxTextBytes: 4096, leaseToken: nativeWindow.leaseToken, timeoutMilliseconds: 1000, region: region)
    let recognized = await performScreenText(bound, environment: environment, cancellation: ScreenTextCancellation())
    try require(recognized.status == "ok" && recognized.region == region && recognized.text?.contains("ROLLBACK OWNER MIRA") == true, "region was not bound through native OCR reply")
    var outside = bound
    outside.region = ScreenTextRect(x: -1500, y: -700, width: 960, height: 580)
    let acquired = environment.captureCount
    let denied = await performScreenText(outside, environment: environment, cancellation: ScreenTextCancellation())
    try require(denied.status == "not_visible" && denied.region == outside.region && environment.captureCount == acquired && denied.text == nil, "out-of-window crop reached image acquisition")
    environment.afterCapture = { environment.window = ScreenTextWindow(pid: 7, windowID: 42, bundleID: "test.synthetic", title: "Private Browsing", titleAvailable: true, titleTruncated: false, frame: nativeWindow.frame, fullOnscreen: true, unobscured: true, permissionGranted: true).sealed() }
    let stale = await performScreenText(bound, environment: environment, cancellation: ScreenTextCancellation())
    try require(stale.status == "target_changed" && stale.text == nil && stale.region == region, "changed private window returned region text")
    print("PASS display-local pre-acquisition region plan, 1x/2x and negative origins, containment/ambiguity/image bounds and strict schema")
}
