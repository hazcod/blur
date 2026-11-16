import Foundation
import CoreFoundation
import CoreGraphics
import Vision

struct ScreenTextFailure: Error {
    let status: String
    static let invalidRequest = ScreenTextFailure(status: "invalid_request")
}

struct ScreenTextRequest: Sendable {
    let kind: String
    let requestID: String
    let pid: Int32
    let windowID: UInt32
    let maxTextBytes: Int
    let leaseToken: String
    let timeoutMilliseconds: Int
    var region: ScreenTextRect? = nil
}

func decodeScreenTextRequest(_ data: Data) throws -> ScreenTextRequest {
    guard data.count > 0, data.count <= 4096, String(data: data, encoding: .utf8) != nil,
          let object = try? JSONSerialization.jsonObject(with: data),
          let fields = object as? [String: Any],
          integer(fields["version"]) == 1,
          let kind = fields["kind"] as? String, ["topmost", "recognize"].contains(kind),
          let requestID = fields["request_id"] as? String,
          !requestID.isEmpty, requestID.utf8.count <= 64,
          requestID.utf8.allSatisfy({ ($0 >= 48 && $0 <= 57) || ($0 >= 65 && $0 <= 90) || ($0 >= 97 && $0 <= 122) || $0 == 45 || $0 == 95 }) else {
        throw ScreenTextFailure.invalidRequest
    }
    let allowed: Set<String> = kind == "topmost" ? ["version", "kind", "request_id", "timeout_ms"] : ["version", "kind", "request_id", "pid", "window_id", "max_text_bytes", "timeout_ms", "lease_token", "region"]
    guard Set(fields.keys).isSubset(of: allowed),
          let timeout = fields["timeout_ms"] == nil ? 1000 : integer(fields["timeout_ms"]),
          timeout >= 100, timeout <= 8000 else { throw ScreenTextFailure.invalidRequest }
    if kind == "topmost" {
        return ScreenTextRequest(kind: kind, requestID: requestID, pid: 0, windowID: 0, maxTextBytes: 0, leaseToken: "", timeoutMilliseconds: timeout)
    }
    guard let pid = integer(fields["pid"]), pid > 0, pid <= Int(Int32.max),
          let window = integer(fields["window_id"]), window > 0, window <= Int(UInt32.max),
          let limit = integer(fields["max_text_bytes"]), limit > 0, limit <= 262144,
          let token = fields["lease_token"] as? String, token.utf8.count == 64,
          token.utf8.allSatisfy({ ($0 >= 48 && $0 <= 57) || ($0 >= 97 && $0 <= 102) }) else { throw ScreenTextFailure.invalidRequest }
    var region: ScreenTextRect?
    if let raw = fields["region"] {
        guard let value = raw as? [String: Any], Set(value.keys) == ["x", "y", "width", "height"],
              let x = screenTextDecimal(value["x"]), let y = screenTextDecimal(value["y"]),
              let width = screenTextDecimal(value["width"]), let height = screenTextDecimal(value["height"]) else { throw ScreenTextFailure.invalidRequest }
        let rectangle = ScreenTextRect(x: x, y: y, width: width, height: height)
        guard validScreenTextRegion(rectangle) else { throw ScreenTextFailure.invalidRequest }
        region = rectangle
    }
    return ScreenTextRequest(kind: kind, requestID: requestID, pid: Int32(pid), windowID: UInt32(window), maxTextBytes: limit, leaseToken: token, timeoutMilliseconds: timeout, region: region)
}

private func screenTextDecimal(_ value: Any?) -> Double? {
    guard let number = value as? NSNumber, CFGetTypeID(number) != CFBooleanGetTypeID(), number.doubleValue.isFinite else { return nil }
    return number.doubleValue
}

private func integer(_ value: Any?) -> Int? {
    guard let number = value as? NSNumber, CFGetTypeID(number) != CFBooleanGetTypeID(),
          number.doubleValue.isFinite, number.doubleValue.rounded() == number.doubleValue,
          number.doubleValue >= Double(Int32.min), number.doubleValue <= Double(UInt32.max) else { return nil }
    return number.intValue
}

struct ScreenTextRect: Codable, Equatable, Sendable {
    let x: Double
    let y: Double
    let width: Double
    let height: Double
}

struct ScreenTextLine: Codable, Sendable {
    let text: String
    let confidence: Float
    let rect: ScreenTextRect
}

struct ScreenTextRecognition: Sendable {
    let text: String
    let lines: [ScreenTextLine]
    let truncated: Bool
}

final class ScreenTextCancellation: @unchecked Sendable {
    private let lock = NSLock()
    private var cancelled = false
    private var callbacks: [UUID: @Sendable () -> Void] = [:]

    func check() throws {
        lock.lock()
        let stopped = cancelled
        lock.unlock()
        if stopped || Task.isCancelled { throw CancellationError() }
    }

    func register(_ callback: @escaping @Sendable () -> Void) -> UUID {
        let id = UUID()
        lock.lock()
        let stopped = cancelled
        if !stopped { callbacks[id] = callback }
        lock.unlock()
        if stopped { callback() }
        return id
    }

    func remove(_ id: UUID) {
        lock.lock()
        callbacks.removeValue(forKey: id)
        lock.unlock()
    }

    func cancel() {
        lock.lock()
        if cancelled { lock.unlock(); return }
        cancelled = true
        let work = Array(callbacks.values)
        callbacks.removeAll()
        lock.unlock()
        for callback in work { callback() }
    }
}

func recognizeScreenText(_ image: CGImage, maxTextBytes: Int, cancellation: ScreenTextCancellation) throws -> ScreenTextRecognition {
    try cancellation.check()
    guard maxTextBytes > 0, maxTextBytes <= 262144, image.width > 0, image.height > 0,
          image.width <= 4096, image.height <= 4096, image.width <= 8388608 / image.height else {
        throw ScreenTextFailure(status: "image_limit")
    }
    guard let permit = screenTextVisionCapacity.acquire() else { throw ScreenTextFailure(status: "ocr_unavailable") }
    defer { permit.release() }
    let work = ScreenTextVisionWork()
    let registration = cancellation.register { work.request.cancel() }
    defer { cancellation.remove(registration) }
    work.request.recognitionLevel = .accurate
    work.request.usesLanguageCorrection = false
    work.request.automaticallyDetectsLanguage = true
    do {
        try VNImageRequestHandler(cgImage: image, options: [:]).perform([work.request])
    } catch {
        try cancellation.check()
        throw ScreenTextFailure(status: "ocr_unavailable")
    }
    try cancellation.check()
    let observations = (work.request.results ?? []).sorted {
        if abs($0.boundingBox.midY - $1.boundingBox.midY) < 0.01 { return $0.boundingBox.minX < $1.boundingBox.minX }
        return $0.boundingBox.midY > $1.boundingBox.midY
    }
    var remaining = maxTextBytes
    var lines: [ScreenTextLine] = []
    var truncated = observations.count > 512
    for observation in observations.prefix(512) {
        try cancellation.check()
        guard let candidate = observation.topCandidates(1).first, candidate.confidence.isFinite else { continue }
        let clean = candidate.string.unicodeScalars.filter { !CharacterSet.controlCharacters.contains($0) }
        let text = String(String.UnicodeScalarView(clean)).trimmingCharacters(in: .whitespacesAndNewlines)
        if text.isEmpty { continue }
        let separator = lines.isEmpty ? 0 : 1
        guard remaining > separator else { truncated = true; break }
        let bounded = boundedScreenText(text, bytes: remaining - separator)
        if bounded.utf8.count < text.utf8.count { truncated = true }
        if bounded.isEmpty { truncated = true; break }
        let rect = observation.boundingBox
        guard rect.minX.isFinite, rect.minY.isFinite, rect.width.isFinite, rect.height.isFinite else { continue }
        lines.append(ScreenTextLine(text: bounded, confidence: min(1, max(0, candidate.confidence)), rect: ScreenTextRect(x: rect.minX, y: rect.minY, width: rect.width, height: rect.height)))
        remaining -= bounded.utf8.count + separator
        if truncated && remaining == 0 { break }
    }
    try cancellation.check()
    return ScreenTextRecognition(text: lines.map(\.text).joined(separator: "\n"), lines: lines, truncated: truncated)
}

// Vision's cancellation API is designed to cancel work being performed on
// another thread. The box only shares that cancel operation across tasks.
private final class ScreenTextVisionWork: @unchecked Sendable {
    let request = VNRecognizeTextRequest()
}

func boundedScreenText(_ text: String, bytes: Int) -> String {
    guard bytes > 0 else { return "" }
    if text.utf8.count <= bytes { return text }
    var result = String.UnicodeScalarView()
    var remaining = bytes
    for scalar in text.unicodeScalars {
        let count = scalar.utf8.count
        if count > remaining { break }
        result.append(scalar)
        remaining -= count
    }
    return String(result)
}

// Cancellation ends publication immediately, but a framework callback can be
// late. Reservations bound outstanding native/OCR work until it really ends.
final class ScreenTextCapacity: @unchecked Sendable {
    private let lock = NSLock()
    private let limit: Int
    private var active = 0
    init(_ limit: Int) { self.limit = limit }
    func acquire() -> ScreenTextPermit? {
        lock.lock()
        defer { lock.unlock() }
        guard active < limit else { return nil }
        active += 1
        return ScreenTextPermit(self)
    }
    fileprivate func release() { lock.lock(); active -= 1; lock.unlock() }
}
final class ScreenTextPermit: @unchecked Sendable {
    private let lock = NSLock()
    private let capacity: ScreenTextCapacity
    private var released = false
    fileprivate init(_ capacity: ScreenTextCapacity) { self.capacity = capacity }
    func release() {
        lock.lock()
        if released { lock.unlock(); return }
        released = true
        lock.unlock()
        capacity.release()
    }
    // A stream that fails to stop must consume its slot for the process's
    // lifetime, preventing retries from multiplying an un-stoppable stream.
    func poison() { lock.lock(); released = true; lock.unlock() }
    deinit { release() }
}
private let screenTextVisionCapacity = ScreenTextCapacity(1)
