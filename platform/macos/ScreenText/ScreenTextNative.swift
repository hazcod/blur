import Foundation
import AppKit
@preconcurrency import ScreenCaptureKit
import CoreGraphics
import CoreMedia
import VideoToolbox

struct NativeScreenTextEnvironment: ScreenTextEnvironment {
    func permissionGranted() -> Bool { CGPreflightScreenCaptureAccess() }

    func topmostWindow(permissionGranted: Bool) -> ScreenTextWindow? {
        guard let application = NSWorkspace.shared.frontmostApplication, application.processIdentifier > 0,
              let windows = CGWindowListCopyWindowInfo([.optionOnScreenOnly, .excludeDesktopElements], kCGNullWindowID) as? [[String: Any]] else { return nil }
        let pid = application.processIdentifier
        guard let index = windows.firstIndex(where: { window in
            number(window[kCGWindowOwnerPID as String]) == Int64(pid) &&
            number(window[kCGWindowLayer as String]) == 0 && validFrame(window) != nil &&
            (window[kCGWindowAlpha as String] as? NSNumber)?.doubleValue ?? 1 > 0
        }), let frame = validFrame(windows[index]), let identifier = number(windows[index][kCGWindowNumber as String]),
              identifier > 0, identifier <= Int64(UInt32.max) else { return nil }
        let unobscured = !windows[..<index].contains { other in
            guard (other[kCGWindowAlpha as String] as? NSNumber)?.doubleValue ?? 1 > 0,
                  let rectangle = validFrame(other) else { return false }
            return rectangle.intersects(frame)
        }
        var count: UInt32 = 0
        guard CGGetActiveDisplayList(0, nil, &count) == .success, count > 0, count <= 64 else { return nil }
        var displays = [CGDirectDisplayID](repeating: 0, count: Int(count))
        guard CGGetActiveDisplayList(count, &displays, &count) == .success else { return nil }
        let fullOnscreen = displays.prefix(Int(count)).contains { CGDisplayBounds($0).contains(frame) }
        let nativeTitle = permissionGranted ? windows[index][kCGWindowName as String] as? String : nil
        let title = boundedScreenText(nativeTitle ?? "", bytes: 512)
        return ScreenTextWindow(pid: pid, windowID: UInt32(identifier), bundleID: boundedScreenText(application.bundleIdentifier ?? "", bytes: 256), title: title, titleAvailable: nativeTitle != nil, titleTruncated: title.utf8.count < (nativeTitle?.utf8.count ?? 0), frame: ScreenTextRect(x: frame.minX, y: frame.minY, width: frame.width, height: frame.height), fullOnscreen: fullOnscreen, unobscured: unobscured, permissionGranted: permissionGranted).sealed()
    }

    func capture(_ request: ScreenTextRequest, window: ScreenTextWindow, cancellation: ScreenTextCancellation) async throws -> CGImage {
        try cancellation.check()
        // Permission is preflighted again; this API never requests or prompts.
        guard permissionGranted() else { throw ScreenTextFailure(status: "permission_required") }
        let content: ScreenTextShareable = try await awaitScreenTextNative(cancellation) { completion in
            SCShareableContent.getExcludingDesktopWindows(true, onScreenWindowsOnly: true) { content, error in
                guard error == nil, let content else { completion(.failure(ScreenTextFailure(status: "capture_unavailable"))); return }
                completion(.success(ScreenTextShareable(content)))
            }
        }
        try cancellation.check()
        guard topmostWindow(permissionGranted: true)?.leaseToken == window.leaseToken,
              let nativeWindow = content.value.windows.first(where: { $0.windowID == request.windowID && $0.owningApplication?.processID == request.pid }),
              nativeWindow.isOnScreen,
              nativeWindow.frame == CGRect(x: window.frame.x, y: window.frame.y, width: window.frame.width, height: window.frame.height) else {
            throw ScreenTextFailure(status: "target_changed")
        }
        let filter: SCContentFilter
        let plan: ScreenTextRegionPlan?
        if let region = request.region {
            // sourceRect is ignored for desktopIndependentWindow filters.
            // A display filter includes EXACTLY this bound window, while its
            // source rectangle samples only the authorized document viewport.
            // Never fall back to a display/all-windows filter or crop afterward.
            guard content.value.displays.count > 0, content.value.displays.count <= 64 else { throw ScreenTextFailure(status: "not_visible") }
            let displays = content.value.displays.compactMap { nativeDisplayGeometry($0) }
            guard displays.count == content.value.displays.count else { throw ScreenTextFailure(status: "target_changed") }
            let bounded = try screenTextRegionPlan(region, window: screenTextCGRect(window.frame), displays: displays)
            guard let display = content.value.displays.first(where: { $0.displayID == bounded.displayID }) else { throw ScreenTextFailure(status: "target_changed") }
            filter = SCContentFilter(display: display, including: [nativeWindow])
            if #available(macOS 14.0, *), filter.style != .display { throw ScreenTextFailure(status: "capture_unavailable") }
            if #available(macOS 14.2, *) { filter.includeMenuBar = false }
            plan = bounded
        } else {
            filter = SCContentFilter(desktopIndependentWindow: nativeWindow)
            plan = nil
        }
        let config = SCStreamConfiguration()
        let scale = min(2, 4096 / window.frame.width, 4096 / window.frame.height, sqrt(8388608 / (window.frame.width * window.frame.height)))
        config.width = plan?.width ?? max(1, Int((window.frame.width * scale).rounded(.down)))
        config.height = plan?.height ?? max(1, Int((window.frame.height * scale).rounded(.down)))
        if let plan {
            config.sourceRect = plan.sourceRect
            config.destinationRect = CGRect(x: 0, y: 0, width: plan.width, height: plan.height)
        }
        config.showsCursor = false
        config.capturesAudio = false
        if #available(macOS 14.2, *) { config.includeChildWindows = false }
        config.minimumFrameInterval = CMTime(value: 1, timescale: 1)
        config.queueDepth = 3
        try cancellation.check()
        guard topmostWindow(permissionGranted: true)?.leaseToken == window.leaseToken, plan.map({ displayUnchanged($0.display) }) ?? true else { throw ScreenTextFailure(status: "target_changed") }
        let image: CGImage
        if #available(macOS 14.0, *) {
            image = try await awaitScreenTextNative(cancellation) { completion in
                SCScreenshotManager.captureImage(contentFilter: filter, configuration: config) { image, error in
                    guard error == nil, let image else { completion(.failure(ScreenTextFailure(status: "capture_unavailable"))); return }
                    completion(.success(image))
                }
            }
        } else {
            let work = ScreenTextOneFrame()
            let registration = cancellation.register { work.cancel() }
            defer { cancellation.remove(registration) }
            image = try await awaitScreenTextNative(cancellation) { completion in work.begin(filter: filter, configuration: config, completion: completion) }
        }
        try cancellation.check()
        guard topmostWindow(permissionGranted: true)?.leaseToken == window.leaseToken, plan.map({ displayUnchanged($0.display) }) ?? true else { throw ScreenTextFailure(status: "target_changed") }
        if let plan, image.width != plan.width || image.height != plan.height { throw ScreenTextFailure(status: "image_limit") }
        return image
    }

    private func nativeDisplayGeometry(_ display: SCDisplay) -> ScreenTextDisplayGeometry? {
        guard CGDisplayIsActive(display.displayID) != 0, CGDisplayBounds(display.displayID) == display.frame else { return nil }
        return ScreenTextDisplayGeometry(id: display.displayID, frame: display.frame,
            pixelWidth: CGDisplayPixelsWide(display.displayID), pixelHeight: CGDisplayPixelsHigh(display.displayID))
    }

    private func displayUnchanged(_ display: ScreenTextDisplayGeometry) -> Bool {
        CGDisplayIsActive(display.id) != 0 && CGDisplayBounds(display.id) == display.frame &&
            CGDisplayPixelsWide(display.id) == display.pixelWidth && CGDisplayPixelsHigh(display.id) == display.pixelHeight
    }

    private func number(_ value: Any?) -> Int64? { (value as? NSNumber)?.int64Value }

    private func validFrame(_ value: [String: Any]) -> CGRect? {
        guard let bounds = value[kCGWindowBounds as String] as? [String: Any],
              let frame = CGRect(dictionaryRepresentation: bounds as CFDictionary), frame.minX.isFinite, frame.minY.isFinite,
              frame.width.isFinite, frame.height.isFinite, frame.width >= 1, frame.height >= 1,
              frame.width <= 32768, frame.height <= 32768 else { return nil }
        return frame
    }
}

private final class ScreenTextShareable: @unchecked Sendable {
    let value: SCShareableContent
    init(_ value: SCShareableContent) { self.value = value }
}

// Native callbacks may finish after cancellation or deadline. Exactly one result
// resumes the Swift task; late images are released without being published.
private final class ScreenTextContinuation<Value: Sendable>: @unchecked Sendable {
    private let lock = NSLock()
    private var continuation: CheckedContinuation<Value, any Error>?
    init(_ continuation: CheckedContinuation<Value, any Error>) { self.continuation = continuation }
    func finish(_ result: Result<Value, any Error>) {
        lock.lock()
        let continuation = self.continuation
        self.continuation = nil
        lock.unlock()
        continuation?.resume(with: result)
    }
}

private let screenTextNativeCapacity = ScreenTextCapacity(2)
private let screenTextStreamCapacity = ScreenTextCapacity(1)

private func awaitScreenTextNative<Value: Sendable>(_ cancellation: ScreenTextCancellation, start: (@escaping @Sendable (Result<Value, any Error>) -> Void) -> Void) async throws -> Value {
    try cancellation.check()
    guard let permit = screenTextNativeCapacity.acquire() else { throw ScreenTextFailure(status: "capture_unavailable") }
    return try await withCheckedThrowingContinuation { continuation in
        let gate = ScreenTextContinuation(continuation)
        let registration = cancellation.register { gate.finish(.failure(CancellationError())) }
        do { try cancellation.check() }
        catch { permit.release(); cancellation.remove(registration); gate.finish(.failure(error)); return }
        start { result in permit.release(); cancellation.remove(registration); gate.finish(result) }
    }
}

private final class ScreenTextOneFrame: NSObject, SCStreamOutput, @unchecked Sendable {
    private let lock = NSLock()
    private var stream: SCStream?
    private var completion: (@Sendable (Result<CGImage, any Error>) -> Void)?
    private var finished = false
    private var startScheduled = false
    private var permit: ScreenTextPermit?

    func begin(filter: SCContentFilter, configuration: SCStreamConfiguration, completion: @escaping @Sendable (Result<CGImage, any Error>) -> Void) {
        lock.lock()
        if finished { lock.unlock(); completion(.failure(CancellationError())); return }
        guard let permit = screenTextStreamCapacity.acquire() else { lock.unlock(); completion(.failure(ScreenTextFailure(status: "capture_unavailable"))); return }
        self.permit = permit
        self.completion = completion
        let stream = SCStream(filter: filter, configuration: configuration, delegate: nil)
        self.stream = stream
        // Serialize scheduling start with cancellation; stopping before a late
        // start would leave a stream active after its request was cancelled.
        do { try stream.addStreamOutput(self, type: .screen, sampleHandlerQueue: DispatchQueue.global(qos: .utility)) }
        catch { lock.unlock(); finish(.failure(ScreenTextFailure(status: "capture_unavailable"))); return }
        startScheduled = true
        stream.startCapture { error in
            if error != nil { DispatchQueue.global(qos: .utility).async { self.finish(.failure(ScreenTextFailure(status: "capture_unavailable"))) } }
        }
        lock.unlock()
    }

    func cancel() { finish(.failure(CancellationError())) }

    func stream(_ stream: SCStream, didOutputSampleBuffer sampleBuffer: CMSampleBuffer, of outputType: SCStreamOutputType) {
        guard outputType == .screen, sampleBuffer.isValid,
              let attachments = CMSampleBufferGetSampleAttachmentsArray(sampleBuffer, createIfNecessary: false) as? [[SCStreamFrameInfo: Any]],
              let raw = attachments.first?[.status] as? Int, SCFrameStatus(rawValue: raw) == .complete,
              let pixels = sampleBuffer.imageBuffer else { return }
        var image: CGImage?
        guard VTCreateCGImageFromCVPixelBuffer(pixels, options: nil, imageOut: &image) == noErr, let image else {
            finish(.failure(ScreenTextFailure(status: "capture_unavailable"))); return
        }
        finish(.success(image))
    }

    private func finish(_ result: Result<CGImage, any Error>) {
        lock.lock()
        if finished { lock.unlock(); return }
        finished = true
        let stream = self.stream
        self.stream = nil
        let completion = self.completion
        self.completion = nil
        let permit = self.permit
        self.permit = nil
        let startScheduled = self.startScheduled
        lock.unlock()
        if let stream, startScheduled {
            stream.stopCapture { error in if error == nil { permit?.release() } else { permit?.poison() } }
        } else { permit?.release() }
        completion?(result)
    }
}
