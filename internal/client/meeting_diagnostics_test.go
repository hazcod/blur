package client

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/hazcod/blur/internal/meetings"
)

func TestMeetingFailureDiagnosticsExposeClosedReasonsWithoutPayload(t *testing.T) {
	for _, fixture := range []struct {
		code string
		err  error
		want string
	}{
		{"audio_overflow", nil, "audio_overflow"},
		{"audio_capture_overflow", nil, "audio_capture_overflow"},
		{"audio_analyzer_overflow", nil, "audio_analyzer_overflow"},
		{"audio_capture_stopped", nil, "audio_capture_stopped"},
		{"audio_analyzer_stopped", nil, "audio_analyzer_stopped"},
		{"save_failed", meetings.ErrOrder, "transcript_order"},
		{"save_failed", meetings.ErrLimit, "transcript_limit"},
		{"save_failed", meetings.ErrConflict, "save_conflict"},
		{"save_failed", errors.New("PRIVATE TRANSCRIPT PAYLOAD"), "save_failed"},
		{"PRIVATE TRANSCRIPT PAYLOAD", nil, "unknown"},
	} {
		var output bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&output, nil))
		logMeetingFailure(logger, fixture.code, fixture.err)
		text := output.String()
		if !strings.Contains(text, "reason="+fixture.want) || strings.Contains(text, "PRIVATE") || strings.Contains(text, "session_id") {
			t.Fatal("stop diagnostics hid their closed cause or exposed source/identity data")
		}
	}
}
