import Foundation
import CoreFoundation
import CryptoKit

enum SafariCaptureProtocol {
    static let maxBatchBytes = 32 * 1024
    static let maxPayloadBytes = 272 * 1024
    static let maxWireBytes = 288 * 1024
    static let maxAckBytes = 1024

    struct Batch {
        let documentID, documentURL, text: String
        let revision, sequence: Int64
        let complete, structureLimited: Bool
    }
    struct Ack { let ok, restart, paused: Bool }

    private static func boolean(_ value: Any?) -> Bool? {
        guard let number = value as? NSNumber, CFGetTypeID(number) == CFBooleanGetTypeID() else { return nil }
        return number.boolValue
    }
    private static func integer(_ value: Any?) -> Int64? {
        guard let number = value as? NSNumber, CFGetTypeID(number) != CFBooleanGetTypeID(),
              number.doubleValue >= 0, number.doubleValue <= 9_007_199_254_740_991,
              number.doubleValue.rounded(.towardZero) == number.doubleValue else { return nil }
        return number.int64Value
    }
    static func batch(_ message: [String: Any]?) -> Batch? {
        guard let message, integer(message["version"]) == 2,
              let documentID = message["documentID"] as? String, documentID.utf8.count == 32,
              documentID.utf8.allSatisfy({ $0 >= 48 && $0 <= 57 || $0 >= 97 && $0 <= 102 }),
              let documentURL = message["documentURL"] as? String, !documentURL.isEmpty, documentURL.utf8.count <= 4096,
              let text = message["text"] as? String, text.utf8.count <= maxBatchBytes, !text.contains("\0"),
              let revision = integer(message["revision"]), revision > 0,
              let sequence = integer(message["sequence"]),
              let complete = boolean(message["complete"]), let limited = boolean(message["structureLimited"]) else { return nil }
        return Batch(documentID: documentID, documentURL: documentURL, text: text, revision: revision, sequence: sequence, complete: complete, structureLimited: limited)
    }
    static func current(_ batch: Batch, url: URL?) -> Bool {
        guard let url, url.absoluteString.utf8.count <= 4096,
              var supplied = URLComponents(string: batch.documentURL),
              var current = URLComponents(url: url, resolvingAgainstBaseURL: false) else { return false }
        supplied.fragment = nil; current.fragment = nil
        return supplied.url == current.url && origin(url) != nil
    }
    static func origin(_ url: URL) -> String? {
        guard var components = URLComponents(url: url, resolvingAgainstBaseURL: false),
              components.scheme == "http" || components.scheme == "https",
              let host = components.host, !host.isEmpty,
              components.user == nil, components.password == nil else { return nil }
        components.path = ""; components.query = nil; components.fragment = nil; components.port = nil
        return components.string
    }
    static func sourceKey(_ url: URL) -> String? {
        guard url.absoluteString.utf8.count <= 4096, origin(url) != nil,
              var components = URLComponents(url: url, resolvingAgainstBaseURL: false) else { return nil }
        components.fragment = nil
        guard let identity = components.string else { return nil }
        return SHA256.hash(data: Data(("blur.safari.document.v2\0" + identity).utf8)).map { String(format: "%02x", $0) }.joined()
    }

    private struct Page: Encodable {
        let browser = "com.apple.safari"
        let url, title, text: String
        let active = true
        let privacy_known = true
        let `private` = false
        let captured_at, document_id, source_key: String
        let revision, sequence: Int64
        let complete, structure_limited: Bool
    }
    static func payload(_ batch: Batch, url: URL, title: String, capturedAt: String) -> Data? {
        guard current(batch, url: url), title.utf8.count <= 8192, !title.contains("\0"),
              let origin = origin(url), let key = sourceKey(url) else { return nil }
        let page = Page(url: origin, title: title, text: batch.text, captured_at: capturedAt, document_id: batch.documentID,
                        source_key: key, revision: batch.revision, sequence: batch.sequence, complete: batch.complete, structure_limited: batch.structureLimited)
        let encoder = JSONEncoder(); encoder.outputFormatting = [.sortedKeys]
        guard let data = try? encoder.encode(page), data.count <= maxPayloadBytes else { return nil }
        return data
    }
    static func ack(_ data: Data) -> Ack? {
        guard data.count <= maxAckBytes, let object = try? JSONSerialization.jsonObject(with: data),
              let reply = object as? [String: Any], Set(reply.keys).isSubset(of: ["ok", "restart", "paused"]),
              let ok = boolean(reply["ok"]), let restart = boolean(reply["restart"] ?? false), let paused = boolean(reply["paused"] ?? false) else { return nil }
        return Ack(ok: ok, restart: restart, paused: paused)
    }
}
