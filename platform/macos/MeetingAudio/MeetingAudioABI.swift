import Foundation

final class MeetingAudioAdmission: @unchecked Sendable {
    private let lock = NSLock()
    private let limit: Int
    private var active = 0
    init(_ limit: Int) { self.limit = limit }
    func acquire() -> MeetingAudioPermit? {
        lock.withLock { guard active < limit else { return nil }; active += 1; return MeetingAudioPermit(self) }
    }
    fileprivate func release() { lock.withLock { active -= 1 } }
}
final class MeetingAudioPermit: @unchecked Sendable {
    private let lock = NSLock()
    private let admission: MeetingAudioAdmission
    private var released = false
    init(_ admission: MeetingAudioAdmission) { self.admission = admission }
    func release() {
        let release = lock.withLock { () -> Bool in guard !released else { return false }; released = true; return true }
        if release { admission.release() }
    }
    // No deinit release: cancellation must not free an occupied physical slot.
}
private final class MeetingAudioEntry: @unchecked Sendable {
    private let setupLock = NSLock()
    private var setupFinished = false
    let configuration: MeetingAudioConfiguration
    let queue: MeetingAudioEventQueue
    let session: MeetingAudioSession?
    let created = DispatchTime.now().uptimeNanoseconds
    var probe: Task<Void, Never>?
    init(_ configuration: MeetingAudioConfiguration, queue: MeetingAudioEventQueue, session: MeetingAudioSession?) { self.configuration = configuration; self.queue = queue; self.session = session }
    func finishSetup(_ event: MeetingAudioEvent, timeout: Bool = false) -> Bool {
        setupLock.withLock {
            guard !setupFinished else { return false }
            setupFinished = true
            if timeout { queue.failure(event) } else { _ = queue.append(event) }
            return true
        }
    }
}
final class MeetingAudioRegistry: @unchecked Sendable {
    private let lock = NSLock()
    private let factory: any MeetingAudioFactory
    private let audio = MeetingAudioAdmission(1)
    private let probes = MeetingAudioAdmission(2)
    private let setup = MeetingAudioAdmission(1)
    private var entries: [UInt64: MeetingAudioEntry] = [:]
    private var next: UInt64 = 1
    init(factory: any MeetingAudioFactory) { self.factory = factory }
    func open(_ data: Data) -> UInt64 {
        guard let configuration = try? decodeMeetingAudioConfiguration(data) else { return 0 }
        reapProbes()
        lock.lock()
        guard entries.count < 4, !entries.values.contains(where: { $0.configuration.sessionID == configuration.sessionID }) else { lock.unlock(); return 0 }
        var handle: UInt64
        repeat { handle = next; next = next == UInt64.max ? 1 : next + 1 } while entries[handle] != nil
        let entry: MeetingAudioEntry
        if configuration.kind == "start" {
            guard let permit = audio.acquire() else { lock.unlock(); return 0 }
            let session = MeetingAudioSession(configuration, factory: factory, releaseSlot: { permit.release() })
            entry = MeetingAudioEntry(configuration, queue: session.queue, session: session)
        } else if configuration.kind == "probe" {
            guard let permit = probes.acquire() else { lock.unlock(); return 0 }
            entry = MeetingAudioEntry(configuration, queue: MeetingAudioEventQueue(), session: nil)
            let factory = self.factory
            entry.probe = Task.detached(priority: .utility) { [weak entry] in
                let availability = await factory.probe(locale: configuration.locale)
                defer { permit.release() }
                guard !Task.isCancelled, let entry else { return }
                _ = entry.queue.append(MeetingAudioEvent(version: 1, sessionID: configuration.sessionID, epoch: 0, kind: "availability", availability: availability))
            }
        } else {
            guard let permit = setup.acquire() else { lock.unlock(); return 0 }
            entry = MeetingAudioEntry(configuration, queue: MeetingAudioEventQueue(), session: nil)
            let factory = self.factory
            entry.probe = Task.detached(priority: .utility) { [weak entry] in
                defer { permit.release() }
                var failure: String?
                do {
                    try Task.checkCancellation()
                    if configuration.kind == "request_access" { try await factory.requestAccess(tracks: configuration.tracks) }
                    else { try await factory.installLocale(configuration.locale) }
                } catch is CancellationError { return }
                catch { failure = (error as? MeetingAudioFailure)?.code ?? "setup_failed" }
                guard !Task.isCancelled, let entry else { return }
                _ = entry.finishSetup(MeetingAudioEvent(version: 1, sessionID: configuration.sessionID, epoch: 0, kind: "operation", state: failure == nil ? "completed" : "failed", error: failure))
            }
            DispatchQueue.global(qos: .utility).asyncAfter(deadline: .now() + .seconds(300)) { [weak entry] in
                guard let entry else { return }
                if entry.finishSetup(MeetingAudioEvent(version: 1, sessionID: configuration.sessionID, epoch: 0, kind: "operation", state: "failed", error: "setup_timeout"), timeout: true) { entry.probe?.cancel() }
            }
        }
        entries[handle] = entry
        lock.unlock()
        entry.session?.begin()
        return handle
    }
    func poll(_ handle: UInt64, capacity: Int) -> (Int32, Data?) {
        reapProbes()
        guard let entry = lock.withLock({ entries[handle] }) else { return (-1, nil) }
        return entry.queue.poll(capacity: capacity)
    }
    func control(_ handle: UInt64, data: Data) -> Int32 {
        guard data.count > 0, data.count <= 512, String(data: data, encoding: .utf8) != nil,
              let raw = try? JSONSerialization.jsonObject(with: data), let object = raw as? [String: Any],
              Set(object.keys) == ["version", "session_id", "epoch", "operation"], meetingInteger(object["version"]) == 1,
              let id = object["session_id"] as? String, let epoch = meetingUnsigned(object["epoch"]), epoch > 0,
              let operation = object["operation"] as? String, ["pause", "resume", "stop"].contains(operation),
              let entry = lock.withLock({ entries[handle] }), entry.configuration.sessionID == id, let session = entry.session else { return -1 }
        return session.control(operation, expectedEpoch: epoch)
    }
    func cancel(_ handle: UInt64) {
        let entry = lock.withLock { entries.removeValue(forKey: handle) }
        entry?.probe?.cancel()
        entry?.session?.cancel()
        if entry?.session == nil { entry?.queue.cancel() }
    }
    private func reapProbes() {
        let now = DispatchTime.now().uptimeNanoseconds
        let expired: [MeetingAudioEntry] = lock.withLock {
            let expired = entries.filter { $0.value.session == nil && now >= $0.value.created && now - $0.value.created > ($0.value.configuration.kind == "probe" ? 30000000000 : 600000000000) }
            for (handle, _) in expired { entries.removeValue(forKey: handle) }
            return expired.map { $0.value }
        }
        for entry in expired { entry.probe?.cancel(); entry.queue.cancel() }
    }
}
private let meetingAudioRegistry = MeetingAudioRegistry(factory: NativeMeetingAudioFactory())

@_cdecl("blur_meeting_audio_open")
public func blurMeetingAudioOpen(_ pointer: UnsafePointer<UInt8>?, _ length: Int32) -> UInt64 {
    autoreleasepool {
        guard let pointer, length > 0, length <= 4096 else { return 0 }
        return meetingAudioRegistry.open(Data(bytes: pointer, count: Int(length)))
    }
}
@_cdecl("blur_meeting_audio_poll")
public func blurMeetingAudioPoll(_ handle: UInt64, _ output: UnsafeMutablePointer<UInt8>?, _ capacity: Int32) -> Int32 {
    autoreleasepool {
        guard capacity >= 0, capacity <= 1048576 else { return -1 }
        let (size, data) = meetingAudioRegistry.poll(handle, capacity: output == nil ? 0 : Int(capacity))
        if let output, let data, size > 0, Int(capacity) >= data.count { data.copyBytes(to: output, count: data.count) }
        return size
    }
}
@_cdecl("blur_meeting_audio_control")
public func blurMeetingAudioControl(_ handle: UInt64, _ pointer: UnsafePointer<UInt8>?, _ length: Int32) -> Int32 {
    autoreleasepool {
        guard let pointer, length > 0, length <= 512 else { return -1 }
        return meetingAudioRegistry.control(handle, data: Data(bytes: pointer, count: Int(length)))
    }
}
@_cdecl("blur_meeting_audio_cancel")
public func blurMeetingAudioCancel(_ handle: UInt64) { meetingAudioRegistry.cancel(handle) }
