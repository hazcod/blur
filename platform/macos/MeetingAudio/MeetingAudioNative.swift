import Foundation
@preconcurrency import AVFoundation
@preconcurrency import ScreenCaptureKit
import CoreGraphics
import CoreMedia
import Speech

struct NativeMeetingAudioFactory: MeetingAudioFactory {
    func probe(locale: String) async -> MeetingAudioAvailability {
        let microphone: String
        switch AVCaptureDevice.authorizationStatus(for: .audio) {
        case .authorized: microphone = "authorized"
        case .denied: microphone = "denied"
        case .restricted: microphone = "restricted"
        case .notDetermined: microphone = "not_determined"
        @unknown default: microphone = "unknown"
        }
        let system = CGPreflightScreenCaptureAccess()
        guard #available(macOS 26.0, *) else { return MeetingAudioAvailability(speechAvailable: false, microphonePermission: microphone, systemPermission: system, installedLocales: [], requestedLocaleInstalled: false) }
        let installed = await SpeechTranscriber.installedLocales.map { $0.identifier(.bcp47) }.sorted()
        let wanted = Locale(identifier: locale).identifier(.bcp47)
        return MeetingAudioAvailability(speechAvailable: SpeechTranscriber.isAvailable, microphonePermission: microphone, systemPermission: system, installedLocales: Array(installed.prefix(256)), requestedLocaleInstalled: installed.contains { $0.caseInsensitiveCompare(wanted) == .orderedSame })
    }
    func makeDriver() throws -> any MeetingAudioDriver {
        guard #available(macOS 26.0, *) else { throw MeetingAudioFailure(code: "speech_unavailable") }
        return NativeMeetingAudioDriver()
    }
}

@available(macOS 26.0, *)
final class NativeMeetingAudioDriver: MeetingAudioDriver, @unchecked Sendable {
    private let lock = NSLock()
    private var tracks: [String: MeetingAudioSpeechTrack] = [:]
    private var source: MeetingAudioDeviceSource?
    private var sealed = false

    func start(_ configuration: MeetingAudioConfiguration, epoch: UInt64, origin: CMTime, cancelled: MeetingAudioCancellation, emit: @escaping @Sendable (MeetingAudioEvent) -> Void) async throws {
        do {
            for name in configuration.tracks {
                try cancelled.check()
                let track = MeetingAudioSpeechTrack(track: name, configuration: configuration, epoch: epoch, emit: emit)
                lock.withLock { tracks[name] = track }
                try await track.start(cancelled: cancelled)
            }
            try cancelled.check()
            guard !lock.withLock({ sealed }) else { throw CancellationError() }
            let tracks = lock.withLock { self.tracks }
            let meter = MeetingAudioLevelMeter(sessionID: configuration.sessionID, epoch: epoch, emit: emit)
            let source = MeetingAudioDeviceSource(tracks: configuration.tracks, origin: origin, packet: { name, packet in
                meter.offer(track: name, buffer: packet.buffer, milliseconds: packet.startMilliseconds)
                tracks[name]?.offer(packet)
            }, failure: { code in
                emit(MeetingAudioEvent(version: 1, sessionID: configuration.sessionID, epoch: epoch, kind: "error", error: code))
            })
            lock.withLock { self.source = source }
            try cancelled.check()
            try await source.start(cancelled: cancelled)
            try cancelled.check()
        } catch {
            seal()
            try await stop(finalize: false)
            throw error
        }
    }
    func seal() { let source = lock.withLock { () -> MeetingAudioDeviceSource? in sealed = true; return self.source }; source?.seal() }
    func stop(finalize: Bool) async throws {
        let context = lock.withLock { (source, Array(tracks.values)) }
        context.0?.seal()
        var failure: String?
        do { try await context.0?.stop() } catch { failure = "device_stop_failed" }
        for track in context.1 {
            do { try await track.stop(finalize: finalize && failure == nil) }
            catch { if failure == nil { failure = "transcription_failed" } }
        }
        if failure == nil { lock.withLock { source = nil; tracks.removeAll() } }
        if let failure { throw MeetingAudioFailure(code: failure) }
    }
}

@available(macOS 26.0, *)
final class MeetingAudioDeviceSource: NSObject, SCStreamOutput, SCStreamDelegate, @unchecked Sendable {
    private let lock = NSLock()
    private let tracks: [String]
    private let origin: CMTime
    private let packet: @Sendable (String, MeetingAudioPCM) -> Void
    private let failure: @Sendable (String) -> Void
    private var stream: SCStream?
    private var engine: AVAudioEngine?
    private var observer: NSObjectProtocol?
    private var accepting = true

    init(tracks: [String], origin: CMTime, packet: @escaping @Sendable (String, MeetingAudioPCM) -> Void, failure: @escaping @Sendable (String) -> Void) {
        self.tracks = tracks; self.origin = origin; self.packet = packet; self.failure = failure
    }
    func start(cancelled: MeetingAudioCancellation) async throws {
        try cancelled.check()
        if tracks == ["microphone"] {
            guard AVCaptureDevice.authorizationStatus(for: .audio) == .authorized else { throw MeetingAudioFailure(code: "microphone_permission") }
            try startMicrophone(cancelled: cancelled)
            return
        }
        guard CGPreflightScreenCaptureAccess() else { throw MeetingAudioFailure(code: "system_permission") }
        if tracks.contains("microphone"), AVCaptureDevice.authorizationStatus(for: .audio) != .authorized { throw MeetingAudioFailure(code: "microphone_permission") }
        let content = try await SCShareableContent.excludingDesktopWindows(true, onScreenWindowsOnly: true)
        try cancelled.check()
        guard lock.withLock({ accepting }), let display = content.displays.first else { throw MeetingAudioFailure(code: "device_unavailable") }
        let filter = SCContentFilter(display: display, excludingApplications: [], exceptingWindows: [])
        let configuration = SCStreamConfiguration()
        configuration.capturesAudio = tracks.contains("system")
        configuration.captureMicrophone = tracks.contains("microphone")
        if configuration.captureMicrophone {
            guard let microphone = AVCaptureDevice.default(for: .audio) else { throw MeetingAudioFailure(code: "device_unavailable") }
            configuration.microphoneCaptureDeviceID = microphone.uniqueID
        }
        configuration.excludesCurrentProcessAudio = true
        configuration.channelCount = 1
        configuration.sampleRate = 48000
        configuration.width = 2
        configuration.height = 2
        configuration.showsCursor = false
        configuration.queueDepth = 3
        configuration.minimumFrameInterval = CMTime(value: 1, timescale: 1)
        let stream = SCStream(filter: filter, configuration: configuration, delegate: self)
        // Register audio consumers only. No screen output, recording output,
        // pixel access, movie writer or file URL exists in the meeting bridge.
        if tracks.contains("system") { try stream.addStreamOutput(self, type: .audio, sampleHandlerQueue: DispatchQueue(label: "com.hazcod.blur.meeting.system", qos: .userInitiated)) }
        if tracks.contains("microphone") { try stream.addStreamOutput(self, type: .microphone, sampleHandlerQueue: DispatchQueue(label: "com.hazcod.blur.meeting.microphone", qos: .userInitiated)) }
        lock.withLock { self.stream = stream }
        try cancelled.check()
        try await stream.startCapture()
        try cancelled.check()
    }
    private func startMicrophone(cancelled: MeetingAudioCancellation) throws {
        let engine = AVAudioEngine()
        let input = engine.inputNode
        let format = input.inputFormat(forBus: 0)
        guard format.sampleRate >= 8000, format.sampleRate <= 192000, format.channelCount > 0, format.channelCount <= 8 else { throw MeetingAudioFailure(code: "audio_format") }
        // No voice processing, routing changes or speaker playback. This must
        // not alter the microphone signal used by a concurrent call app.
        input.installTap(onBus: 0, bufferSize: 1024, format: nil) { [weak self] buffer, when in
            guard let self, self.lock.withLock({ self.accepting }) else { return }
            guard when.isHostTimeValid, let copy = self.copyPCM(buffer) else { self.report("audio_format"); return }
            let time = CMClockMakeHostTimeFromSystemUnits(when.hostTime)
            self.deliver("microphone", buffer: copy, hostTime: time)
        }
        lock.withLock { self.engine = engine }
        let observer = NotificationCenter.default.addObserver(forName: .AVAudioEngineConfigurationChange, object: engine, queue: nil) { [weak self] _ in self?.report("device_changed") }
        lock.withLock { self.observer = observer }
        try cancelled.check()
        guard lock.withLock({ accepting }) else { throw CancellationError() }
        engine.prepare()
        try engine.start()
    }
    func seal() { lock.withLock { accepting = false } }
    func stop() async throws {
        seal()
        let context = lock.withLock { () -> (SCStream?, AVAudioEngine?, NSObjectProtocol?) in
            let context = (stream, engine, observer)
            engine = nil; observer = nil
            return context
        }
        if let observer = context.2 { NotificationCenter.default.removeObserver(observer) }
        if let engine = context.1 { engine.inputNode.removeTap(onBus: 0); engine.stop() }
        if let stream = context.0 { try await stream.stopCapture(); lock.withLock { self.stream = nil } }
    }
    func stream(_ stream: SCStream, didOutputSampleBuffer sampleBuffer: CMSampleBuffer, of outputType: SCStreamOutputType) {
        guard lock.withLock({ accepting }), sampleBuffer.isValid else { return }
        let name: String
        switch outputType { case .audio: name = "system"; case .microphone: name = "microphone"; default: return }
        guard tracks.contains(name), let description = sampleBuffer.formatDescription,
              let pointer = CMAudioFormatDescriptionGetStreamBasicDescription(description) else { report("audio_format"); return }
        var value = pointer.pointee
        guard value.mFormatID == kAudioFormatLinearPCM else { report("audio_format"); return }
        let count = CMSampleBufferGetNumSamples(sampleBuffer)
        guard count > 0 else { return }
        guard count <= 8192, let format = AVAudioFormat(streamDescription: &value), format.sampleRate >= 8000, format.sampleRate <= 192000,
              format.channelCount > 0, format.channelCount <= 8,
              let buffer = AVAudioPCMBuffer(pcmFormat: format, frameCapacity: AVAudioFrameCount(count)) else { report("audio_format"); return }
        buffer.frameLength = AVAudioFrameCount(count)
        guard CMSampleBufferCopyPCMDataIntoAudioBufferList(sampleBuffer, at: 0, frameCount: Int32(count), into: buffer.mutableAudioBufferList) == noErr,
              let clock = stream.synchronizationClock else { report("audio_clock"); return }
        let time = CMSyncConvertTime(sampleBuffer.presentationTimeStamp, from: clock, to: CMClockGetHostTimeClock())
        deliver(name, buffer: buffer, hostTime: time)
    }
    func stream(_ stream: SCStream, didStopWithError error: any Error) { report("device_failed") }
    private func deliver(_ track: String, buffer: AVAudioPCMBuffer, hostTime: CMTime) {
        guard let milliseconds = try? meetingAudioMilliseconds(hostTime: hostTime, origin: origin) else { report("audio_clock"); return }
        if lock.withLock({ accepting }) { packet(track, MeetingAudioPCM(buffer, startMilliseconds: milliseconds)) }
    }
    private func report(_ code: String) {
        let notify = lock.withLock { () -> Bool in guard accepting else { return false }; accepting = false; return true }
        if notify { failure(code) }
    }
    private func copyPCM(_ source: AVAudioPCMBuffer) -> AVAudioPCMBuffer? {
        guard source.frameLength > 0, source.frameLength <= 8192, source.format.channelCount <= 8,
              let result = AVAudioPCMBuffer(pcmFormat: source.format, frameCapacity: source.frameLength) else { return nil }
        result.frameLength = source.frameLength
        let inputs = UnsafeMutableAudioBufferListPointer(source.mutableAudioBufferList)
        let outputs = UnsafeMutableAudioBufferListPointer(result.mutableAudioBufferList)
        guard inputs.count == outputs.count else { return nil }
        for index in inputs.indices {
            guard inputs[index].mDataByteSize <= outputs[index].mDataByteSize, let input = inputs[index].mData, let output = outputs[index].mData else { return nil }
            output.copyMemory(from: input, byteCount: Int(inputs[index].mDataByteSize))
        }
        return result
    }
}
