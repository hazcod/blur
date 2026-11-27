import Foundation
import CoreFoundation

struct MeetingAudioFailure: Error { let code: String }
// The ABI carries milliseconds; Go converts them to signed nanosecond durations.
// This is a representation boundary, not a recording-session duration policy.
let meetingAudioMaximumMilliseconds: Int64 = Int64.max / 1_000_000
func meetingAudioTimestamp(_ milliseconds: Double) -> Int64? {
    guard milliseconds.isFinite, milliseconds >= 0, milliseconds <= Double(meetingAudioMaximumMilliseconds),
          let rounded = Int64(exactly: milliseconds.rounded()), rounded <= meetingAudioMaximumMilliseconds else { return nil }
    return rounded
}
struct MeetingAudioConfiguration: Sendable {
    let kind: String
    let sessionID: String
    let locale: String
    let tracks: [String]
    let startupMilliseconds: Int
}
func decodeMeetingAudioConfiguration(_ data: Data) throws -> MeetingAudioConfiguration {
    guard data.count > 0, data.count <= 4096, String(data: data, encoding: .utf8) != nil,
          let value = try? JSONSerialization.jsonObject(with: data), let object = value as? [String: Any],
          Set(object.keys).isSubset(of: ["version", "kind", "session_id", "locale", "tracks", "explicit_start", "explicit_setup", "startup_ms"]),
          meetingInteger(object["version"]) == 1, let kind = object["kind"] as? String, ["probe", "start", "request_access", "install_locale"].contains(kind),
          let sessionID = object["session_id"] as? String, meetingIdentifier(sessionID),
          let locale = object["locale"] as? String, !locale.isEmpty, locale.utf8.count <= 64,
          locale.utf8.allSatisfy({ ($0 >= 48 && $0 <= 57) || ($0 >= 65 && $0 <= 90) || ($0 >= 97 && $0 <= 122) || $0 == 45 }),
          let startup = object["startup_ms"] == nil ? 8000 : meetingInteger(object["startup_ms"]), startup >= 100, startup <= 8000 else { throw MeetingAudioFailure(code: "invalid_request") }
    let tracks = object["tracks"] as? [String] ?? []
    if kind == "start" || kind == "request_access" {
        let consentKey = kind == "start" ? "explicit_start" : "explicit_setup"
        guard let consent = object[consentKey] as? NSNumber, CFGetTypeID(consent) == CFBooleanGetTypeID(), consent.boolValue,
              !tracks.isEmpty, tracks.count <= 2, Set(tracks).count == tracks.count,
              tracks.allSatisfy({ ["microphone", "system"].contains($0) }) else { throw MeetingAudioFailure(code: "invalid_request") }
        if kind == "start", object["explicit_setup"] != nil { throw MeetingAudioFailure(code: "invalid_request") }
        if kind == "request_access", object["explicit_start"] != nil || object["startup_ms"] != nil { throw MeetingAudioFailure(code: "invalid_request") }
    } else if kind == "install_locale" {
        guard let consent = object["explicit_setup"] as? NSNumber, CFGetTypeID(consent) == CFBooleanGetTypeID(), consent.boolValue,
              object["tracks"] == nil, object["explicit_start"] == nil, object["startup_ms"] == nil else { throw MeetingAudioFailure(code: "invalid_request") }
    } else if object["tracks"] != nil || object["explicit_start"] != nil || object["explicit_setup"] != nil { throw MeetingAudioFailure(code: "invalid_request") }
    return MeetingAudioConfiguration(kind: kind, sessionID: sessionID, locale: locale, tracks: tracks.sorted(), startupMilliseconds: startup)
}

func meetingIdentifier(_ text: String) -> Bool {
    !text.isEmpty && text.utf8.count <= 64 && text.utf8.allSatisfy { ($0 >= 48 && $0 <= 57) || ($0 >= 65 && $0 <= 90) || ($0 >= 97 && $0 <= 122) || $0 == 45 || $0 == 95 }
}
func meetingInteger(_ value: Any?) -> Int? {
    guard let value = value as? NSNumber, CFGetTypeID(value) != CFBooleanGetTypeID(), value.doubleValue.isFinite,
          value.doubleValue.rounded() == value.doubleValue, value.doubleValue >= 0, value.doubleValue <= Double(Int32.max) else { return nil }
    return value.intValue
}

struct MeetingAudioAvailability: Codable, Sendable {
    let speechAvailable: Bool
    let microphonePermission: String
    let systemPermission: Bool
    let installedLocales: [String]
    let requestedLocaleInstalled: Bool
    enum CodingKeys: String, CodingKey { case speechAvailable = "speech_available", microphonePermission = "microphone_permission", systemPermission = "system_permission", installedLocales = "installed_locales", requestedLocaleInstalled = "requested_locale_installed" }
}
struct MeetingAudioEvent: Codable, Sendable {
    let version: Int
    let sessionID: String
    let epoch: UInt64
    let kind: String
    var state: String? = nil
    var error: String? = nil
    var track: String? = nil
    var segmentID: String? = nil
    var revision: UInt64? = nil
    var startMS: Int64? = nil
    var endMS: Int64? = nil
    var final: Bool? = nil
    var text: String? = nil
    var availability: MeetingAudioAvailability? = nil
    var level: Double? = nil
    enum CodingKeys: String, CodingKey { case version, epoch, kind, state, error, track, revision, final, text, availability, level; case sessionID = "session_id", segmentID = "segment_id", startMS = "start_ms", endMS = "end_ms" }
}
struct MeetingAudioBatch: Codable {
    let version: Int
    let sessionID: String
    let events: [MeetingAudioEvent]
    enum CodingKeys: String, CodingKey { case version, events; case sessionID = "session_id" }
}
final class MeetingAudioEventQueue: @unchecked Sendable {
    private let lock = NSLock()
    private var events: [(MeetingAudioEvent, Int)] = []
    private var levels: [String: MeetingAudioEvent] = [:]
    private var bytes = 0
    private var staged: Data?
    private var stagedBytes = 0
    private var stagedCount = 0
    private var emergency: MeetingAudioEvent?
    private var failed = false
    private let encoder = JSONEncoder()

    func append(_ event: MeetingAudioEvent) -> Bool {
        guard meetingIdentifier(event.sessionID), let encoded = try? JSONEncoder().encode(event),
              encoded.count <= 131072, (event.text?.utf8.count ?? 0) <= 16384 else { return false }
        return lock.withLock {
            guard !failed else { return false }
            if event.kind == "level" {
                guard event.epoch > 0, let track = event.track, ["microphone", "system"].contains(track), let level = event.level,
                      level.isFinite, level >= 0, level <= 1, event.state == nil, event.error == nil, event.segmentID == nil,
                      event.revision == nil, event.startMS == nil, event.endMS == nil, event.final == nil, event.text == nil, event.availability == nil else { return false }
                if let previous = levels[track], previous.epoch > event.epoch { return true }
                levels[track] = event
                return true // independent, bounded lane: never consumes final capacity
            }
            guard event.level == nil else { return false }
            if event.kind == "transcript" || event.kind == "preview", let index = events.firstIndex(where: { $0.0.kind == event.kind && $0.0.epoch == event.epoch && $0.0.track == event.track && $0.0.segmentID == event.segmentID }) {
                let previous = events[index]
                if previous.0.final == true { return (try? encoder.encode(previous.0)) == encoded }
                guard (event.revision ?? 0) > (previous.0.revision ?? 0), bytes - previous.1 + encoded.count <= 524288 else { return false }
                events[index] = (event, encoded.count)
                bytes += encoded.count - previous.1
                return true
            }
            guard events.count + stagedCount < 128, bytes + encoded.count <= 524288 else { return false }
            events.append((event, encoded.count))
            bytes += encoded.count
            return true
        }
    }
    func poll(capacity: Int) -> (Int32, Data?) {
        guard capacity >= 0, capacity <= 1048576 else { return (-1, nil) }
        return lock.withLock {
            if staged == nil {
                if !events.isEmpty || !levels.isEmpty {
                    let reliable = Array(events.prefix(16))
                    var selected = reliable
                    for track in levels.keys.sorted().prefix(16 - selected.count) {
                        if let level = levels.removeValue(forKey: track), let encoded = try? encoder.encode(level) { selected.append((level, encoded.count)) }
                    }
                    guard let data = try? encoder.encode(MeetingAudioBatch(version: 1, sessionID: selected[0].0.sessionID, events: selected.map { $0.0 })), data.count <= 1048576 else { return (-1, nil) }
                    staged = data
                    stagedBytes = reliable.reduce(0) { $0 + $1.1 }
                    stagedCount = reliable.count
                    events.removeFirst(reliable.count)
                } else if let error = emergency {
                    staged = try? encoder.encode(MeetingAudioBatch(version: 1, sessionID: error.sessionID, events: [error]))
                    stagedCount = staged == nil ? 0 : 1
                    emergency = nil
                }
            }
            guard let data = staged else { return (0, nil) }
            if capacity >= data.count { staged = nil; bytes -= stagedBytes; stagedBytes = 0; stagedCount = 0 }
            return (Int32(data.count), data)
        }
    }
    func dropPartials() {
        lock.withLock {
            events.removeAll { event, size in
                if event.kind == "preview" { bytes -= size; return true }
                return false
            }
        }
    }
    func dropLevels() { lock.withLock { levels.removeAll() } }
    func failure(_ event: MeetingAudioEvent) {
        lock.withLock { if !failed { failed = true; emergency = event } }
    }
    func cancel() {
        lock.withLock {
            failed = true
            events.removeAll(); levels.removeAll()
            staged = nil; stagedBytes = 0; stagedCount = 0; bytes = 0
            emergency = nil
        }
    }
}

func meetingUnsigned(_ value: Any?) -> UInt64? {
    guard let number = value as? NSNumber, CFGetTypeID(number) != CFBooleanGetTypeID() else { return nil }
    return UInt64(number.stringValue)
}
