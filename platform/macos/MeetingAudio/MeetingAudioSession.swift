import Foundation
import CoreMedia

protocol MeetingAudioDriver: AnyObject, Sendable {
    func start(_ configuration: MeetingAudioConfiguration, epoch: UInt64, origin: CMTime, cancelled: MeetingAudioCancellation, emit: @escaping @Sendable (MeetingAudioEvent) -> Void) async throws
    func seal()
    func stop(finalize: Bool) async throws
}
protocol MeetingAudioFactory: Sendable {
    func probe(locale: String) async -> MeetingAudioAvailability
    func makeDriver() throws -> any MeetingAudioDriver
    func requestAccess(tracks: [String]) async throws
    func installLocale(_ locale: String) async throws
}
extension MeetingAudioFactory {
    func requestAccess(tracks: [String]) async throws { throw MeetingAudioFailure(code: "setup_unavailable") }
    func installLocale(_ locale: String) async throws { throw MeetingAudioFailure(code: "setup_unavailable") }
}
final class MeetingAudioCancellation: @unchecked Sendable {
    private let lock = NSLock()
    private var stopped = false
    func cancel() { lock.withLock { stopped = true } }
    func check() throws { if lock.withLock({ stopped }) || Task.isCancelled { throw CancellationError() } }
}

protocol MeetingAudioActivityLease: AnyObject, Sendable { func end() }

// Recording is explicit user work even when the menu-bar app is not visible.
// The activity prevents App Nap, but deliberately permits normal idle sleep.
final class MeetingAudioProcessActivity: MeetingAudioActivityLease, @unchecked Sendable {
    private let lock = NSLock()
    private var token: (any NSObjectProtocol)?
    init() { token = ProcessInfo.processInfo.beginActivity(options: .userInitiatedAllowingIdleSystemSleep, reason: "Blur is recording a voice note") }
    func end() {
        let token = lock.withLock { let value = self.token; self.token = nil; return value }
        if let token { ProcessInfo.processInfo.endActivity(token) }
    }
    deinit { end() }
}

final class MeetingAudioSession: @unchecked Sendable {
    let configuration: MeetingAudioConfiguration
    let queue = MeetingAudioEventQueue()
    private let lock = NSLock()
    private let factory: any MeetingAudioFactory
    private let origin = CMClockGetTime(CMClockGetHostTimeClock())
    private let releaseSlot: @Sendable () -> Void
    private let beginActivity: @Sendable () -> any MeetingAudioActivityLease
    private var activity: (any MeetingAudioActivityLease)?
    private var epoch: UInt64 = 1
    private var state = "starting"
    private var callbackEpoch: UInt64? = 1
    private var driver: (any MeetingAudioDriver)?
    private var startup: Task<Void, Never>?
    private var transition: Task<Void, Never>?
    private var cancellation = MeetingAudioCancellation()
    private var stopping = false
    private var released = false
    private var transitionSequence: UInt64 = 0

    init(_ configuration: MeetingAudioConfiguration, factory: any MeetingAudioFactory, beginActivity: @escaping @Sendable () -> any MeetingAudioActivityLease = { MeetingAudioProcessActivity() }, releaseSlot: @escaping @Sendable () -> Void) {
        self.configuration = configuration
        self.factory = factory
        self.releaseSlot = releaseSlot
        self.beginActivity = beginActivity
    }
    func begin() {
        let event = MeetingAudioEvent(version: 1, sessionID: configuration.sessionID, epoch: 1, kind: "state", state: "starting")
        _ = queue.append(event)
        beginEpoch()
    }
    private func beginEpoch() {
        lock.lock()
        let context = (epoch, cancellation)
        activity = beginActivity()
        let task = Task.detached(priority: .userInitiated) { [self] in
            do {
                let available = await factory.probe(locale: configuration.locale)
                try context.1.check()
                guard available.speechAvailable else { throw MeetingAudioFailure(code: "speech_unavailable") }
                guard available.requestedLocaleInstalled else { throw MeetingAudioFailure(code: "assets_missing") }
                if configuration.tracks.contains("microphone"), available.microphonePermission != "authorized" { throw MeetingAudioFailure(code: "microphone_permission") }
                if configuration.tracks.contains("system"), !available.systemPermission { throw MeetingAudioFailure(code: "system_permission") }
                let native = try factory.makeDriver()
                lock.withLock { driver = native }
                try context.1.check()
                try await native.start(configuration, epoch: context.0, origin: origin, cancelled: context.1) { [weak self] event in self?.receive(event) }
                let running = lock.withLock { () -> Bool in
                    guard epoch == context.0, state == "starting" else { return false }
                    state = "running"
                    return queue.append(MeetingAudioEvent(version: 1, sessionID: configuration.sessionID, epoch: context.0, kind: "state", state: "running"))
                }
                if !running {
                    native.seal()
                    if lock.withLock({ state == "running" && epoch == context.0 }) { fail("event_overflow") }
                }
            } catch {
                let shouldFail = lock.withLock { state == "starting" && epoch == context.0 }
                if shouldFail { fail((error as? MeetingAudioFailure)?.code ?? "start_failed") }
            }
        }
        startup = task
        lock.unlock()
        DispatchQueue.global(qos: .utility).asyncAfter(deadline: .now() + .milliseconds(configuration.startupMilliseconds)) { [weak self] in
            guard let self else { return }
            if self.lock.withLock({ self.state == "starting" && self.epoch == context.0 }) { self.fail("startup_timeout") }
        }
    }
    func receive(_ event: MeetingAudioEvent) {
        var nativeFailure: String?
        let queued = lock.withLock { () -> Bool in
            guard event.version == 1, event.sessionID == configuration.sessionID, event.epoch == callbackEpoch else { return true }
            if event.kind == "error" { nativeFailure = event.error ?? "device_failed"; return true }
            if event.kind == "level" {
                guard state == "running" || state == "starting", let track = event.track, configuration.tracks.contains(track) else { return true }
                _ = queue.append(event) // meter admission is always drop-eligible
                return true
            }
            guard event.kind == "transcript" || event.kind == "preview", let track = event.track, configuration.tracks.contains(track),
                  let segment = event.segmentID, meetingIdentifier(segment), let revision = event.revision, revision > 0,
                  let start = event.startMS, let end = event.endMS, start >= 0, end >= start, end <= meetingAudioMaximumMilliseconds,
                  let text = event.text, !text.isEmpty, text.utf8.count <= 16384, let final = event.final, final == (event.kind == "transcript"),
                  state == "running" || state == "starting" || state == "stopping" && final else { return true }
            // Admission and enqueue are atomic with epoch invalidation, so a
            // callback cannot enqueue after a completed pause acknowledgement.
            return queue.append(event)
        }
        if let nativeFailure { fail(nativeFailure) }
        else if !queued { fail("event_overflow") }
    }
    func control(_ operation: String, expectedEpoch: UInt64) -> Int32 {
        var action: String?
        var queueFailed = false
        let accepted = lock.withLock { () -> Bool in
            guard epoch == expectedEpoch, !stopping else { return false }
            switch operation {
            case "pause":
                guard state == "running" else { return false }
                epoch += 1; callbackEpoch = nil; state = "pausing"; cancellation.cancel(); action = "paused"
            case "resume":
                guard state == "paused" else { return false }
                epoch += 1; callbackEpoch = epoch; state = "starting"; cancellation = MeetingAudioCancellation(); driver = nil; startup = nil; action = "resume"
            case "stop":
                guard ["starting", "running", "paused"].contains(state) else { return false }
                if state == "starting" { cancellation.cancel(); callbackEpoch = nil }
                state = "stopping"; action = "completed"
            default: return false
            }
            if action != "resume" { stopping = true }
            if operation == "pause" { queue.dropPartials() }
            queue.dropLevels()
            queueFailed = !queue.append(MeetingAudioEvent(version: 1, sessionID: configuration.sessionID, epoch: epoch, kind: "state", state: state))
            return true
        }
        guard accepted, let action else { return 0 }
        if queueFailed {
            fail("event_overflow", schedule: action == "resume")
            if action != "resume" { scheduleStop(finalize: false, target: "error") }
            return 1
        }
        if action == "resume" { beginEpoch() }
        else { scheduleStop(finalize: operation == "stop", target: action) }
        return 1
    }
    func cancel() {
        let shouldStop = lock.withLock { () -> Bool in
            callbackEpoch = nil; epoch += 1; state = "cancelled"; cancellation.cancel()
            if stopping { return false }; stopping = true; return true
        }
        queue.cancel()
        driverSnapshot()?.seal()
        if shouldStop { scheduleStop(finalize: false, target: "cancelled") }
    }
    private func driverSnapshot() -> (any MeetingAudioDriver)? { lock.withLock { driver } }
    private func scheduleStop(finalize: Bool, target: String) {
        let context = lock.withLock { () -> (Task<Void, Never>?, UInt64) in transitionSequence += 1; return (startup, transitionSequence) }
        let previous = context.0
        driverSnapshot()?.seal()
        let task = Task.detached(priority: .userInitiated) { [self] in
            await previous?.value
            do {
                try await driverSnapshot()?.stop(finalize: finalize)
                endActivity()
                let stateEvent = lock.withLock { () -> (String, UInt64)? in
                    stopping = false
                    if state == "cancelled" { return nil }
                    if state == "error" { return nil }
                    let nextEpoch = target == "completed" ? epoch + 1 : epoch
                    let queued = queue.append(MeetingAudioEvent(version: 1, sessionID: configuration.sessionID, epoch: nextEpoch, kind: "state", state: target))
                    if !queued { return ("queue_failed", epoch) }
                    state = target; epoch = nextEpoch
                    if target == "completed" { callbackEpoch = nil }
                    return (state, epoch)
                }
                if stateEvent?.0 == "queue_failed" { fail("event_overflow", schedule: false) }
                if target != "paused" || lock.withLock({ state == "cancelled" || state == "error" }) { releaseOnce() }
            } catch { endActivity(); fail((error as? MeetingAudioFailure)?.code ?? "stop_failed", schedule: false) }
        }
        lock.withLock { transition = task }
        DispatchQueue.global(qos: .utility).asyncAfter(deadline: .now() + .seconds(8)) { [weak self] in
            guard let self else { return }
            if self.lock.withLock({ self.stopping && self.transitionSequence == context.1 }) { self.fail("stop_timeout", schedule: false) }
        }
    }
    func fail(_ code: String, schedule: Bool = true) {
        let context = lock.withLock { () -> (UInt64, Bool)? in
            guard state != "error", state != "completed", state != "cancelled" else { return nil }
            state = "error"; callbackEpoch = nil; cancellation.cancel()
            let shouldStop = schedule && !stopping
            if shouldStop { stopping = true }
            return (epoch, shouldStop)
        }
        guard let context else { return }
        driverSnapshot()?.seal()
        queue.dropPartials()
        queue.dropLevels()
        queue.failure(MeetingAudioEvent(version: 1, sessionID: configuration.sessionID, epoch: context.0, kind: "error", error: code))
        if context.1 { scheduleStop(finalize: false, target: "error") }
    }
    private func endActivity() {
        let activity = lock.withLock { let value = self.activity; self.activity = nil; return value }
        activity?.end()
    }
    private func releaseOnce() {
        let release = lock.withLock { () -> Bool in if released { return false }; released = true; return true }
        if release { releaseSlot() }
    }
}
