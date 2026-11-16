import Foundation
@preconcurrency import AVFoundation
import CoreGraphics
import Speech

extension NativeMeetingAudioFactory {
    // These methods are reachable only from an explicitly gated setup request.
    // Probe and Start never call them.
    func requestAccess(tracks: [String]) async throws {
        try Task.checkCancellation()
        if tracks.contains("microphone") {
            let status = AVCaptureDevice.authorizationStatus(for: .audio)
            if status == .notDetermined {
                guard await AVCaptureDevice.requestAccess(for: .audio) else { throw MeetingAudioFailure(code: "microphone_permission") }
            } else if status != .authorized { throw MeetingAudioFailure(code: "microphone_permission") }
        }
        try Task.checkCancellation()
        if tracks.contains("system"), !CGPreflightScreenCaptureAccess() {
            let granted = await MainActor.run { CGRequestScreenCaptureAccess() }
            guard granted else { throw MeetingAudioFailure(code: "system_permission") }
        }
        try Task.checkCancellation()
    }
    func installLocale(_ locale: String) async throws {
        guard #available(macOS 26.0, *) else { throw MeetingAudioFailure(code: "speech_unavailable") }
        guard SpeechTranscriber.isAvailable else { throw MeetingAudioFailure(code: "speech_unavailable") }
        try Task.checkCancellation()
        let wanted = Locale(identifier: locale)
        guard await SpeechTranscriber.supportedLocales.contains(where: { $0.identifier(.bcp47).caseInsensitiveCompare(wanted.identifier(.bcp47)) == .orderedSame }) else { throw MeetingAudioFailure(code: "locale_unsupported") }
        let preview = SpeechTranscriber(locale: wanted, transcriptionOptions: [], reportingOptions: [.volatileResults], attributeOptions: [.audioTimeRange])
        let finals = SpeechTranscriber(locale: wanted, transcriptionOptions: [], reportingOptions: [], attributeOptions: [.audioTimeRange])
        try Task.checkCancellation()
        do {
            // Apple may reserve the locale here. Downloads can be consolidated
            // and retried by the OS later; cancellation cannot undo that consent.
            if let request = try await AssetInventory.assetInstallationRequest(supporting: [preview, finals]) {
                try Task.checkCancellation()
                try await request.downloadAndInstall()
            }
            try Task.checkCancellation()
            guard await AssetInventory.status(forModules: [preview, finals]) == .installed else { throw MeetingAudioFailure(code: "assets_install_failed") }
        } catch is CancellationError { throw CancellationError() }
        catch { throw MeetingAudioFailure(code: "assets_install_failed") }
    }
}
