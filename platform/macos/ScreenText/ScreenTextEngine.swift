import Foundation
import CoreGraphics
import CryptoKit

struct ScreenTextWindow: Codable, Sendable {
    let pid: Int32
    let windowID: UInt32
    let bundleID: String
    let title: String
    let titleAvailable: Bool
    let titleTruncated: Bool
    let frame: ScreenTextRect
    let fullOnscreen: Bool
    let unobscured: Bool
    let permissionGranted: Bool
    var leaseToken: String = ""

    func sealed() -> ScreenTextWindow {
        var copy = self
        copy.leaseToken = ""
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.sortedKeys, .withoutEscapingSlashes]
        let data = (try? encoder.encode(copy)) ?? Data()
        copy.leaseToken = SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined()
        return copy
    }
}

struct ScreenTextReply: Codable, Sendable {
    let version: Int
    let requestID: String
    let pid: Int32
    let windowID: UInt32
    let capturedAtMilliseconds: Int64
    let status: String
    let window: ScreenTextWindow?
    let text: String?
    let lines: [ScreenTextLine]?
    let truncated: Bool
    var region: ScreenTextRect? = nil
}

protocol ScreenTextEnvironment: Sendable {
    func permissionGranted() -> Bool
    func topmostWindow(permissionGranted: Bool) -> ScreenTextWindow?
    func capture(_ request: ScreenTextRequest, window: ScreenTextWindow, cancellation: ScreenTextCancellation) async throws -> CGImage
}

func performScreenText(_ request: ScreenTextRequest, environment: any ScreenTextEnvironment, cancellation: ScreenTextCancellation) async -> ScreenTextReply {
    do {
        try cancellation.check()
        let permission = environment.permissionGranted()
        if request.kind == "topmost" {
            let window = environment.topmostWindow(permissionGranted: permission)
            try cancellation.check()
            return screenTextReply(request, status: window == nil ? "no_window" : "ok", window: window)
        }
        guard permission else { return screenTextReply(request, status: "permission_required") }
        guard let window = environment.topmostWindow(permissionGranted: true), window.pid == request.pid,
              window.windowID == request.windowID, window.leaseToken == request.leaseToken else {
            return screenTextReply(request, status: "target_changed")
        }
        guard window.fullOnscreen, window.unobscured else { return screenTextReply(request, status: "not_visible") }
        if let region = request.region {
            guard validScreenTextRegion(region), screenTextCGRect(window.frame).contains(screenTextCGRect(region)) else { return screenTextReply(request, status: "not_visible") }
        }
        let image = try await environment.capture(request, window: window, cancellation: cancellation)
        let capturedAt = Int64(Date().timeIntervalSince1970 * 1000)
        try cancellation.check()
        guard environment.permissionGranted(), environment.topmostWindow(permissionGranted: true)?.leaseToken == window.leaseToken else {
            return screenTextReply(request, status: "target_changed")
        }
        let recognition = try recognizeScreenText(image, maxTextBytes: request.maxTextBytes, cancellation: cancellation)
        try cancellation.check()
        guard environment.permissionGranted(), environment.topmostWindow(permissionGranted: true)?.leaseToken == window.leaseToken else {
            return screenTextReply(request, status: "target_changed")
        }
        return screenTextReply(request, status: recognition.text.isEmpty ? "no_text" : "ok", window: window, recognition: recognition, capturedAt: capturedAt)
    } catch is CancellationError {
        return screenTextReply(request, status: "cancelled")
    } catch let failure as ScreenTextFailure {
        return screenTextReply(request, status: failure.status)
    } catch {
        return screenTextReply(request, status: "capture_unavailable")
    }
}

func screenTextReply(_ request: ScreenTextRequest, status: String, window: ScreenTextWindow? = nil, recognition: ScreenTextRecognition? = nil, capturedAt: Int64? = nil) -> ScreenTextReply {
    ScreenTextReply(version: 1, requestID: request.requestID, pid: request.pid, windowID: request.windowID, capturedAtMilliseconds: capturedAt ?? Int64(Date().timeIntervalSince1970 * 1000), status: status, window: window, text: recognition?.text, lines: recognition?.lines, truncated: recognition?.truncated ?? false, region: request.region)
}
