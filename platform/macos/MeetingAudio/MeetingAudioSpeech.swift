import Foundation
@preconcurrency import AVFoundation
import CoreMedia
import Speech

final class MeetingAudioPCM: @unchecked Sendable {
    let buffer: AVAudioPCMBuffer
    let startMilliseconds: Double
    init(_ buffer: AVAudioPCMBuffer, startMilliseconds: Double) { self.buffer = buffer; self.startMilliseconds = startMilliseconds }
}

struct MeetingAudioClock {
    let sampleRate: Double
    private(set) var next: Int64 = 0
    mutating func place(milliseconds: Double, frames: Int) throws -> CMTime {
        guard meetingAudioTimestamp(milliseconds) != nil, frames > 0, frames <= 65536,
              sampleRate >= 8000, sampleRate <= 192000, sampleRate.rounded() == sampleRate else { throw MeetingAudioFailure(code: "audio_format") }
        guard let proposed = Int64(exactly: (milliseconds * sampleRate / 1000).rounded()) else { throw MeetingAudioFailure(code: "audio_clock") }
        let (threshold, thresholdOverflow) = next.addingReportingOverflow(Int64(sampleRate / 20))
        let start = thresholdOverflow || proposed < threshold ? next : proposed
        let (end, overflow) = start.addingReportingOverflow(Int64(frames))
        guard !overflow else { throw MeetingAudioFailure(code: "audio_clock") }
        next = end
        return CMTime(value: start, timescale: CMTimeScale(sampleRate))
    }
}

enum MeetingAudioQueueStage { case capture, analyzer }

enum MeetingAudioQueueAdmission {
    case accepted, invalid, overflow, terminated
    init<Element>(_ result: AsyncStream<Element>.Continuation.YieldResult) {
        switch result {
        case .enqueued: self = .accepted
        case .dropped: self = .overflow
        case .terminated: self = .terminated
        @unknown default: self = .terminated
        }
    }
    func failureCode(stage: MeetingAudioQueueStage) -> String? {
        switch self {
        case .accepted: return nil
        case .invalid: return "audio_format"
        case .overflow: return stage == .capture ? "audio_capture_overflow" : "audio_analyzer_overflow"
        case .terminated: return stage == .capture ? "audio_capture_stopped" : "audio_analyzer_stopped"
        }
    }
}

final class MeetingAudioPCMQueue: Sendable {
    // At the capture path's 48 kHz/1024-frame cadence this holds about 2.7
    // seconds. Eight packets left only 171 ms for a consumer scheduling stall,
    // which ended the whole recording on otherwise valid live audio.
    static let packetCapacity = 128
    let stream: AsyncStream<MeetingAudioPCM>
    let continuation: AsyncStream<MeetingAudioPCM>.Continuation
    init() { (stream, continuation) = AsyncStream.makeStream(bufferingPolicy: .bufferingOldest(Self.packetCapacity)) }
    func offer(_ packet: MeetingAudioPCM) -> Bool { admit(packet) == .accepted }
    func admit(_ packet: MeetingAudioPCM) -> MeetingAudioQueueAdmission {
        let buffer = packet.buffer
        guard packet.startMilliseconds.isFinite, packet.startMilliseconds >= 0,
              buffer.frameLength > 0, buffer.frameLength <= 8192, buffer.format.sampleRate >= 8000, buffer.format.sampleRate <= 192000,
              buffer.format.channelCount > 0, buffer.format.channelCount <= 8 else { return .invalid }
        return MeetingAudioQueueAdmission(continuation.yield(packet))
    }
    func finish() { continuation.finish() }
}

// The volatile stream is only a replaceable preview. Apple does not guarantee
// that every volatile result is reissued as final; durable events therefore come
// exclusively from a separate final-only module, never from preview transitions.
struct MeetingAudioResultIdentity {
    let sessionID: String
    let track: String
    let epoch: UInt64
    private var nextFinal: UInt64 = 1
    private var previewRevision: UInt64 = 0

    init(sessionID: String, track: String, epoch: UInt64) { self.sessionID = sessionID; self.track = track; self.epoch = epoch }
    mutating func event(text: String, range: CMTimeRange, finalStream: Bool, isFinal: Bool) throws -> MeetingAudioEvent {
        let start = CMTimeGetSeconds(range.start) * 1000
        let end = CMTimeGetSeconds(CMTimeRangeGetEnd(range)) * 1000
        guard let startMS = meetingAudioTimestamp(start), let endMS = meetingAudioTimestamp(end), end >= start,
              !text.isEmpty, text.utf8.count <= 16384 else { throw MeetingAudioFailure(code: "transcript_limit") }
        if finalStream && !isFinal { throw MeetingAudioFailure(code: "transcription_failed") }
        let identity: String
        let revision: UInt64
        if finalStream {
            guard nextFinal < UInt64.max else { throw MeetingAudioFailure(code: "transcript_limit") }
            identity = "\(track)-\(epoch)-final-\(nextFinal)"; nextFinal += 1; revision = 1
        } else {
            guard previewRevision < UInt64.max else { throw MeetingAudioFailure(code: "transcript_limit") }
            identity = "\(track)-\(epoch)-preview"; previewRevision += 1; revision = previewRevision
        }
        // Preserve the producer's boundaries (rounded only to the ABI's ms
        // resolution). Preview boundaries may move in either direction.
        return MeetingAudioEvent(version: 1, sessionID: sessionID, epoch: epoch, kind: finalStream ? "transcript" : "preview", track: track, segmentID: identity, revision: revision, startMS: startMS, endMS: endMS, final: finalStream, text: text)
    }
}

@available(macOS 26.0, *)
final class MeetingAudioSpeechTrack: @unchecked Sendable {
    private let track: String
    private let configuration: MeetingAudioConfiguration
    private let epoch: UInt64
    private let emit: @Sendable (MeetingAudioEvent) -> Void
    private let lock = NSLock()
    private let packets = MeetingAudioPCMQueue()
    private var analyzer: SpeechAnalyzer?
    private var inputs: AsyncStream<AnalyzerInput>.Continuation?
    private var feeding: Task<Void, Never>?
    private var results: [Task<Void, Never>] = []
    private var accepting = true
    private var outputEnabled = true
    private var identity: MeetingAudioResultIdentity

    init(track: String, configuration: MeetingAudioConfiguration, epoch: UInt64, emit: @escaping @Sendable (MeetingAudioEvent) -> Void) {
        self.track = track; self.configuration = configuration; self.epoch = epoch; self.emit = emit
        identity = MeetingAudioResultIdentity(sessionID: configuration.sessionID, track: track, epoch: epoch)
    }
    func start(cancelled: MeetingAudioCancellation) async throws {
        let locale = Locale(identifier: configuration.locale)
        let preview = SpeechTranscriber(locale: locale, transcriptionOptions: [], reportingOptions: [.volatileResults], attributeOptions: [.audioTimeRange])
        let finals = SpeechTranscriber(locale: locale, transcriptionOptions: [], reportingOptions: [], attributeOptions: [.audioTimeRange])
        let modules = [preview, finals]
        guard await AssetInventory.status(forModules: modules) == .installed else { throw MeetingAudioFailure(code: "assets_missing") }
        try cancelled.check()
        guard let format = await SpeechAnalyzer.bestAvailableAudioFormat(compatibleWith: modules), format.sampleRate >= 8000,
              format.sampleRate <= 192000 else { throw MeetingAudioFailure(code: "audio_format") }
        try cancelled.check()
        let analyzer = SpeechAnalyzer(modules: modules, options: SpeechAnalyzer.Options(priority: .userInitiated, modelRetention: .whileInUse))
        let (sequence, continuation) = AsyncStream<AnalyzerInput>.makeStream(bufferingPolicy: .bufferingOldest(MeetingAudioPCMQueue.packetCapacity))
        lock.withLock { self.analyzer = analyzer; inputs = continuation }
        // Exactly two result consumers and one feeding task per requested
        // track. Both modules share an analyzer/input, not a proven compute cost.
        let results = [(preview, false), (finals, true)].map { module, finalStream in Task.detached(priority: .userInitiated) { [self] in
            do {
                for try await result in module.results {
                    if Task.isCancelled { break }
                    let text = String(result.text.characters).trimmingCharacters(in: .whitespacesAndNewlines)
                    if text.isEmpty { continue }
                    let event: MeetingAudioEvent? = try lock.withLock {
                        guard outputEnabled else { return nil }
                        return try identity.event(text: text, range: result.range, finalStream: finalStream, isFinal: result.isFinal)
                    }
                    if let event { emit(event) }
                }
            } catch { if lock.withLock({ outputEnabled }) { failure((error as? MeetingAudioFailure)?.code ?? "transcription_failed") } }
        } }
        lock.withLock { self.results = results }
        do { try await analyzer.prepareToAnalyze(in: format); try cancelled.check(); try await analyzer.start(inputSequence: sequence) }
        catch { await analyzer.cancelAndFinishNow(); for result in results { result.cancel(); await result.value }; throw MeetingAudioFailure(code: "transcription_failed") }
        let feeding = Task.detached(priority: .userInitiated) { [self] in
            var converter: AVAudioConverter?
            var previous: AVAudioFormat?
            var clock = MeetingAudioClock(sampleRate: format.sampleRate)
            for await packet in packets.stream {
                if Task.isCancelled { break }
                do {
                    if previous == nil { previous = packet.buffer.format; converter = AVAudioConverter(from: packet.buffer.format, to: format) }
                    guard previous == packet.buffer.format, let converter else { throw MeetingAudioFailure(code: "device_changed") }
                    let capacity = AVAudioFrameCount(ceil(Double(packet.buffer.frameLength) * format.sampleRate / packet.buffer.format.sampleRate) + 64)
                    guard capacity > 0, capacity <= 65536, let converted = AVAudioPCMBuffer(pcmFormat: format, frameCapacity: capacity) else { throw MeetingAudioFailure(code: "audio_format") }
                    let source = MeetingAudioConverterInput(packet.buffer)
                    var error: NSError?
                    let status = converter.convert(to: converted, error: &error) { _, status in
                        if let buffer = source.take() { status.pointee = .haveData; return buffer }
                        status.pointee = .noDataNow; return nil
                    }
                    guard error == nil, status != .error else { throw MeetingAudioFailure(code: "audio_conversion") }
                    if converted.frameLength == 0 { continue }
                    let time = try clock.place(milliseconds: packet.startMilliseconds, frames: Int(converted.frameLength))
                    let admission = MeetingAudioQueueAdmission(continuation.yield(AnalyzerInput(buffer: converted, bufferStartTime: time)))
                    if let code = admission.failureCode(stage: .analyzer) { throw MeetingAudioFailure(code: code) }
                } catch { failure((error as? MeetingAudioFailure)?.code ?? "audio_conversion"); break }
            }
            continuation.finish()
        }
        lock.withLock { self.feeding = feeding }
    }
    func offer(_ packet: MeetingAudioPCM) {
        let code = lock.withLock { accepting ? packets.admit(packet).failureCode(stage: .capture) : nil }
        if let code { failure(code) }
    }
    func seal() { lock.withLock { accepting = false }; packets.finish() }
    func stop(finalize: Bool) async throws {
        seal()
        let context = lock.withLock { () -> (Task<Void, Never>?, [Task<Void, Never>], SpeechAnalyzer?) in
            if !finalize { outputEnabled = false }
            return (feeding, results, analyzer)
        }
        if !finalize { context.0?.cancel() }
        await context.0?.value
        var failed = false
        if finalize {
            do { try await context.2?.finalizeAndFinishThroughEndOfInput() }
            catch { failed = true; await context.2?.cancelAndFinishNow() }
        } else { await context.2?.cancelAndFinishNow() }
        for result in context.1 { await result.value }
        lock.withLock { outputEnabled = false; analyzer = nil; inputs = nil; feeding = nil; results = [] }
        if failed { throw MeetingAudioFailure(code: "transcription_failed") }
    }
    private func failure(_ code: String) {
        let notify = lock.withLock { () -> Bool in if !outputEnabled { return false }; outputEnabled = false; accepting = false; return true }
        packets.finish()
        if notify { emit(MeetingAudioEvent(version: 1, sessionID: configuration.sessionID, epoch: epoch, kind: "error", error: code, track: track)) }
    }
}
private final class MeetingAudioConverterInput: @unchecked Sendable {
    private var buffer: AVAudioPCMBuffer?
    init(_ buffer: AVAudioPCMBuffer) { self.buffer = buffer }
    func take() -> AVAudioPCMBuffer? { defer { buffer = nil }; return buffer }
}

func meetingAudioMilliseconds(hostTime: CMTime, origin: CMTime) throws -> Double {
    guard hostTime.isNumeric, origin.isNumeric else { throw MeetingAudioFailure(code: "audio_clock") }
    let milliseconds = CMTimeGetSeconds(CMTimeSubtract(hostTime, origin)) * 1000
    guard meetingAudioTimestamp(milliseconds) != nil else { throw MeetingAudioFailure(code: "audio_clock") }
    return milliseconds
}
