import CryptoKit
import Foundation
import SafariServices
import Security
import Darwin

private let captureMessage = "blur-page-snapshot-v1"
private let stateQueryMessage = "blur-capture-state-query"
private let stateReplyMessage = "blur-capture-state"
private let grantService = "sh.blur.safari"
private let grantAccount = "capture"
private let maxTextBytes = 256 * 1024
private let maxWireBytes = 288 * 1024
private let maxPayloadBytes = maxTextBytes + 8 * 1024 + 4 * 1024 + 4 * 1024
private let captureScope = "capture:ingest"

private struct GrantRecord: Decodable {
    let id: String
    let client_id: String
    let credential: Data
    let expires_at: String
}

private struct PagePayload: Encodable {
    let browser: String
    let url: String
    let title: String
    let text: String
    let active: Bool
    let privacy_known: Bool
    let `private`: Bool
    let captured_at: String
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
    override func messageReceived(withName messageName: String, from page: SFSafariPage, userInfo: [String: Any]? = nil) {
        guard messageName == captureMessage || messageName == stateQueryMessage else { return }
        page.getPropertiesWithCompletionHandler { properties in
            guard let properties = properties else {
                if messageName == stateQueryMessage { page.dispatchMessageToScript(withName: stateReplyMessage, userInfo: ["enabled": false]) }
                return
            }
            let active = properties.isActive
            let nonPrivate = !properties.usesPrivateBrowsing
            guard active, nonPrivate, let grant = Self.validGrant() else {
                page.dispatchMessageToScript(withName: stateReplyMessage, userInfo: ["enabled": false])
                return
            }
            if messageName == stateQueryMessage {
                page.dispatchMessageToScript(withName: stateReplyMessage, userInfo: ["enabled": true])
                return
            }
            guard let text = userInfo?["text"] as? String,
                  !text.isEmpty,
                  let url = Self.safeOrigin(properties.url),
                  let title = properties.title, title.utf8.count <= 8 * 1024 else {
                page.dispatchMessageToScript(withName: stateReplyMessage, userInfo: ["enabled": false])
                return
            }
            guard text.utf8.count <= maxTextBytes,
                  url.utf8.count <= 4 * 1024 else {
                page.dispatchMessageToScript(withName: stateReplyMessage, userInfo: ["enabled": false])
                return
            }
            let encoder = JSONEncoder()
            encoder.outputFormatting = [.sortedKeys]
            let emptyPage = PagePayload(browser: "com.apple.safari", url: url, title: title, text: "",
                                        active: active, privacy_known: true, private: !nonPrivate,
                                        captured_at: Self.formatDate(Date(timeIntervalSince1970: floor(Date().timeIntervalSince1970))))
            guard let basePayload = try? encoder.encode(emptyPage) else { return }
            let encodedTextBudget = maxPayloadBytes - basePayload.count + 2
            let boundedText = Self.boundedText(text, byteLimit: maxTextBytes - title.utf8.count - 1,
                                               jsonByteLimit: encodedTextBudget)
            guard !boundedText.isEmpty,
                  let frame = Self.makeFrame(grant: grant, url: url, title: title, text: boundedText, active: active, private: !nonPrivate),
                  frame.count <= maxWireBytes else {
                page.dispatchMessageToScript(withName: stateReplyMessage, userInfo: ["enabled": false])
                return
            }
            if !Self.send(frame: frame) { page.dispatchMessageToScript(withName: stateReplyMessage, userInfo: ["enabled": false]) }
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

    private static func safeOrigin(_ url: URL?) -> String? {
        guard let url = url, var components = URLComponents(url: url, resolvingAgainstBaseURL: false),
              components.scheme == "http" || components.scheme == "https",
              let host = components.host, !host.isEmpty,
              components.user == nil, components.password == nil else { return nil }
        components.path = ""
        components.query = nil
        components.fragment = nil
        components.port = nil
        return components.string
    }

    private static func boundedText(_ value: String, byteLimit: Int, jsonByteLimit: Int) -> String {
        var result = String()
        var used = 2 // JSON string quotes
        var textBytes = 0
        for scalar in value.unicodeScalars {
            let utf8Width = scalar.value <= 0x7f ? 1 : scalar.value <= 0x7ff ? 2 : scalar.value <= 0xffff ? 3 : 4
            let width = Self.jsonEncodedWidth(scalar.value)
            if textBytes + utf8Width > byteLimit || used + width > jsonByteLimit { break }
            result.unicodeScalars.append(scalar)
            used += width
            textBytes += utf8Width
        }
        return result
    }

    private static func jsonEncodedWidth(_ scalar: UInt32) -> Int {
        if scalar == 0x22 || scalar == 0x5c || scalar == 0x2f { return 2 }
        if scalar == 0x2028 || scalar == 0x2029 || (scalar >= 0x7f && scalar <= 0x9f) { return 6 }
        if scalar < 0x20 { return scalar == 0x08 || scalar == 0x09 || scalar == 0x0a || scalar == 0x0c || scalar == 0x0d ? 2 : 6 }
        if scalar <= 0x7f { return 1 }
        if scalar <= 0x7ff { return 2 }
        if scalar <= 0xffff { return 3 }
        return 4
    }

    private static func makeFrame(grant: GrantRecord, url: String, title: String, text: String, active: Bool, private isPrivate: Bool) -> Data? {
        let timestamp = Date(timeIntervalSince1970: floor(Date().timeIntervalSince1970))
        let timestampString = formatDate(timestamp)
        let page = PagePayload(browser: "com.apple.safari", url: url, title: title, text: text,
                               active: active, privacy_known: true, private: isPrivate, captured_at: timestampString)
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.sortedKeys]
        guard let payload = try? encoder.encode(page), payload.count <= maxPayloadBytes else { return nil }
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
		var encoded = Data("{\"version\":1,\"payload\":".utf8)
		encoded.append(payload)
		encoded.append(Data(",\"proof\":".utf8))
		encoded.append(encodedProof)
		encoded.append(0x7d)
		guard encoded.count <= maxWireBytes else { return nil }
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

    private static func send(frame: Data) -> Bool {
        guard let group = Bundle.main.object(forInfoDictionaryKey: "BlurAppGroupIdentifier") as? String,
              let container = FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: group) else { return false }
        let path = container.appendingPathComponent("b", isDirectory: true).appendingPathComponent("s").path
        guard path.utf8.count < MemoryLayout<sockaddr_un>.size else { return false }
        let fd = socket(AF_UNIX, SOCK_STREAM, 0)
        guard fd >= 0 else { return false }
        defer { close(fd) }
        var noSignal: Int32 = 1
        _ = setsockopt(fd, SOL_SOCKET, SO_NOSIGPIPE, &noSignal, socklen_t(MemoryLayout<Int32>.size))
        var address = sockaddr_un()
        address.sun_family = sa_family_t(AF_UNIX)
        let pathBytes = Array(path.utf8) + [0]
        let socketPathCapacity = MemoryLayout.size(ofValue: address.sun_path)
        guard pathBytes.count <= socketPathCapacity else { return false }
        withUnsafeMutablePointer(to: &address.sun_path) { pointer in
            pointer.withMemoryRebound(to: CChar.self, capacity: socketPathCapacity) { destination in
                for index in pathBytes.indices { destination[index] = CChar(bitPattern: pathBytes[index]) }
            }
        }
        let connected = withUnsafePointer(to: &address) { pointer in
            pointer.withMemoryRebound(to: sockaddr.self, capacity: 1) { Darwin.connect(fd, $0, socklen_t(MemoryLayout<sockaddr_un>.size)) }
        }
        guard connected == 0 else { return false }
		var header = UInt32(frame.count).bigEndian
        var packet = Data()
        withUnsafeBytes(of: &header) { packet.append(contentsOf: $0) }
        packet.append(frame)
        return writeAll(fd: fd, packet: packet)
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
