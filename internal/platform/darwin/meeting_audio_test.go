package darwin

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"unsafe"
)

const meetingAudioFinalFixture = `{"version":1,"session_id":"synthetic","events":[{"version":1,"session_id":"synthetic","epoch":18446744073709551615,"kind":"transcript","track":"microphone","segment_id":"microphone-1-1","revision":18446744073709551615,"start_ms":0,"end_ms":100,"final":true,"text":"synthetic final"}]}`

func TestMeetingAudioExplicitSetupUsesGatedOperationsWithoutAudio(t *testing.T) {
	var request meetingAudioRequest
	var opens int
	failure := ""
	bridge := newMeetingAudioBridge(meetingAudioNativeAPI{
		open: func(data *byte, length int32) uint64 {
			opens++
			request = meetingAudioRequest{}
			if err := json.Unmarshal(unsafe.Slice(data, int(length)), &request); err != nil {
				t.Fatal(err)
			}
			return 1
		},
		poll: func(_ uint64, data *byte, capacity int32) int32 {
			state := "completed"
			if failure != "" {
				state = "failed"
			}
			encoded, err := json.Marshal(meetingAudioBatch{Version: 1, SessionID: request.SessionID, Events: []MeetingAudioEvent{{Version: 1, SessionID: request.SessionID, Kind: "operation", State: state, Error: failure}}})
			if err != nil {
				t.Fatal(err)
			}
			if data != nil {
				copy(unsafe.Slice(data, int(capacity)), encoded)
			}
			return int32(len(encoded))
		},
		cancel: func(uint64) {},
	})
	if err := bridge.RequestAccess(context.Background(), []string{"microphone", "system"}); err != nil || request.Kind != "request_access" || !request.ExplicitSetup || request.ExplicitStart {
		t.Fatalf("permission action not isolated/gated: %v", err)
	}
	if err := bridge.InstallLocale(context.Background(), "en-US"); err != nil || request.Kind != "install_locale" || !request.ExplicitSetup || len(request.Tracks) != 0 {
		t.Fatalf("install action not isolated/gated: %v", err)
	}
	failure = "assets_install_failed"
	var operationError MeetingAudioOperationError
	if err := bridge.InstallLocale(context.Background(), "en-US"); !errors.As(err, &operationError) || operationError.Code != failure {
		t.Fatalf("setup failure not visible: %v", err)
	}
	before := opens
	if !errors.Is(bridge.RequestAccess(context.Background(), []string{"system", "system"}), ErrMeetingAudioInvalid) || opens != before {
		t.Fatal("invalid setup reached native code")
	}
}

func TestMeetingAudioPreviewCannotMasqueradeAsDurableTranscript(t *testing.T) {
	preview := strings.Replace(strings.Replace(meetingAudioFinalFixture, `"kind":"transcript"`, `"kind":"preview"`, 1), `"final":true`, `"final":false`, 1)
	if events, err := decodeMeetingAudioBatch([]byte(preview), "synthetic"); err != nil || len(events) != 1 || events[0].Kind != "preview" {
		t.Fatalf("separate preview stream rejected: %v", err)
	}
	for _, invalid := range []string{
		strings.Replace(meetingAudioFinalFixture, `"final":true`, `"final":false`, 1),
		strings.Replace(preview, `"final":false`, `"final":true`, 1),
	} {
		if _, err := decodeMeetingAudioBatch([]byte(invalid), "synthetic"); !errors.Is(err, ErrMeetingAudioInvalid) {
			t.Fatal("preview/final kind confusion accepted")
		}
	}
}

func TestMeetingAudioBatchPreservesIntegerBoundsAndRejectsUnsafeEvents(t *testing.T) {
	events, err := decodeMeetingAudioBatch([]byte(meetingAudioFinalFixture), "synthetic")
	if err != nil || len(events) != 1 || events[0].Epoch != math.MaxUint64 || events[0].Revision != math.MaxUint64 || events[0].StartMS == nil || *events[0].StartMS != 0 {
		t.Fatalf("integer precision lost: events=%v err=%v", events, err)
	}
	for _, data := range []string{
		strings.Replace(meetingAudioFinalFixture, `"session_id":"synthetic"`, `"session_id":"late"`, 1),
		strings.Replace(meetingAudioFinalFixture, `"final":true,`, "", 1),
		strings.Replace(meetingAudioFinalFixture, `"end_ms":100`, `"end_ms":86400001`, 1),
		strings.Replace(meetingAudioFinalFixture, `"track":"microphone"`, `"track":"speaker name"`, 1),
		strings.Replace(meetingAudioFinalFixture, `"text":"synthetic final"`, `"text":"`+strings.Repeat("x", 16385)+`"`, 1),
		strings.Replace(meetingAudioFinalFixture, `"epoch":18446744073709551615`, `"epoch":18446744073709551616`, 1),
		strings.Replace(meetingAudioFinalFixture, `"kind":"transcript"`, `"kind":"error","error":"backend user words"`, 1),
		meetingAudioFinalFixture + ` {}`,
	} {
		if _, err := decodeMeetingAudioBatch([]byte(data), "synthetic"); !errors.Is(err, ErrMeetingAudioInvalid) {
			t.Fatalf("unsafe event accepted: %v", err)
		}
	}
}

func TestMeetingAudioPollCancellationRetainsConsumedFinals(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var consumed atomic.Int32
	var cancelled atomic.Int32
	bridge := newMeetingAudioBridge(meetingAudioNativeAPI{
		open: func(*byte, int32) uint64 { return 1 },
		poll: func(_ uint64, out *byte, capacity int32) int32 {
			if out == nil {
				return int32(len(meetingAudioFinalFixture))
			}
			copy(unsafe.Slice(out, int(capacity)), meetingAudioFinalFixture)
			consumed.Add(1)
			cancel()
			return int32(len(meetingAudioFinalFixture))
		},
		cancel: func(uint64) { cancelled.Add(1) },
	})
	session, err := bridge.Start(ctx, MeetingAudioStart{SessionID: "synthetic", Locale: "en-US", Tracks: []string{"microphone"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Poll(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled wait did not return cancellation: %v", err)
	}
	events, err := session.Poll(context.Background())
	if err != nil || len(events) != 1 || events[0].Text != "synthetic final" || consumed.Load() != 1 || cancelled.Load() != 0 {
		t.Fatalf("cancel discarded or consumed final twice: events=%v err=%v consumed=%d cancelled=%d", events, err, consumed.Load(), cancelled.Load())
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if cancelled.Load() != 1 {
		t.Fatal("explicit close did not cancel native session")
	}
}

func TestMeetingAudioNativeLibraryABIWithoutAudioOrProbe(t *testing.T) {
	path := os.Getenv("BLUR_MEETING_AUDIO_TEST_LIBRARY")
	if path == "" {
		t.Skip("isolated native build supplied by synthetic test script")
	}
	bridge, err := OpenMeetingAudioBridge(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := bridge.Close(); err != nil {
			t.Error(err)
		}
	})
	if !bridge.Available() {
		t.Fatal("compiled bridge unavailable")
	}
	if bridge.api.open(nil, 0) != 0 {
		t.Fatal("nil config accepted")
	}
	invalid := []byte(`{"version":0,"kind":"probe","session_id":"invalid","locale":"en-US"}`)
	if bridge.api.open(&invalid[0], int32(len(invalid))) != 0 {
		t.Fatal("invalid config reached probe")
	}
	if bridge.api.poll(math.MaxUint64, nil, 0) != -1 || bridge.api.control(math.MaxUint64, nil, 0) != -1 {
		t.Fatal("invalid handle accepted")
	}
}

func TestMeetingAudioControlPreservesUnsignedEpoch(t *testing.T) {
	var epoch uint64
	bridge := newMeetingAudioBridge(meetingAudioNativeAPI{
		open: func(*byte, int32) uint64 { return 1 },
		control: func(_ uint64, data *byte, length int32) int32 {
			var request struct {
				Epoch uint64 `json:"epoch"`
			}
			if err := json.Unmarshal(unsafe.Slice(data, int(length)), &request); err != nil {
				t.Fatal(err)
			}
			epoch = request.Epoch
			return 1
		},
		cancel: func(uint64) {},
	})
	session, err := bridge.Start(context.Background(), MeetingAudioStart{SessionID: "synthetic", Locale: "en-US", Tracks: []string{"microphone", "system"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := session.Control("stop", math.MaxUint64); err != nil || epoch != math.MaxUint64 {
		t.Fatalf("epoch rounded in control request: epoch=%d err=%v", epoch, err)
	}
}

func TestMeetingAudioQueueDiagnosticsRetainStageAndDistinguishTermination(t *testing.T) {
	for _, code := range []string{"audio_capture_overflow", "audio_analyzer_overflow", "audio_capture_stopped", "audio_analyzer_stopped"} {
		encoded, err := json.Marshal(meetingAudioBatch{Version: 1, SessionID: "synthetic", Events: []MeetingAudioEvent{{Version: 1, SessionID: "synthetic", Epoch: 1, Kind: "error", Error: code}}})
		if err != nil {
			t.Fatal(err)
		}
		events, err := decodeMeetingAudioBatch(encoded, "synthetic")
		if err != nil || len(events) != 1 || events[0].Error != code {
			t.Fatal("closed native stage diagnostic was lost", code, err)
		}
	}
}
