import Foundation
@preconcurrency import AVFoundation

// RMS reads the current bounded PCM buffer synchronously. It retains no audio,
// performs no inference, and emits no text. Unsupported/nonfinite input simply
// has no meter sample; it never makes transcript persistence fail.
func meetingAudioRMS(_ buffer: AVAudioPCMBuffer) -> Double? {
    let frames = Int(buffer.frameLength)
    guard frames > 0, frames <= 8192, buffer.format.channelCount > 0, buffer.format.channelCount <= 8,
          buffer.format.streamDescription.pointee.mFormatFlags & kAudioFormatFlagIsBigEndian == 0 else { return nil }
    let width: Int
    switch buffer.format.commonFormat {
    case .pcmFormatFloat32, .pcmFormatInt32: width = 4
    case .pcmFormatFloat64: width = 8
    case .pcmFormatInt16: width = 2
    default: return nil
    }
    var sum = 0.0
    var samples = 0
    var channels = 0
    for input in UnsafeMutableAudioBufferListPointer(buffer.mutableAudioBufferList) {
        let count = Int(input.mNumberChannels)
        guard count > 0, count <= 8, channels + count <= 8,
              Int(input.mDataByteSize) >= frames * count * width, let data = input.mData else { return nil }
        channels += count
        for index in 0..<(frames * count) {
            let value: Double
            switch buffer.format.commonFormat {
            case .pcmFormatFloat32: value = Double(data.assumingMemoryBound(to: Float.self)[index])
            case .pcmFormatFloat64: value = data.assumingMemoryBound(to: Double.self)[index]
            case .pcmFormatInt16: value = Double(data.assumingMemoryBound(to: Int16.self)[index]) / 32768
            case .pcmFormatInt32: value = Double(data.assumingMemoryBound(to: Int32.self)[index]) / 2147483648
            default: return nil
            }
            guard value.isFinite else { return nil }
            let normalized = min(1, max(-1, value))
            sum += normalized * normalized
            samples += 1
        }
    }
    guard channels == Int(buffer.format.channelCount), samples > 0 else { return nil }
    return min(1, max(0, sqrt(sum / Double(samples))))
}

final class MeetingAudioLevelMeter: @unchecked Sendable {
    private let lock = NSLock()
    private let sessionID: String
    private let epoch: UInt64
    private let emit: @Sendable (MeetingAudioEvent) -> Void
    private var last: [String: Double] = [:]
    init(sessionID: String, epoch: UInt64, emit: @escaping @Sendable (MeetingAudioEvent) -> Void) {
        self.sessionID = sessionID; self.epoch = epoch; self.emit = emit
    }
    func offer(track: String, buffer: AVAudioPCMBuffer, milliseconds: Double) {
        if let event = event(track: track, buffer: buffer, milliseconds: milliseconds) { emit(event) }
    }
    func event(track: String, buffer: AVAudioPCMBuffer, milliseconds: Double) -> MeetingAudioEvent? {
        guard ["microphone", "system"].contains(track), meetingAudioTimestamp(milliseconds) != nil else { return nil }
        guard lock.withLock({ last[track].map { milliseconds >= $0 + 100 } ?? true }) else { return nil }
        guard let level = meetingAudioRMS(buffer) else { return nil }
        return lock.withLock {
            guard last[track].map({ milliseconds >= $0 + 100 }) ?? true else { return nil }
            last[track] = milliseconds
            return MeetingAudioEvent(version: 1, sessionID: sessionID, epoch: epoch, kind: "level", track: track, level: level)
        }
    }
}
