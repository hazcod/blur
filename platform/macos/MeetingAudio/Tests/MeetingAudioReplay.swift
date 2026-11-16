// Explicit opt-in, generated-file replay. This entry point never creates a
// capture device, requests permission or installs assets. The Python runner
// supplies its fixed, locally generated TTS file.
import Foundation
import AVFoundation
import CoreMedia
import Speech

final class MeetingAudioReplayCounts: @unchecked Sendable {
    private let lock = NSLock()
    private var finals = 0
    private var previews = 0
    private var error: String?
    func receive(_ event: MeetingAudioEvent) {
        lock.withLock {
            if event.kind == "transcript" { finals += 1 }
            if event.kind == "preview" { previews += 1 }
            if event.kind == "error" { error = event.error }
        }
    }
    func snapshot() -> (Int, Int, String?) { lock.withLock { (finals, previews, error) } }
}
func replayLog(_ text: String) { FileHandle.standardOutput.write(Data((text + "\n").utf8)) }

@main struct MeetingAudioReplay {
    static func main() async throws {
        guard CommandLine.arguments.count == 3, let duration = Double(CommandLine.arguments[2]), duration >= 1, duration <= 600 else { throw MeetingAudioFailure(code: "invalid_request") }
        guard #available(macOS 26.0, *) else { replayLog("SKIP speech_unavailable"); return }
        let file = try AVAudioFile(forReading: URL(fileURLWithPath: CommandLine.arguments[1]))
        let format = file.processingFormat
        guard format.sampleRate == 48000, format.channelCount == 1, file.length > 0, file.length <= 48000 * 120 else { throw MeetingAudioFailure(code: "audio_format") }
        var packets: [AVAudioPCMBuffer] = []
        while file.framePosition < file.length {
            let remaining = AVAudioFrameCount(min(1024, file.length - file.framePosition))
            guard let buffer = AVAudioPCMBuffer(pcmFormat: format, frameCapacity: remaining) else { throw MeetingAudioFailure(code: "audio_format") }
            try file.read(into: buffer, frameCount: remaining)
            packets.append(buffer)
        }
        let installed = await SpeechTranscriber.installedLocales.map { $0.identifier(.bcp47) }.sorted()
        guard let locale = installed.first(where: { $0 == "en-US" }) ?? installed.first else { replayLog("SKIP no installed speech locale (no download attempted)"); return }
        let previewModule = SpeechTranscriber(locale: Locale(identifier: locale), transcriptionOptions: [], reportingOptions: [.volatileResults], attributeOptions: [.audioTimeRange])
        let finalModule = SpeechTranscriber(locale: Locale(identifier: locale), transcriptionOptions: [], reportingOptions: [], attributeOptions: [.audioTimeRange])
        let previewStatus = await AssetInventory.status(forModules: [previewModule])
        let finalStatus = await AssetInventory.status(forModules: [finalModule])
        let combinedStatus = await AssetInventory.status(forModules: [previewModule, finalModule])
        replayLog("INSTALLED speech_locale=\(locale) available=\(SpeechTranscriber.isAvailable) preview_status=\(previewStatus) final_status=\(finalStatus) combined_status=\(combinedStatus)")
        let request: [String: Any] = ["version": 1, "kind": "start", "session_id": "synthetic-throughput", "locale": locale, "tracks": ["system"], "explicit_start": true, "startup_ms": 8000]
        let configuration = try decodeMeetingAudioConfiguration(JSONSerialization.data(withJSONObject: request))
        let counts = MeetingAudioReplayCounts()
        let track = MeetingAudioSpeechTrack(track: "system", configuration: configuration, epoch: 1, emit: { counts.receive($0) })
        let activity = MeetingAudioProcessActivity()
        defer { activity.end() }
        do { try await track.start(cancelled: MeetingAudioCancellation()) }
        catch let error as MeetingAudioFailure where error.code == "assets_missing" { replayLog("SKIP assets_missing (no download attempted)"); return }
        replayLog("START installed-asset synthetic speech replay duration_seconds=\(Int(duration)) cadence_frames=1024 sample_rate=48000")
        let started = DispatchTime.now().uptimeNanoseconds
        var frames: UInt64 = 0
        var index = 0
        var nextProgress = 30.0
        while Double(frames) / 48000 < duration {
            let buffer = packets[index % packets.count]
            track.offer(MeetingAudioPCM(buffer, startMilliseconds: Double(frames) / 48))
            frames += UInt64(buffer.frameLength)
            index += 1
            let snapshot = counts.snapshot()
            if let error = snapshot.2 {
                try await track.stop(finalize: false)
                replayLog("FAIL stage=\(error) audio_seconds=\(Double(frames) / 48000) finals=\(snapshot.0) previews=\(snapshot.1)")
                throw MeetingAudioFailure(code: error)
            }
            let seconds = Double(frames) / 48000
            if seconds >= nextProgress {
                let elapsed = Double(DispatchTime.now().uptimeNanoseconds - started) / 1_000_000_000
                replayLog("PROGRESS audio_seconds=\(Int(seconds)) elapsed_seconds=\(Int(elapsed)) finals=\(snapshot.0) previews=\(snapshot.1)")
                nextProgress += 30
            }
            let deadline = started + UInt64(Double(frames) / 48000 * 1_000_000_000)
            let now = DispatchTime.now().uptimeNanoseconds
            if deadline > now { try await Task.sleep(nanoseconds: deadline - now) }
        }
        try await track.stop(finalize: true)
        let snapshot = counts.snapshot()
        guard snapshot.2 == nil, snapshot.0 > 0 else { throw MeetingAudioFailure(code: snapshot.2 ?? "transcription_failed") }
        replayLog("PASS actual SpeechAnalyzer replay audio_seconds=\(Int(Double(frames) / 48000)) finals=\(snapshot.0) previews=\(snapshot.1) stage_errors=0")
    }
}
