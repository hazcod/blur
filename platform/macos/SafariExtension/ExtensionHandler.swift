import CryptoKit
import Foundation
import SafariServices
import Security
import Darwin

private let captureMessage = "blur-page-snapshot-v2"
private let stateQueryMessage = "blur-capture-state-query"
private let stateReplyMessage = "blur-capture-state"
private let grantService = "sh.blur.safari"
private let grantAccount = "capture"
private let captureScope = "capture:ingest"

private struct GrantRecord: Decodable {
    let id: String
    let client_id: String
    let credential: Data
    let expires_at: String
}

private struct ProofPayload: Encodable {
    let grant_id: String
    let client_id: String
    let scope: String
    let timestamp: String
    let nonce: String
    let mac: String
}

final class SafariExtensionHandler: SFSafariExtensionHandler {
    private let captureGate = DispatchSemaphore(value: 4)

    override func messageReceived(withName messageName: String, from page: SFSafariPage, userInfo: [String: Any]? = nil) {
        guard messageName == captureMessage || messageName == stateQueryMessage else { return }
        let batch = messageName == captureMessage ? SafariCaptureProtocol.batch(userInfo) : nil
        guard messageName == stateQueryMessage || batch != nil else {
            page.dispatchMessageToScript(withName: stateReplyMessage, userInfo: ["enabled": false]); return
        }
        guard captureGate.wait(timeout: .now()) == .success else {
            if let batch { Self.reply(page, batch: batch, acknowledgement: nil) }
            return
        }
        page.getPropertiesWithCompletionHandler { properties in
            guard let properties, properties.isActive, !properties.usesPrivateBrowsing,
                  let grant = Self.validGrant() else {
                self.captureGate.signal()
                page.dispatchMessageToScript(withName: stateReplyMessage, userInfo: ["enabled": false]); return
            }
            if messageName == stateQueryMessage {
                self.captureGate.signal()
                page.dispatchMessageToScript(withName: stateReplyMessage, userInfo: ["enabled": true]); return
            }
            guard let batch, let url = properties.url, SafariCaptureProtocol.current(batch, url: url),
                  let title = properties.title else {
                self.captureGate.signal()
                if let batch { Self.reply(page, batch: batch, acknowledgement: SafariCaptureProtocol.Ack(ok: false, restart: true, paused: false)) }
                return
            }
            // Bound native work and recheck browser authority for every batch,
            // including retries and the final publication message.
            DispatchQueue.global(qos: .utility).async {
                defer { self.captureGate.signal() }
                let at = Date(timeIntervalSince1970: floor(Date().timeIntervalSince1970))
                guard let payload = SafariCaptureProtocol.payload(batch, url: url, title: title, capturedAt: Self.formatDate(at)),
                      let frame = Self.makeFrame(grant: grant, payload: payload, timestamp: at) else {
                    Self.reply(page, batch: batch, acknowledgement: SafariCaptureProtocol.Ack(ok: false, restart: false, paused: true)); return
                }
                Self.reply(page, batch: batch, acknowledgement: Self.send(frame: frame))
            }
        }
    }

    private static func reply(_ page: SFSafariPage, batch: SafariCaptureProtocol.Batch, acknowledgement: SafariCaptureProtocol.Ack?) {
        DispatchQueue.main.async {
            if acknowledgement?.paused == true {
                page.dispatchMessageToScript(withName: stateReplyMessage, userInfo: ["enabled": false])
            } else {
                page.dispatchMessageToScript(withName: "blur-page-capture-ack", userInfo: ["revision": batch.revision, "sequence": batch.sequence,
                                                                                       "ok": acknowledgement?.ok ?? false, "restart": acknowledgement?.restart ?? false])
            }
        }
    }

    private static func validGrant() -> GrantRecord? {
        guard let accessGroup = Bundle.main.object(forInfoDictionaryKey: "BlurKeychainAccessGroup") as? String,
              !accessGroup.isEmpty else { return nil }
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: grantService,
            kSecAttrAccount as String: grantAccount,
            kSecAttrAccessGroup as String: accessGroup,
            kSecReturnData as String: true,
            kSecMatchLimit as String: kSecMatchLimitOne,
        ]
        var result: CFTypeRef?
        guard SecItemCopyMatching(query as CFDictionary, &result) == errSecSuccess,
              let data = result as? Data,
              let grant = try? JSONDecoder().decode(GrantRecord.self, from: data),
              grant.credential.count == 32,
              let expiry = Self.parseDate(grant.expires_at), expiry > Date() else { return nil }
        return grant
    }

    private static func makeFrame(grant: GrantRecord, payload: Data, timestamp: Date) -> Data? {
        let timestampString = formatDate(timestamp)
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.sortedKeys]
        let nonce = randomBytes(count: 24)
        guard nonce.count == 24 else { return nil }
        let keyData = Data(SHA256.hash(data: grant.credential))
        let proof = ProofPayload(grant_id: grant.id, client_id: grant.client_id, scope: captureScope,
                                 timestamp: timestampString, nonce: base64URL(nonce),
                                 mac: hex(HMAC<SHA256>.authenticationCode(for: signingBytes(grantID: grant.id,
                                                                                         clientID: grant.client_id,
                                                                                         scope: captureScope,
                                                                                         timestamp: timestamp,
                                                                                         nonce: nonce,
                                                                                         digest: Data(SHA256.hash(data: payload))),
                                                                 using: SymmetricKey(data: keyData))))
		guard let encodedProof = try? encoder.encode(proof) else { return nil }
		var encoded = Data("{\"version\":2,\"payload\":".utf8)
		encoded.append(payload)
		encoded.append(Data(",\"proof\":".utf8))
		encoded.append(encodedProof)
		encoded.append(0x7d)
		guard encoded.count <= SafariCaptureProtocol.maxWireBytes else { return nil }
		return encoded
    }

    private static func signingBytes(grantID: String, clientID: String, scope: String, timestamp: Date, nonce: Data, digest: Data) -> Data {
        var result = Data()
        appendField(Data("blur-local-proof-v1".utf8), to: &result)
        appendField(Data(grantID.utf8), to: &result)
        appendField(Data(clientID.utf8), to: &result)
        appendField(Data(scope.utf8), to: &result)
        var time = Data()
        appendUInt64(UInt64(Int64(timestamp.timeIntervalSince1970)), to: &time)
        appendUInt32(0, to: &time)
        appendField(time, to: &result)
        appendField(nonce, to: &result)
        appendField(digest, to: &result)
        return result
    }

    private static func appendField(_ value: Data, to data: inout Data) {
        appendUInt32(UInt32(value.count), to: &data)
        data.append(value)
    }

    private static func appendUInt32(_ value: UInt32, to data: inout Data) {
        var encoded = value.bigEndian
        withUnsafeBytes(of: &encoded) { data.append(contentsOf: $0) }
    }

    private static func appendUInt64(_ value: UInt64, to data: inout Data) {
        var encoded = value.bigEndian
        withUnsafeBytes(of: &encoded) { data.append(contentsOf: $0) }
    }

    private static func randomBytes(count: Int) -> Data {
        var bytes = Data(count: count)
        let status = bytes.withUnsafeMutableBytes { buffer in
            SecRandomCopyBytes(kSecRandomDefault, count, buffer.baseAddress!)
        }
        guard status == errSecSuccess else { bytes.resetBytes(in: 0..<bytes.count); return Data() }
        return bytes
    }

    private static func send(frame: Data) -> SafariCaptureProtocol.Ack? {
        guard let group = Bundle.main.object(forInfoDictionaryKey: "BlurAppGroupIdentifier") as? String,
              let container = FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: group) else { return nil }
        let path = container.appendingPathComponent("b", isDirectory: true).appendingPathComponent("s").path
        guard path.utf8.count < MemoryLayout<sockaddr_un>.size else { return nil }
        let fd = socket(AF_UNIX, SOCK_STREAM, 0)
        guard fd >= 0 else { return nil }
        defer { close(fd) }
        var timeout = timeval(tv_sec: 2, tv_usec: 0)
        _ = setsockopt(fd, SOL_SOCKET, SO_SNDTIMEO, &timeout, socklen_t(MemoryLayout<timeval>.size))
        _ = setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &timeout, socklen_t(MemoryLayout<timeval>.size))
        var noSignal: Int32 = 1
        _ = setsockopt(fd, SOL_SOCKET, SO_NOSIGPIPE, &noSignal, socklen_t(MemoryLayout<Int32>.size))
        var address = sockaddr_un()
        address.sun_family = sa_family_t(AF_UNIX)
        let pathBytes = Array(path.utf8) + [0]
        let socketPathCapacity = MemoryLayout.size(ofValue: address.sun_path)
        guard pathBytes.count <= socketPathCapacity else { return nil }
        withUnsafeMutablePointer(to: &address.sun_path) { pointer in
            pointer.withMemoryRebound(to: CChar.self, capacity: socketPathCapacity) { destination in
                for index in pathBytes.indices { destination[index] = CChar(bitPattern: pathBytes[index]) }
            }
        }
        let connected = withUnsafePointer(to: &address) { pointer in
            pointer.withMemoryRebound(to: sockaddr.self, capacity: 1) { Darwin.connect(fd, $0, socklen_t(MemoryLayout<sockaddr_un>.size)) }
        }
        guard connected == 0 else { return nil }
		var header = UInt32(frame.count).bigEndian
        var packet = Data()
        withUnsafeBytes(of: &header) { packet.append(contentsOf: $0) }
        packet.append(frame)
        guard writeAll(fd: fd, packet: packet), let header = readAll(fd: fd, count: 4) else { return nil }
        let length = header.reduce(UInt32(0)) { ($0 << 8) | UInt32($1) }
        guard length > 0, length <= SafariCaptureProtocol.maxAckBytes, let data = readAll(fd: fd, count: Int(length)) else { return nil }
        return SafariCaptureProtocol.ack(data)
    }

    private static func writeAll(fd: Int32, packet: Data) -> Bool {
        packet.withUnsafeBytes { buffer in
            guard let base = buffer.baseAddress else { return false }
            var sent = 0
            while sent < buffer.count {
                let count = Darwin.send(fd, base.advanced(by: sent), buffer.count - sent, 0)
                if count <= 0 { return false }
                sent += count
            }
            return true
        }
    }

    private static func readAll(fd: Int32, count: Int) -> Data? {
        var data = Data(count: count)
        let complete = data.withUnsafeMutableBytes { buffer -> Bool in
            guard let base = buffer.baseAddress else { return false }
            var received = 0
            while received < buffer.count {
                let amount = Darwin.recv(fd, base.advanced(by: received), buffer.count - received, 0)
                if amount < 0 && errno == EINTR { continue }
                if amount <= 0 { return false }
                received += amount
            }
            return true
        }
        return complete ? data : nil
    }

    private static func parseDate(_ value: String) -> Date? {
        let fractional = ISO8601DateFormatter(); fractional.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return fractional.date(from: value) ?? ISO8601DateFormatter().date(from: value)
    }

    private static func formatDate(_ value: Date) -> String {
        let formatter = ISO8601DateFormatter(); formatter.formatOptions = [.withInternetDateTime]
        return formatter.string(from: value)
    }

    private static func base64URL(_ value: Data) -> String {
        value.base64EncodedString().replacingOccurrences(of: "+", with: "-")
            .replacingOccurrences(of: "/", with: "_").replacingOccurrences(of: "=", with: "")
    }

    private static func hex(_ value: some Sequence<UInt8>) -> String {
        value.map { String(format: "%02x", $0) }.joined()
    }
}
