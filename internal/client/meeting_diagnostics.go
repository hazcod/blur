package client

import (
	"errors"
	"log/slog"

	"github.com/hazcod/blur/internal/meetings"
)

// Native errors can contain recognized words or device details. Log only our
// closed reason vocabulary, without error strings, transcript or identifiers.
func logMeetingFailure(logger *slog.Logger, code string, err error) {
	reason := "unknown"
	switch {
	case errors.Is(err, meetings.ErrOrder):
		reason = "transcript_order"
	case errors.Is(err, meetings.ErrLimit):
		reason = "transcript_limit"
	case errors.Is(err, meetings.ErrConflict):
		reason = "save_conflict"
	case errors.Is(err, meetings.ErrCanceled):
		reason = "save_canceled"
	default:
		switch code {
		case "audio_interrupted", "invalid_event", "save_failed", "invalid_request", "speech_unavailable", "assets_missing", "microphone_permission", "system_permission", "device_unavailable", "audio_format", "audio_clock", "audio_conversion", "audio_overflow", "audio_capture_overflow", "audio_analyzer_overflow", "audio_capture_stopped", "audio_analyzer_stopped", "transcript_limit", "transcription_failed", "device_changed", "device_failed", "device_stop_failed", "start_failed", "startup_timeout", "stop_failed", "stop_timeout", "event_overflow", "setup_unavailable", "setup_failed", "setup_timeout", "locale_unsupported", "assets_install_failed":
			reason = code
		}
	}
	logger.Warn("meeting transcription stopped", "reason", reason)
}

func logMeetingState(state string) {
	switch state {
	case "starting", "running", "pausing", "paused", "stopping", "completed", "cancelled":
		slog.Info("meeting transcription state changed", "state", state)
	}
}
