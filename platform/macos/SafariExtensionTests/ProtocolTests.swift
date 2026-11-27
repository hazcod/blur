import Foundation

@main
enum SafariProtocolTests {
    static func main() throws {
        let url = URL(string: "https://docs.example.test/private/path?token=hidden#anchor")!
        var message: [String: Any] = ["version": 2, "documentID": String(repeating: "a", count: 32), "documentURL": url.absoluteString,
                                      "revision": 1, "sequence": 0, "text": String(repeating: "x", count: 32768), "complete": false, "structureLimited": false]
        let batch = SafariCaptureProtocol.batch(message)!
        precondition(batch.text.utf8.count == 32768)
        precondition(SafariCaptureProtocol.current(batch, url: url))
        precondition(SafariCaptureProtocol.current(batch, url: URL(string: "https://docs.example.test/private/path?token=hidden#other")!), "fragment navigation invalidated the document cursor")
        precondition(!SafariCaptureProtocol.current(batch, url: URL(string: "https://other.example.test/")!))
        let payload = SafariCaptureProtocol.payload(batch, url: url, title: "Project", capturedAt: "2026-11-22T12:00:00Z")!
        let page = try JSONSerialization.jsonObject(with: payload) as! [String: Any]
        precondition(page["text"] as? String == batch.text, "native transport clipped an acknowledged batch")
        precondition(page["url"] as? String == "https://docs.example.test")
        precondition((page["source_key"] as? String)?.count == 64)
        precondition(!String(data: payload, encoding: .utf8)!.contains("private/path"))
        precondition(!String(data: payload, encoding: .utf8)!.contains("hidden"))
        let key = SafariCaptureProtocol.sourceKey(url)!
        precondition(key == SafariCaptureProtocol.sourceKey(URL(string: "https://docs.example.test/private/path?token=hidden#other")!))
        precondition(key != SafariCaptureProtocol.sourceKey(URL(string: "https://docs.example.test/other/path?token=hidden")!))
        var escaped = message
        escaped["text"] = String(repeating: "\u{1}", count: 32768)
        let escapedBatch = SafariCaptureProtocol.batch(escaped)!
        let escapedPayload = SafariCaptureProtocol.payload(escapedBatch, url: url, title: String(repeating: "\u{1}", count: 8192), capturedAt: "2026-11-22T12:00:00Z")!
        precondition(escapedPayload.count <= SafariCaptureProtocol.maxPayloadBytes, "valid escaped batch exceeded the unchanged frame budget")
        let escapedPage = try JSONSerialization.jsonObject(with: escapedPayload) as! [String: Any]
        precondition(escapedPage["text"] as? String == escapedBatch.text)
        for (field, value) in [("revision", -1 as Any), ("sequence", -1 as Any), ("sequence", true as Any), ("sequence", 1.5 as Any), ("revision", 9007199254740992 as Any), ("complete", 1 as Any), ("structureLimited", "false" as Any), ("documentID", "bad" as Any), ("text", String(repeating: "x", count: 32769) as Any)] {
            var invalid = message; invalid[field] = value
            precondition(SafariCaptureProtocol.batch(invalid) == nil, "accepted invalid protocol field \(field)")
        }
        message["text"] = ""; message["sequence"] = 1; message["complete"] = true; message["structureLimited"] = true
        precondition(SafariCaptureProtocol.batch(message)?.complete == true)
        precondition(SafariCaptureProtocol.ack(Data("{\"ok\":true,\"restart\":false}".utf8))?.ok == true)
        precondition(SafariCaptureProtocol.ack(Data("{\"ok\":false,\"restart\":true}".utf8))?.restart == true)
        precondition(SafariCaptureProtocol.ack(Data("{\"ok\":false,\"paused\":true}".utf8))?.paused == true)
        for invalid in ["{}", "{\"ok\":1}", "{\"ok\":true,\"extra\":true}"] {
            precondition(SafariCaptureProtocol.ack(Data(invalid.utf8)) == nil)
        }
        print("Safari protocol checks passed")
    }
}
