import Foundation
import AVFoundation

func testMeetingAudioBuffering() async throws {
    let format = AVAudioFormat(commonFormat: .pcmFormatFloat32, sampleRate: 48000, channels: 1, interleaved: false)!
    let buffer = AVAudioPCMBuffer(pcmFormat: format, frameCapacity: 1024)!
    buffer.frameLength = 1024
    for frame in 0..<1024 { buffer.floatChannelData![0][frame] = 0 }
    let queue = MeetingAudioPCMQueue()
    // A one-second scheduling stall must not end a live system-audio session.
    // No consumer runs until all generated silence has been offered.
    for index in 0..<48 {
        try requireMeeting(queue.offer(MeetingAudioPCM(buffer, startMilliseconds: Double(index) * 1024 / 48)), "one-second consumer stall stopped live PCM capture")
    }
    for index in 48..<128 {
        try requireMeeting(queue.offer(MeetingAudioPCM(buffer, startMilliseconds: Double(index) * 1024 / 48)), "bounded PCM buffer rejected before its declared capacity")
    }
    try requireMeeting(!queue.offer(MeetingAudioPCM(buffer, startMilliseconds: 3000)), "PCM buffering became unbounded or silently replaced accepted audio")
    queue.finish()
    var count = 0
    for await packet in queue.stream {
        try requireMeeting(packet.startMilliseconds == Double(count) * 1024 / 48, "buffer pressure changed audio order or dropped accepted input")
        count += 1
    }
    try requireMeeting(count == 128 && !queue.offer(MeetingAudioPCM(buffer, startMilliseconds: 4000)), "finish lost accepted audio or reopened input")
    print("PASS bounded PCM scheduling-stall reserve and lossless accepted-input drain")
}

// Virtual-time producer/consumer schedules test minutes of sustained load with
// generated silence, without a microphone, system capture or speech assets.
func testMeetingAudioLongQueueSchedules() async throws {
    let format = AVAudioFormat(commonFormat: .pcmFormatFloat32, sampleRate: 48000, channels: 1, interleaved: false)!
    let buffer = AVAudioPCMBuffer(pcmFormat: format, frameCapacity: 1024)!
    buffer.frameLength = 1024
    buffer.floatChannelData![0].initialize(repeating: 0, count: 1024)
    let cadence = 1024.0 / 48000.0
    let queue = MeetingAudioPCMQueue()
    var input = queue.stream.makeAsyncIterator()
    var consumed = 0
    // Ten minutes, including a two-second consumer stall and eventual catchup.
    let frames = Int(600 / cadence)
    for index in 0..<frames {
        let offered = queue.admit(MeetingAudioPCM(buffer, startMilliseconds: Double(index) * cadence * 1000))
        try requireMeeting(offered == .accepted, "ten-minute schedule lost accepted input")
        if index >= 10000 && index < 10094 { continue }
        let count = index >= 10094 && index < 10188 ? 2 : 1
        for _ in 0..<count {
            let packet = await input.next()
            try requireMeeting(packet?.startMilliseconds == Double(consumed) * cadence * 1000, "scheduler catchup reordered or replaced audio")
            consumed += 1
        }
    }
    queue.finish()
    let ended = await input.next()
    try requireMeeting(ended == nil && consumed == frames, "ten-minute schedule left accepted input behind")
    try requireMeeting(queue.admit(MeetingAudioPCM(buffer, startMilliseconds: 600001)) == .terminated, "closed capture queue was mislabeled full")
    // A consumer just 0.63% slower than capture eventually exhausts any fixed
    // queue. 128 packets reaches its bound near seven minutes at this cadence.
    let drift = MeetingAudioPCMQueue()
    var slow = drift.stream.makeAsyncIterator()
    var accepted = 0
    var read = 0
    var overflowAt = 0
    for index in 0..<frames {
        let result = drift.admit(MeetingAudioPCM(buffer, startMilliseconds: Double(index) * cadence * 1000))
        if result != .accepted { overflowAt = index; try requireMeeting(result.failureCode(stage: .capture) == "audio_capture_overflow", "capture overload lost its exact stage"); break }
        accepted += 1
        if index % 159 == 0 { continue }
        let packet = await slow.next()
        try requireMeeting(packet?.startMilliseconds == Double(read) * cadence * 1000, "sustained deficit replaced queued audio")
        read += 1
    }
    try requireMeeting(overflowAt > 20000 && overflowAt < 21000, "sustained-rate-deficit test did not reach expected bounded pressure")
    drift.finish()
    while let packet = await slow.next() {
        try requireMeeting(packet.startMilliseconds == Double(read) * cadence * 1000, "overload changed retained FIFO")
        read += 1
    }
    try requireMeeting(read == accepted, "overflow discarded already admitted PCM")
    let (analyzer, continuation) = AsyncStream<Int>.makeStream(bufferingPolicy: .bufferingOldest(MeetingAudioPCMQueue.packetCapacity))
    for index in 0..<MeetingAudioPCMQueue.packetCapacity { try requireMeeting(MeetingAudioQueueAdmission(continuation.yield(index)) == .accepted, "analyzer bound rejected early") }
    try requireMeeting(MeetingAudioQueueAdmission(continuation.yield(129)).failureCode(stage: .analyzer) == "audio_analyzer_overflow", "analyzer overload mislabeled capture pressure")
    continuation.finish()
    try requireMeeting(MeetingAudioQueueAdmission(continuation.yield(130)).failureCode(stage: .analyzer) == "audio_analyzer_stopped", "analyzer termination mislabeled overflow")
    var count = 0
    for await value in analyzer { try requireMeeting(value == count, "analyzer pressure replaced accepted input"); count += 1 }
    try requireMeeting(count == MeetingAudioPCMQueue.packetCapacity, "analyzer termination lost admitted input")
    print("PASS ten-minute virtual PCM scheduling/catchup and 0.63% sustained deficit with stage-specific pressure/termination")
}

func testMeetingAudioLevels(_ configuration: MeetingAudioConfiguration) async throws {
    let mono = AVAudioFormat(commonFormat: .pcmFormatFloat32, sampleRate: 48000, channels: 1, interleaved: false)!
    let pcm = AVAudioPCMBuffer(pcmFormat: mono, frameCapacity: 4)!
    pcm.frameLength = 4
    for index in 0..<4 { pcm.floatChannelData![0][index] = 0.5 }
    try requireMeeting(abs((meetingAudioRMS(pcm) ?? -1) - 0.5) < 0.000001, "actual PCM amplitude did not produce RMS")
    for index in 0..<4 { pcm.floatChannelData![0][index] = 0 }
    try requireMeeting(meetingAudioRMS(pcm) == 0, "silence did not produce zero")
    pcm.floatChannelData![0][0] = .nan
    try requireMeeting(meetingAudioRMS(pcm) == nil, "nonfinite PCM admitted a meter")
    pcm.floatChannelData![0][0] = 2
    try requireMeeting(abs((meetingAudioRMS(pcm) ?? -1) - 0.5) < 0.000001, "clipped PCM normalization exceeded one")
    let stereo = AVAudioFormat(commonFormat: .pcmFormatInt16, sampleRate: 48000, channels: 2, interleaved: true)!
    let integerPCM = AVAudioPCMBuffer(pcmFormat: stereo, frameCapacity: 4)!
    integerPCM.frameLength = 4
    let values = integerPCM.int16ChannelData![0]
    for index in 0..<8 { values[index] = index % 2 == 0 ? 16384 : -16384 }
    try requireMeeting(abs((meetingAudioRMS(integerPCM) ?? -1) - 0.5) < 0.000001, "interleaved integer PCM RMS was wrong")
    let planar = AVAudioFormat(commonFormat: .pcmFormatInt32, sampleRate: 48000, channels: 2, interleaved: false)!
    let planarPCM = AVAudioPCMBuffer(pcmFormat: planar, frameCapacity: 4)!
    planarPCM.frameLength = 4
    for index in 0..<4 { planarPCM.int32ChannelData![0][index] = 1073741824; planarPCM.int32ChannelData![1][index] = -1073741824 }
    try requireMeeting(abs((meetingAudioRMS(planarPCM) ?? -1) - 0.5) < 0.000001, "planar integer PCM RMS was wrong")
    let doubleFormat = AVAudioFormat(commonFormat: .pcmFormatFloat64, sampleRate: 48000, channels: 1, interleaved: false)!
    let doublePCM = AVAudioPCMBuffer(pcmFormat: doubleFormat, frameCapacity: 4)!
    doublePCM.frameLength = 4
    let doubleData = UnsafeMutableAudioBufferListPointer(doublePCM.mutableAudioBufferList)[0].mData!.assumingMemoryBound(to: Double.self)
    for index in 0..<4 { doubleData[index] = index % 2 == 0 ? 0.75 : -0.75 }
    try requireMeeting(abs((meetingAudioRMS(doublePCM) ?? -1) - 0.75) < 0.000001, "float64 PCM RMS was wrong")
    doublePCM.frameLength = 0
    try requireMeeting(meetingAudioRMS(doublePCM) == nil, "empty PCM emitted fake activity")
    let oversized = AVAudioPCMBuffer(pcmFormat: mono, frameCapacity: 8193)!
    oversized.frameLength = 8193
    try requireMeeting(meetingAudioRMS(oversized) == nil, "meter exceeded PCM work bound")
    let meter = MeetingAudioLevelMeter(sessionID: configuration.sessionID, epoch: 9, emit: { _ in })
    for index in 0..<4 { pcm.floatChannelData![0][index] = 0.5 }
    try requireMeeting(meter.event(track: "microphone", buffer: pcm, milliseconds: 0)?.level == 0.5, "first real level not emitted")
    try requireMeeting(meter.event(track: "microphone", buffer: pcm, milliseconds: 99) == nil, "meter exceeded ten hertz")
    try requireMeeting(meter.event(track: "microphone", buffer: pcm, milliseconds: 100)?.epoch == 9, "meter lost current epoch")
    try requireMeeting(meter.event(track: "system", buffer: pcm, milliseconds: 1) != nil, "one track suppressed the other meter")
    try requireMeeting(meter.event(track: "unknown", buffer: pcm, milliseconds: 200) == nil, "unknown meter track admitted")
    let queue = MeetingAudioEventQueue()
    for index in 0..<10000 {
        let level = MeetingAudioEvent(version: 1, sessionID: configuration.sessionID, epoch: 1, kind: "level", track: index % 2 == 0 ? "microphone" : "system", level: Double(index % 100) / 100)
        try requireMeeting(queue.append(level), "drop-eligible meter failed queue")
    }
    for index in 0..<128 {
        try requireMeeting(queue.append(MeetingAudioEvent(version: 1, sessionID: configuration.sessionID, epoch: 1, kind: "transcript", track: "microphone", segmentID: "level-final-\(index)", revision: 1, startMS: Int64(index), endMS: Int64(index+1), final: true, text: "synthetic final")), "meter consumed final transcript capacity")
    }
    var finals = 0
    var levels = 0
    for _ in 0..<10 {
        let size = queue.poll(capacity: 0).0
        if size == 0 { break }
        let data = queue.poll(capacity: Int(size)).1!
        let batch = try JSONDecoder().decode(MeetingAudioBatch.self, from: data)
        for event in batch.events {
            if event.kind == "transcript" { finals += 1 }
            if event.kind == "level" { levels += 1; try requireMeeting(finals == 128, "level displaced a queued final") }
        }
    }
    try requireMeeting(finals == 128 && levels == 2, "meter updates did not coalesce separately from finals")
    let staged = MeetingAudioEventQueue()
    let level = MeetingAudioEvent(version: 1, sessionID: configuration.sessionID, epoch: 1, kind: "level", track: "microphone", level: 0.3)
    try requireMeeting(staged.append(level), "meter not queued")
    let stagedSize = staged.poll(capacity: 0).0
    for index in 0..<128 {
        try requireMeeting(staged.append(MeetingAudioEvent(version: 1, sessionID: configuration.sessionID, epoch: 1, kind: "transcript", track: "microphone", segmentID: "staged-level-\(index)", revision: 1, startMS: 0, endMS: 1, final: true, text: "x")), "staged meter consumed reliable final capacity")
    }
    try requireMeeting(staged.poll(capacity: Int(stagedSize)).0 == stagedSize, "meter size handshake changed while finals queued")
    let factory = SyntheticMeetingFactory()
    let session = MeetingAudioSession(configuration, factory: factory, releaseSlot: {})
    session.begin()
    _ = try await collectMeeting(session.queue, until: "running")
    let streamedMeter = MeetingAudioLevelMeter(sessionID: configuration.sessionID, epoch: 1, emit: { session.receive($0) })
    for (index, amplitude) in [Float(0), Float(0.005), Float(0.5), Float(0)].enumerated() {
        for frame in 0..<4 { pcm.floatChannelData![0][frame] = amplitude }
        streamedMeter.offer(track: "microphone", buffer: pcm, milliseconds: Double(index * 100))
        let size = session.queue.poll(capacity: 0).0
        try requireMeeting(size > 0, "generated PCM did not reach the active native event stream")
        let data = session.queue.poll(capacity: Int(size)).1 ?? Data()
        let batch = try JSONDecoder().decode(MeetingAudioBatch.self, from: data)
        try requireMeeting(batch.events.count == 1 && batch.events[0].kind == "level" && batch.events[0].track == "microphone" && batch.events[0].epoch == 1 && abs((batch.events[0].level ?? -1) - Double(amplitude)) < 0.000001, "changing PCM RMS did not survive native session admission and JSON polling")
    }
    session.receive(level)
    var malformed = level
    malformed.text = "meter must not carry words"
    session.receive(malformed)
    try requireMeeting(session.control("pause", expectedEpoch: 1) == 1, "synthetic meter pause rejected")
    session.receive(level)
    let paused = try await collectMeeting(session.queue, until: "paused")
    try requireMeeting(!paused.contains { $0.kind == "level" }, "pause retained pending/late RMS")
    try requireMeeting(session.control("stop", expectedEpoch: 2) == 1, "synthetic meter stop rejected")
    _ = try await collectMeeting(session.queue, until: "completed")
    session.receive(level)
    try requireMeeting(session.queue.poll(capacity: 0).0 == 0, "stopped session accepted late RMS")
    session.cancel()
    print("PASS actual PCM RMS, ten-hertz per-track meter, reliable-queue isolation and pause/stop cleanup")
}
