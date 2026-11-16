import Foundation
import CoreGraphics
import CoreText

func require(_ condition: @autoclosure () -> Bool, _ message: String) throws {
    if !condition() { throw NSError(domain: "ScreenTextTests", code: 1, userInfo: [NSLocalizedDescriptionKey: message]) }
}

@main
struct ScreenTextTests {
    static func main() async throws {
        try await testScreenTextRegions()
        let valid = Data(#"{"version":1,"kind":"recognize","request_id":"synthetic-1","pid":7,"window_id":42,"max_text_bytes":256,"timeout_ms":1000,"lease_token":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}"#.utf8)
        let request = try decodeScreenTextRequest(valid)
        try require(request.pid == 7 && request.windowID == 42 && request.maxTextBytes == 256, "valid bound request rejected")
        for invalid in [
            #"{"version":1,"kind":"recognize","request_id":"x","pid":0,"window_id":42,"max_text_bytes":256,"timeout_ms":1000,"lease_token":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}"#,
            #"{"version":1,"kind":"recognize","request_id":"x","pid":7,"window_id":0,"max_text_bytes":256,"timeout_ms":1000,"lease_token":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}"#,
            #"{"version":1,"kind":"recognize","request_id":"x","pid":7,"window_id":42,"max_text_bytes":262145,"timeout_ms":1000}"#,
            #"{"version":1,"kind":"recognize","request_id":"x","pid":7,"window_id":42,"max_text_bytes":256,"timeout_ms":8001}"#,
            #"{"version":1,"kind":"recognize","request_id":"x","pid":7,"window_id":42,"max_text_bytes":256,"timeout_ms":1000,"file":"/tmp/read-me"}"#,
            #"{"version":1,"kind":"recognize","request_id":"x","pid":true,"window_id":42,"max_text_bytes":256,"timeout_ms":1000,"lease_token":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}"#,
            #"{"version":2,"kind":"topmost","request_id":"x"}"#,
        ] {
            do { _ = try decodeScreenTextRequest(Data(invalid.utf8)); throw NSError(domain: "ScreenTextTests", code: 2, userInfo: [NSLocalizedDescriptionKey: "unsafe request accepted"]) }
            catch is ScreenTextFailure { }
        }
        print("PASS request bounds and strict schema")
        let image = try syntheticImage()
        let result = try recognizeScreenText(image, maxTextBytes: 4096, cancellation: ScreenTextCancellation())
        try require(result.text.contains("ROLLBACK OWNER MIRA"), "synthetic image text was not recognized")
        try require(!result.lines.isEmpty && result.lines.allSatisfy { $0.confidence >= 0 && $0.confidence <= 1 }, "missing OCR confidence")
        let clipped = try recognizeScreenText(image, maxTextBytes: 8, cancellation: ScreenTextCancellation())
        try require(clipped.text.utf8.count <= 8 && clipped.truncated, "OCR byte budget lost")
        let cancelled = ScreenTextCancellation()
        cancelled.cancel()
        do { _ = try recognizeScreenText(image, maxTextBytes: 4096, cancellation: cancelled); throw NSError(domain: "ScreenTextTests", code: 3, userInfo: [NSLocalizedDescriptionKey: "cancelled OCR was returned"]) }
        catch is CancellationError { }
        print("PASS real Vision OCR on synthetic image, bounds and cancellation")
        let baseWindow = ScreenTextWindow(pid: 7, windowID: 42, bundleID: "test.synthetic", title: "Synthetic", titleAvailable: true, titleTruncated: false, frame: ScreenTextRect(x: 0, y: 0, width: 1200, height: 240), fullOnscreen: true, unobscured: true, permissionGranted: true).sealed()
        let environment = SyntheticScreenTextEnvironment(window: baseWindow, image: image)
        let boundRequest = ScreenTextRequest(kind: "recognize", requestID: "synthetic-bound", pid: 7, windowID: 42, maxTextBytes: 4096, leaseToken: baseWindow.leaseToken, timeoutMilliseconds: 1000)
        environment.permission = false
        let denied = await performScreenText(boundRequest, environment: environment, cancellation: ScreenTextCancellation())
        try require(denied.status == "permission_required" && environment.captureCount == 0, "capture ran without permission")
        environment.permission = true
        environment.window = ScreenTextWindow(pid: 8, windowID: 42, bundleID: "test.synthetic", title: "Synthetic", titleAvailable: true, titleTruncated: false, frame: baseWindow.frame, fullOnscreen: true, unobscured: true, permissionGranted: true).sealed()
        let moved = await performScreenText(boundRequest, environment: environment, cancellation: ScreenTextCancellation())
        try require(moved.status == "target_changed" && environment.captureCount == 0, "other foreground app was captured")
        environment.window = baseWindow
        let recognized = await performScreenText(boundRequest, environment: environment, cancellation: ScreenTextCancellation())
        try require(recognized.status == "ok" && recognized.text?.contains("ROLLBACK OWNER MIRA") == true && environment.captureCount == 1, "bound synthetic capture lost text")
        environment.afterCapture = { environment.window = ScreenTextWindow(pid: 7, windowID: 42, bundleID: "test.synthetic", title: "Private Browsing", titleAvailable: true, titleTruncated: false, frame: baseWindow.frame, fullOnscreen: true, unobscured: true, permissionGranted: true).sealed() }
        let changed = await performScreenText(boundRequest, environment: environment, cancellation: ScreenTextCancellation())
        try require(changed.status == "target_changed" && changed.text == nil, "same-window privacy/title change returned OCR text")
        environment.afterCapture = nil
        for (onscreen, unobscured) in [(false, true), (true, false)] {
            environment.window = ScreenTextWindow(pid: 7, windowID: 42, bundleID: "test.synthetic", title: "Synthetic", titleAvailable: true, titleTruncated: false, frame: baseWindow.frame, fullOnscreen: onscreen, unobscured: unobscured, permissionGranted: true).sealed()
            let hiddenRequest = ScreenTextRequest(kind: "recognize", requestID: "hidden", pid: 7, windowID: 42, maxTextBytes: 4096, leaseToken: environment.window!.leaseToken, timeoutMilliseconds: 1000)
            let count = environment.captureCount
            let hidden = await performScreenText(hiddenRequest, environment: environment, cancellation: ScreenTextCancellation())
            try require(hidden.status == "not_visible" && environment.captureCount == count && hidden.text == nil, "hidden/offscreen contents captured")
        }
        print("PASS synthetic capture, permission, foreground, private/title and visibility gates")
        let delayed = DelayedScreenTextEnvironment(window: baseWindow, image: image)
        let registry = ScreenTextRegistry(environment: delayed)
        let timeoutJSON = Data((#"{"version":1,"kind":"recognize","request_id":"deadline","pid":7,"window_id":42,"max_text_bytes":256,"timeout_ms":100,"lease_token":""# + baseWindow.leaseToken + #""}"#).utf8)
        let handle = registry.start(timeoutJSON)
        try require(handle != 0, "valid async capture request rejected")
        let started = ContinuousClock.now
        var replyData: Data?
        while replyData == nil && started.duration(to: .now) < .seconds(2) {
            replyData = registry.result(handle, capacity: screenTextMaximumReplyBytes)
            if replyData == nil { try await Task.sleep(for: .milliseconds(10)) }
        }
        let replyObject = try JSONSerialization.jsonObject(with: replyData ?? Data()) as? [String: Any]
        try require(replyObject?["status"] as? String == "timeout", "hung capture did not return bounded timeout")
        try require(started.duration(to: .now) < .seconds(1), "capture deadline waited for uncooperative native work")
        try require(registry.poll(handle, capacity: 0).0 == -1, "consumed timeout result remained available")
        let cancelledHandle = registry.start(timeoutJSON)
        try require(cancelledHandle != 0, "second request rejected after deadline")
        registry.cancel(cancelledHandle)
        try await Task.sleep(for: .milliseconds(500))
        try require(registry.result(cancelledHandle, capacity: screenTextMaximumReplyBytes) == nil, "cancelled request resurrected a result")
        let metadataJSON = Data(#"{"version":1,"kind":"topmost","request_id":"metadata","timeout_ms":1000}"#.utf8)
        let metadataHandle = registry.start(metadataJSON)
        var size: Int32 = 0
        for _ in 0..<100 {
            size = registry.poll(metadataHandle, capacity: 0).0
            if size > 0 { break }
            try await Task.sleep(for: .milliseconds(10))
        }
        try require(size > 1, "metadata response was not produced")
        try require(registry.poll(metadataHandle, capacity: Int(size) - 1).0 == size, "small buffer consumed metadata")
        try require(registry.poll(metadataHandle, capacity: Int(size)).0 == size, "metadata result changed between size and copy")
        try require(registry.poll(metadataHandle, capacity: Int(size)).0 == -1, "result was not consumed")
        let capacity = ScreenTextCapacity(1)
        let reservation = capacity.acquire()
        try require(reservation != nil && capacity.acquire() == nil, "native work admission was unbounded")
        reservation?.release()
        reservation?.release()
        let replacement = capacity.acquire()
        try require(replacement != nil && capacity.acquire() == nil, "native admission release was not idempotent")
        replacement?.poison()
        try require(capacity.acquire() == nil, "failed stream stop released its safety reservation")
        print("PASS async deadline, late-result suppression, bounded admission and result consumption")
    }
}

func syntheticImage() throws -> CGImage {
    guard let context = CGContext(data: nil, width: 1200, height: 240, bitsPerComponent: 8, bytesPerRow: 0, space: CGColorSpaceCreateDeviceRGB(), bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue) else { throw ScreenTextFailure(status: "test_image") }
    context.setFillColor(CGColor(gray: 1, alpha: 1))
    context.fill(CGRect(x: 0, y: 0, width: 1200, height: 240))
    let font = CTFontCreateWithName("Menlo" as CFString, 44, nil)
    for (y, text) in [(150.0, "ROLLBACK OWNER MIRA"), (65.0, "MONDAY GET EGGS")] {
        let line = CTLineCreateWithAttributedString(NSAttributedString(string: text, attributes: [NSAttributedString.Key(kCTFontAttributeName as String): font, NSAttributedString.Key(kCTForegroundColorAttributeName as String): CGColor(gray: 0, alpha: 1)]))
        context.textPosition = CGPoint(x: 35, y: y)
        CTLineDraw(line, context)
    }
    guard let image = context.makeImage() else { throw ScreenTextFailure(status: "test_image") }
    return image
}

final class SyntheticScreenTextEnvironment: ScreenTextEnvironment, @unchecked Sendable {
    var permission = true
    var window: ScreenTextWindow?
    let image: CGImage
    var captureCount = 0
    var afterCapture: (() -> Void)?
    init(window: ScreenTextWindow, image: CGImage) { self.window = window; self.image = image }
    func permissionGranted() -> Bool { permission }
    func topmostWindow(permissionGranted: Bool) -> ScreenTextWindow? { window }
    func capture(_ request: ScreenTextRequest, window: ScreenTextWindow, cancellation: ScreenTextCancellation) async throws -> CGImage {
        captureCount += 1
        afterCapture?()
        return image
    }
}

struct DelayedScreenTextEnvironment: ScreenTextEnvironment {
    let window: ScreenTextWindow
    let image: CGImage
    func permissionGranted() -> Bool { true }
    func topmostWindow(permissionGranted: Bool) -> ScreenTextWindow? { window }
    func capture(_ request: ScreenTextRequest, window: ScreenTextWindow, cancellation: ScreenTextCancellation) async throws -> CGImage {
        return await withCheckedContinuation { continuation in
            DispatchQueue.global(qos: .utility).asyncAfter(deadline: .now() + .milliseconds(400)) {
                continuation.resume(returning: image)
            }
        }
    }
}
