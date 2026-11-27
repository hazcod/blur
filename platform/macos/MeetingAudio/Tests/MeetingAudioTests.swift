import Foundation
import AVFoundation
import CoreMedia

func requireMeeting(_ condition: @autoclosure () -> Bool, _ message: String) throws {
    if !condition() { throw NSError(domain: "MeetingAudioTests", code: 1, userInfo: [NSLocalizedDescriptionKey: message]) }
}
@main struct MeetingAudioTests {
    static func main() async throws {
        let valid = Data(#"{"version":1,"kind":"start","session_id":"synthetic","locale":"en-US","tracks":["microphone","system"],"explicit_start":true,"startup_ms":1000}"#.utf8)
        let configuration = try decodeMeetingAudioConfiguration(valid)
        try await testMeetingAudioLongTimeline(configuration)
        try await testMeetingAudioBuffering()
        try await testMeetingAudioLongQueueSchedules()
        try await testMeetingAudioLevels(configuration)
        try await testMeetingAudioRecordingActivity(configuration)
        try requireMeeting(configuration.tracks == ["microphone", "system"], "explicit dual-track start rejected")
        for text in [
            #"{"version":1,"kind":"start","session_id":"synthetic","locale":"en-US","tracks":[],"explicit_start":true}"#,
            #"{"version":1,"kind":"start","session_id":"synthetic","locale":"en-US","tracks":["microphone"]}"#,
            #"{"version":1,"kind":"start","session_id":"synthetic","locale":"en-US","tracks":["microphone","microphone"],"explicit_start":true}"#,
            #"{"version":1,"kind":"start","session_id":"synthetic","locale":"en-US","tracks":["microphone"],"explicit_start":true,"file":"secret.pcm"}"#,
        ] {
            do { _ = try decodeMeetingAudioConfiguration(Data(text.utf8)); throw NSError(domain: "MeetingAudioTests", code: 2) }
            catch is MeetingAudioFailure { }
        }
        print("PASS explicit start and strict track/permission schema")
        let setupData = Data(#"{"version":1,"kind":"request_access","session_id":"setup-synthetic","locale":"en-US","tracks":["microphone","system"],"explicit_setup":true}"#.utf8)
        let installData = Data(#"{"version":1,"kind":"install_locale","session_id":"install-synthetic","locale":"en-US","explicit_setup":true}"#.utf8)
        let setupFactory = SyntheticMeetingFactory()
        let setupRegistry = MeetingAudioRegistry(factory: setupFactory)
        let implicitSetup = Data(#"{"version":1,"kind":"request_access","session_id":"implicit-setup","locale":"en-US","tracks":["microphone"]}"#.utf8)
        try requireMeeting(setupRegistry.open(implicitSetup) == 0 && setupFactory.setupCounts() == [0, 0], "implicit permission action reached native setup")
        let setupHandle = setupRegistry.open(setupData)
        try requireMeeting(setupHandle > 0, "explicit synthetic permission action rejected")
        let setupEvents = try await collectMeetingRegistry(setupRegistry, handle: setupHandle)
        try requireMeeting(setupEvents.count == 1 && setupEvents[0].kind == "operation" && setupEvents[0].state == "completed" && setupFactory.setupCounts() == [1, 0] && setupFactory.latest() == nil, "explicit permissions started audio or installed assets")
        setupRegistry.cancel(setupHandle)
        let installHandle = setupRegistry.open(installData)
        let installEvents = try await collectMeetingRegistry(setupRegistry, handle: installHandle)
        try requireMeeting(installEvents.count == 1 && installEvents[0].state == "completed" && setupFactory.setupCounts() == [1, 1] && setupFactory.latest() == nil, "explicit synthetic install started audio or requested permissions")
        setupRegistry.cancel(installHandle)
        let slowSetupFactory = SyntheticMeetingFactory()
        slowSetupFactory.setupDelayMilliseconds = 100
        let slowSetup = MeetingAudioRegistry(factory: slowSetupFactory)
        let pendingSetup = slowSetup.open(setupData)
        for _ in 0..<100 { if slowSetupFactory.setupCounts()[0] == 1 { break }; try await Task.sleep(for: .milliseconds(5)) }
        slowSetup.cancel(pendingSetup)
        try requireMeeting(slowSetup.open(setupData) == 0, "cancel freed an outstanding setup operation before actual exit")
        try await Task.sleep(for: .milliseconds(150))
        let nextSetup = slowSetup.open(setupData)
        try requireMeeting(nextSetup > 0, "actual setup exit did not release its single slot")
        slowSetup.cancel(nextSetup)
        print("PASS explicit setup gates, operation isolation and bounded outstanding work")
        let queue = MeetingAudioEventQueue()
        let partial = MeetingAudioEvent(version: 1, sessionID: "synthetic", epoch: 1, kind: "preview", track: "microphone", segmentID: "microphone-1-preview", revision: 1, startMS: 0, endMS: 100, final: false, text: "hello")
        try requireMeeting(queue.append(partial), "partial event not admitted")
        var revised = partial
        revised.revision = 2
        revised.text = "hello world"
        try requireMeeting(queue.append(revised), "partial replacement rejected")
        let size = queue.poll(capacity: 0).0
        try requireMeeting(size > 0, "events not available")
        let data = queue.poll(capacity: Int(size)).1 ?? Data()
        let decoder = JSONDecoder()
        let batch = try decoder.decode(MeetingAudioBatch.self, from: data)
        try requireMeeting(batch.events.count == 1 && batch.events[0].text == "hello world", "partials grew queue instead of replacing")
        print("PASS bounded event queue and partial replacement")
        let stagedQueue = MeetingAudioEventQueue()
        for index in 0..<128 {
            let event = MeetingAudioEvent(version: 1, sessionID: "synthetic", epoch: 1, kind: "transcript", track: "system", segmentID: "staged-\(index)", revision: 1, startMS: Int64(index), endMS: Int64(index + 1), final: true, text: "x")
            try requireMeeting(stagedQueue.append(event), "staged queue filled before its declared event bound")
        }
        let stagedSize = stagedQueue.poll(capacity: 0).0
        let overflowEvent = MeetingAudioEvent(version: 1, sessionID: "synthetic", epoch: 1, kind: "transcript", track: "system", segmentID: "staged-overflow", revision: 1, startMS: 128, endMS: 129, final: true, text: "x")
        try requireMeeting(stagedSize > 0 && !stagedQueue.append(overflowEvent), "size query removed staged events from the 128-event admission bound")
        try requireMeeting(stagedQueue.poll(capacity: Int(stagedSize) - 1).0 == stagedSize && !stagedQueue.append(overflowEvent), "short poll buffer released staged event capacity")
        let stagedData = stagedQueue.poll(capacity: Int(stagedSize)).1 ?? Data()
        let stagedBatch = try decoder.decode(MeetingAudioBatch.self, from: stagedData)
        try requireMeeting(stagedBatch.events.count == 16, "staged batch did not contain declared 16 events")
        for index in 0..<16 {
            var event = overflowEvent
            event.segmentID = "refill-\(index)"
            try requireMeeting(stagedQueue.append(event), "consuming staged batch did not release its event capacity")
        }
        try requireMeeting(!stagedQueue.append(overflowEvent), "consuming one staged batch released too much event capacity")
        let replacementQueue = MeetingAudioEventQueue()
        for index in 0..<127 {
            var event = overflowEvent
            event.segmentID = "replacement-\(index)"
            try requireMeeting(replacementQueue.append(event), "replacement queue filled before declared event bound")
        }
        try requireMeeting(replacementQueue.append(partial), "preview failed to fill last event slot")
        let replacementSize = replacementQueue.poll(capacity: 0).0
        try requireMeeting(replacementSize > 0 && replacementQueue.append(revised), "preview replacement consumed an additional slot while batch staged")
        try requireMeeting(!replacementQueue.append(overflowEvent), "preview replacement released staged event count")
        replacementQueue.cancel()
        try requireMeeting(replacementQueue.poll(capacity: 0).0 == 0 && !replacementQueue.append(overflowEvent), "cancel retained staged output or reopened event admission")
        print("PASS staged size queries and short buffers retain event admission count")
        var identity = MeetingAudioResultIdentity(sessionID: "synthetic", track: "microphone", epoch: 1)
        let earlyRange = CMTimeRange(start: CMTime(value: 20, timescale: 1000), duration: CMTime(value: 180, timescale: 1000))
        let revisedRange = CMTimeRange(start: CMTime(value: 5, timescale: 1000), duration: CMTime(value: 145, timescale: 1000))
        let firstPreview = try identity.event(text: "a tentative phrase", range: earlyRange, finalStream: false, isFinal: false)
        let lastPreview = try identity.event(text: "a changed preview", range: revisedRange, finalStream: false, isFinal: true)
        try requireMeeting(firstPreview.segmentID == lastPreview.segmentID && lastPreview.kind == "preview" && lastPreview.final == false && lastPreview.startMS == 5 && lastPreview.endMS == 150 && lastPreview.revision == 2, "preview boundaries were normalized or volatile final became durable")
        let standaloneFinal = try identity.event(text: "independent complete phrase", range: earlyRange, finalStream: true, isFinal: true)
        let nextFinal = try identity.event(text: "next complete phrase", range: revisedRange, finalStream: true, isFinal: true)
        try requireMeeting(standaloneFinal.kind == "transcript" && standaloneFinal.final == true && standaloneFinal.revision == 1 && standaloneFinal.segmentID != lastPreview.segmentID && nextFinal.segmentID != standaloneFinal.segmentID && nextFinal.startMS == 5, "immutable final identity depended on preview identity or invented boundaries")
        do { _ = try identity.event(text: "unexpected tentative output", range: earlyRange, finalStream: true, isFinal: false); throw NSError(domain: "MeetingAudioTests", code: 4) }
        catch is MeetingAudioFailure { }
        print("PASS separate replaceable previews and independent immutable final stream")
        let factory = SyntheticMeetingFactory()
        let released = SyntheticMeetingFlag()
        let session = MeetingAudioSession(configuration, factory: factory, releaseSlot: { released.set() })
        session.begin()
        _ = try await collectMeeting(session.queue, until: "running")
        let oldDriver = factory.latest()!
        try requireMeeting(session.control("pause", expectedEpoch: 1) == 1, "pause rejected")
        oldDriver.send(text: "late private words", final: true)
        let paused = try await collectMeeting(session.queue, until: "paused")
        try requireMeeting(!paused.contains { $0.text == "late private words" } && !released.get(), "pause admitted late text or released session slot")
        try requireMeeting(session.control("resume", expectedEpoch: 2) == 1, "resume rejected")
        _ = try await collectMeeting(session.queue, until: "running")
        let activeDriver = factory.latest()!
        try requireMeeting(oldDriver.timelineOrigin() == activeDriver.timelineOrigin(), "resume reset the shared meeting clock")
        try requireMeeting(session.control("stop", expectedEpoch: 3) == 1, "stop rejected")
        let stopped = try await collectMeeting(session.queue, until: "completed")
        try requireMeeting(stopped.contains { $0.text == "accepted final during stop" && $0.final == true } && released.get(), "stop lost accepted finals or did not release actual work")
        activeDriver.send(text: "late after completed", final: true)
        try requireMeeting(session.queue.poll(capacity: 0).0 == 0, "completed session accepted late callback")
        let deniedFactory = SyntheticMeetingFactory()
        deniedFactory.microphonePermission = "denied"
        let denied = MeetingAudioSession(configuration, factory: deniedFactory, releaseSlot: {})
        denied.begin()
        let deniedEvents = try await collectMeeting(denied.queue, until: "error")
        try requireMeeting(deniedEvents.contains { $0.error == "microphone_permission" } && deniedFactory.latest() == nil, "missing track permission silently downgraded capture")
        let slowFactory = SyntheticMeetingFactory()
        slowFactory.stopDelayMilliseconds = 150
        let registry = MeetingAudioRegistry(factory: slowFactory)
        let handle = registry.open(valid)
        try requireMeeting(handle > 0, "synthetic registry start failed")
        for _ in 0..<100 { if slowFactory.latest() != nil { break }; try await Task.sleep(for: .milliseconds(5)) }
        registry.cancel(handle)
        try requireMeeting(registry.open(valid) == 0, "cancel released native slot before actual device stopped")
        try await Task.sleep(for: .milliseconds(250))
        let next = registry.open(valid)
        try requireMeeting(next > 0, "actual completed cleanup did not release slot")
        registry.cancel(next)
        let fullFactory = SyntheticMeetingFactory()
        let fullReleased = SyntheticMeetingFlag()
        let fullSession = MeetingAudioSession(configuration, factory: fullFactory, releaseSlot: { fullReleased.set() })
        fullSession.begin()
        _ = try await collectMeeting(fullSession.queue, until: "running")
        for index in 0..<128 {
            let event = MeetingAudioEvent(version: 1, sessionID: "synthetic", epoch: 1, kind: "transcript", track: "system", segmentID: "system-1-\(index)", revision: 1, startMS: Int64(index), endMS: Int64(index + 1), final: true, text: "retained final")
            try requireMeeting(fullSession.queue.append(event), "final queue filled before declared event bound")
        }
        try requireMeeting(fullSession.control("stop", expectedEpoch: 1) == 1, "full transcript queue prevented explicit stop")
        let overflow = try await collectMeeting(fullSession.queue, until: "error")
        try requireMeeting(overflow.filter { $0.final == true }.count == 128 && overflow.contains { $0.error == "event_overflow" }, "overflow lost already accepted finals or hid failure")
        for _ in 0..<100 { if fullReleased.get() { break }; try await Task.sleep(for: .milliseconds(5)) }
        try requireMeeting(fullReleased.get(), "full queue left audio running after stop")
        let changedFactory = SyntheticMeetingFactory()
        let changedReleased = SyntheticMeetingFlag()
        let changedSession = MeetingAudioSession(configuration, factory: changedFactory, releaseSlot: { changedReleased.set() })
        changedSession.begin()
        _ = try await collectMeeting(changedSession.queue, until: "running")
        changedFactory.latest()!.failDevice()
        let changed = try await collectMeeting(changedSession.queue, until: "error")
        try requireMeeting(changed.contains { $0.error == "device_changed" }, "device change silently continued recording")
        for _ in 0..<100 { if changedReleased.get() { break }; try await Task.sleep(for: .milliseconds(5)) }
        try requireMeeting(changedReleased.get(), "device error did not complete actual cleanup")
        let timeoutFactory = SyntheticMeetingFactory()
        timeoutFactory.stopDelayMilliseconds = 10000
        let timeoutReleased = SyntheticMeetingFlag()
        let timeoutSession = MeetingAudioSession(configuration, factory: timeoutFactory, releaseSlot: { timeoutReleased.set() })
        timeoutSession.begin()
        _ = try await collectMeeting(timeoutSession.queue, until: "running")
        try requireMeeting(timeoutSession.control("stop", expectedEpoch: 1) == 1, "bounded stop rejected")
        let timedOut = try await collectMeeting(timeoutSession.queue, until: "error", attempts: 2200)
        try requireMeeting(timedOut.contains { $0.error == "stop_timeout" } && !timedOut.contains { $0.state == "completed" } && !timeoutReleased.get(), "stop timeout reported success or released physical work early")
        timeoutFactory.latest()!.send(text: "late after stop timeout", final: true)
        try requireMeeting(timeoutSession.queue.poll(capacity: 0).0 == 0, "stop timeout admitted late words")
        for _ in 0..<700 { if timeoutReleased.get() { break }; try await Task.sleep(for: .milliseconds(5)) }
        try requireMeeting(timeoutReleased.get(), "actual stop after deadline did not release physical work")
        print("PASS epochs, stop final drain, permission gates and physical work admission")
        let format = AVAudioFormat(commonFormat: .pcmFormatFloat32, sampleRate: 16000, channels: 1, interleaved: false)!
        let pcm = AVAudioPCMBuffer(pcmFormat: format, frameCapacity: 1024)!
        pcm.frameLength = 1024
        pcm.floatChannelData![0].initialize(repeating: 0, count: 1024)
        let pcmQueue = MeetingAudioPCMQueue()
        for _ in 0..<128 { try requireMeeting(pcmQueue.offer(MeetingAudioPCM(pcm, startMilliseconds: 0)), "bounded PCM admission failed early") }
        try requireMeeting(!pcmQueue.offer(MeetingAudioPCM(pcm, startMilliseconds: 0)), "PCM queue accepted unbounded producer")
        pcmQueue.finish()
        var clock = MeetingAudioClock(sampleRate: 16000)
        let first = try clock.place(milliseconds: 0, frames: 1024)
        let second = try clock.place(milliseconds: 63.5, frames: 1024)
        let gap = try clock.place(milliseconds: 500, frames: 1024)
        try requireMeeting(first.value == 0 && second.value == 1024 && gap.value == 8000, "track clock overlapped buffers or erased pause gap")
        let origin = CMTime(seconds: 100, preferredTimescale: 1000000000)
        let pts = CMTime(seconds: 100.5, preferredTimescale: 1000000000)
        let systemTime = CMSyncConvertTime(pts, from: CMClockGetHostTimeClock(), to: CMClockGetHostTimeClock())
        let microphoneHost = CMClockMakeHostTimeFromSystemUnits(CMClockConvertHostTimeToSystemUnits(pts))
        let systemMS = try meetingAudioMilliseconds(hostTime: systemTime, origin: origin)
        let microphoneMS = try meetingAudioMilliseconds(hostTime: microphoneHost, origin: origin)
        try requireMeeting(abs(systemMS - 500) < 0.001 && abs(microphoneMS - 500) < 0.001, "microphone and system clocks used different origins")
        let largeJSON = try JSONSerialization.jsonObject(with: Data(#"{"epoch":18446744073709551615}"#.utf8)) as! [String: Any]
        try requireMeeting(meetingUnsigned(largeJSON["epoch"]) == UInt64.max && meetingUnsigned(NSNumber(value: true)) == nil, "epoch parser rounded UInt64 or accepted bool")
        print("PASS generated PCM bounds, shared microphone/system origin and sample-accurate clock")
    }
}

// Virtual timestamps exercise long sessions without recording audio or waiting.
func testMeetingAudioLongTimeline(_ configuration: MeetingAudioConfiguration) async throws {
    let longMS: Int64 = 40 * 60 * 60 * 1000
    let maximumMS = Int64.max / 1_000_000
    for milliseconds in [longMS, maximumMS] {
        var clock = MeetingAudioClock(sampleRate: 192000)
        let placed = try clock.place(milliseconds: Double(milliseconds), frames: 1024)
        try requireMeeting(placed.value > 0, "long-session sample clock rejected a representable offset")
        let origin = CMTime(value: 0, timescale: 1000)
        let host = CMTime(value: milliseconds, timescale: 1000)
        let measured = try meetingAudioMilliseconds(hostTime: host, origin: origin)
        try requireMeeting(abs(measured - Double(milliseconds)) < 0.01, "long-session host clock lost its offset")
        var identity = MeetingAudioResultIdentity(sessionID: configuration.sessionID, track: "microphone", epoch: 1)
        let range = CMTimeRange(start: CMTime(value: milliseconds - 100, timescale: 1000), duration: CMTime(value: 100, timescale: 1000))
        let final = try identity.event(text: "synthetic long-session final", range: range, finalStream: true, isFinal: true)
        try requireMeeting(final.endMS == milliseconds, "long-session final timestamp changed")
    }
    for invalid in [Double.nan, Double.infinity, -1, Double(maximumMS + 1)] {
        var clock = MeetingAudioClock(sampleRate: 48000)
        do { _ = try clock.place(milliseconds: invalid, frames: 1024); throw NSError(domain: "MeetingAudioTests", code: 6) }
        catch is MeetingAudioFailure { }
    }
    let format = AVAudioFormat(commonFormat: .pcmFormatFloat32, sampleRate: 48000, channels: 1, interleaved: false)!
    let pcm = AVAudioPCMBuffer(pcmFormat: format, frameCapacity: 1024)!
    pcm.frameLength = 1024
    pcm.floatChannelData![0].initialize(repeating: 0, count: 1024)
    let meter = MeetingAudioLevelMeter(sessionID: configuration.sessionID, epoch: 1, emit: { _ in })
    try requireMeeting(meter.event(track: "microphone", buffer: pcm, milliseconds: Double(longMS)) != nil, "long-session meter stopped at a lifetime cap")
    let session = MeetingAudioSession(configuration, factory: SyntheticMeetingFactory(), releaseSlot: {})
    defer { session.cancel() }
    session.begin()
    _ = try await collectMeeting(session.queue, until: "running")
    session.receive(MeetingAudioEvent(version: 1, sessionID: configuration.sessionID, epoch: 1, kind: "transcript", track: "microphone", segmentID: "long-final", revision: 1, startMS: longMS - 100, endMS: longMS, final: true, text: "retained after forty hours"))
    let size = session.queue.poll(capacity: 0).0
    try requireMeeting(size > 0, "native session silently rejected a long-session final")
    let batch = try JSONDecoder().decode(MeetingAudioBatch.self, from: session.queue.poll(capacity: Int(size)).1 ?? Data())
    try requireMeeting(batch.events.contains { $0.segmentID == "long-final" && $0.endMS == longMS }, "native session lost a long-session final")
    print("PASS long-session clocks, finals, native admission and duration representation guards")
}

final class SyntheticMeetingFactory: MeetingAudioFactory, @unchecked Sendable {
    let lock = NSLock()
    var drivers: [SyntheticMeetingDriver] = []
    var microphonePermission = "authorized"
    var stopDelayMilliseconds = 0
    var setupDelayMilliseconds = 0
    private var accesses = 0
    private var installs = 0
    func setupCounts() -> [Int] { lock.withLock { [accesses, installs] } }
    func requestAccess(tracks: [String]) async throws {
        let delay = lock.withLock { accesses += 1; return setupDelayMilliseconds }
        if delay > 0 { await withCheckedContinuation { continuation in DispatchQueue.global().asyncAfter(deadline: .now() + .milliseconds(delay)) { continuation.resume() } } }
    }
    func installLocale(_ locale: String) async throws { lock.withLock { installs += 1 } }
    func probe(locale: String) async -> MeetingAudioAvailability {
        lock.withLock { MeetingAudioAvailability(speechAvailable: true, microphonePermission: microphonePermission, systemPermission: true, installedLocales: ["en-US"], requestedLocaleInstalled: true) }
    }
    func makeDriver() throws -> any MeetingAudioDriver {
        lock.withLock { let driver = SyntheticMeetingDriver(stopDelayMilliseconds: stopDelayMilliseconds); drivers.append(driver); return driver }
    }
    func latest() -> SyntheticMeetingDriver? { lock.withLock { drivers.last } }
}
func collectMeetingRegistry(_ registry: MeetingAudioRegistry, handle: UInt64) async throws -> [MeetingAudioEvent] {
    for _ in 0..<200 {
        let size = registry.poll(handle, capacity: 0).0
        if size > 0, let data = registry.poll(handle, capacity: Int(size)).1 { return try JSONDecoder().decode(MeetingAudioBatch.self, from: data).events }
        try await Task.sleep(for: .milliseconds(5))
    }
    throw NSError(domain: "MeetingAudioTests", code: 5)
}
final class SyntheticMeetingDriver: MeetingAudioDriver, @unchecked Sendable {
    private let lock = NSLock()
    private var emit: (@Sendable (MeetingAudioEvent) -> Void)?
    private var configuration: MeetingAudioConfiguration?
    private var epoch: UInt64 = 0
    private var origin = CMTime.invalid
    func timelineOrigin() -> CMTime { lock.withLock { origin } }
    let stopDelayMilliseconds: Int
    init(stopDelayMilliseconds: Int) { self.stopDelayMilliseconds = stopDelayMilliseconds }
    func start(_ configuration: MeetingAudioConfiguration, epoch: UInt64, origin: CMTime, cancelled: MeetingAudioCancellation, emit: @escaping @Sendable (MeetingAudioEvent) -> Void) async throws {
        try cancelled.check()
        lock.withLock { self.configuration = configuration; self.epoch = epoch; self.origin = origin; self.emit = emit }
    }
    func seal() { }
    func stop(finalize: Bool) async throws {
        if stopDelayMilliseconds > 0 { await withCheckedContinuation { continuation in DispatchQueue.global().asyncAfter(deadline: .now() + .milliseconds(stopDelayMilliseconds)) { continuation.resume() } } }
        if finalize { send(text: "accepted final during stop", final: true) }
    }
    func send(text: String, final: Bool) {
        let context = lock.withLock { (configuration, epoch, emit) }
        if let configuration = context.0 {
            context.2?(MeetingAudioEvent(version: 1, sessionID: configuration.sessionID, epoch: context.1, kind: final ? "transcript" : "preview", track: "microphone", segmentID: "microphone-\(context.1)-1", revision: 1, startMS: 0, endMS: 100, final: final, text: text))
        }
    }
    func failDevice() {
        let context = lock.withLock { (configuration, epoch, emit) }
        if let configuration = context.0 { context.2?(MeetingAudioEvent(version: 1, sessionID: configuration.sessionID, epoch: context.1, kind: "error", error: "device_changed")) }
    }
}
func collectMeeting(_ queue: MeetingAudioEventQueue, until state: String, attempts: Int = 200) async throws -> [MeetingAudioEvent] {
    var events: [MeetingAudioEvent] = []
    for _ in 0..<attempts {
        let size = queue.poll(capacity: 0).0
        if size > 0, let data = queue.poll(capacity: Int(size)).1 {
            let batch = try JSONDecoder().decode(MeetingAudioBatch.self, from: data)
            events.append(contentsOf: batch.events)
            if events.contains(where: { $0.state == state || state == "error" && $0.kind == "error" }) { return events }
        }
        try await Task.sleep(for: .milliseconds(5))
    }
    throw NSError(domain: "MeetingAudioTests", code: 3, userInfo: [NSLocalizedDescriptionKey: "state not reached: \(state)"])
}
final class SyntheticMeetingFlag: @unchecked Sendable {
    private let lock = NSLock()
    private var flag = false
    func set() { lock.withLock { flag = true } }
    func get() -> Bool { lock.withLock { flag } }
}

final class SyntheticMeetingActivities: @unchecked Sendable {
    private let lock = NSLock()
    private var begun = 0
    private var ended = 0
    func begin() -> any MeetingAudioActivityLease {
        lock.withLock { begun += 1 }
        return SyntheticMeetingActivity { self.lock.withLock { self.ended += 1 } }
    }
    func counts() -> [Int] { lock.withLock { [begun, ended] } }
}
final class SyntheticMeetingActivity: MeetingAudioActivityLease, @unchecked Sendable {
    private let lock = NSLock()
    private var action: (@Sendable () -> Void)?
    init(_ action: @escaping @Sendable () -> Void) { self.action = action }
    func end() {
        let action = lock.withLock { let value = self.action; self.action = nil; return value }
        action?()
    }
}
func testMeetingAudioRecordingActivity(_ configuration: MeetingAudioConfiguration) async throws {
    let activities = SyntheticMeetingActivities()
    let factory = SyntheticMeetingFactory()
    factory.stopDelayMilliseconds = 100
    let session = MeetingAudioSession(configuration, factory: factory, beginActivity: { activities.begin() }, releaseSlot: {})
    session.begin()
    _ = try await collectMeeting(session.queue, until: "running")
    try requireMeeting(activities.counts() == [1, 0], "active recording did not hold its scoped activity")
    try requireMeeting(session.control("pause", expectedEpoch: 1) == 1 && activities.counts() == [1, 0], "pause released activity before actual cleanup")
    _ = try await collectMeeting(session.queue, until: "paused")
    try requireMeeting(activities.counts() == [1, 1], "pause acknowledgement retained activity")
    try requireMeeting(session.control("resume", expectedEpoch: 2) == 1, "activity-protected resume rejected")
    _ = try await collectMeeting(session.queue, until: "running")
    try requireMeeting(activities.counts() == [2, 1], "resume did not create exactly one new activity")
    try requireMeeting(session.control("stop", expectedEpoch: 3) == 1 && activities.counts() == [2, 1], "stop released activity before actual final drain")
    _ = try await collectMeeting(session.queue, until: "completed")
    try requireMeeting(activities.counts() == [2, 2], "completed stop leaked activity")
    let denied = SyntheticMeetingFactory()
    denied.microphonePermission = "denied"
    let deniedReleased = SyntheticMeetingFlag()
    let deniedSession = MeetingAudioSession(configuration, factory: denied, beginActivity: { activities.begin() }, releaseSlot: { deniedReleased.set() })
    deniedSession.begin()
    _ = try await collectMeeting(deniedSession.queue, until: "error")
    for _ in 0..<100 { if deniedReleased.get() { break }; try await Task.sleep(for: .milliseconds(5)) }
    try requireMeeting(deniedReleased.get() && activities.counts() == [3, 3], "failed startup leaked recording activity")
    let cancelledReleased = SyntheticMeetingFlag()
    let cancelled = MeetingAudioSession(configuration, factory: factory, beginActivity: { activities.begin() }, releaseSlot: { cancelledReleased.set() })
    cancelled.begin()
    _ = try await collectMeeting(cancelled.queue, until: "running")
    cancelled.cancel()
    try requireMeeting(activities.counts() == [4, 3], "cancel released activity before physical cleanup")
    for _ in 0..<100 { if cancelledReleased.get() { break }; try await Task.sleep(for: .milliseconds(5)) }
    try requireMeeting(cancelledReleased.get() && activities.counts() == [4, 4], "actual cancellation cleanup leaked activity")
    let failedReleased = SyntheticMeetingFlag()
    let failed = MeetingAudioSession(configuration, factory: factory, beginActivity: { activities.begin() }, releaseSlot: { failedReleased.set() })
    failed.begin()
    _ = try await collectMeeting(failed.queue, until: "running")
    factory.latest()!.failDevice()
    _ = try await collectMeeting(failed.queue, until: "error")
    for _ in 0..<100 { if failedReleased.get() { break }; try await Task.sleep(for: .milliseconds(5)) }
    try requireMeeting(failedReleased.get() && activities.counts() == [5, 5], "runtime failure cleanup leaked activity")
    print("PASS scoped recording activity across pause/resume, final drain, failed startup, cancel and runtime failure")
}
