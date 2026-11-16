package darwin

import (
	"context"
	"errors"
	"math"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestScreenTextReplyBindsWindowAndRejectsUnboundedOrInconsistentText(t *testing.T) {
	request := screenTextWireRequest{Version: 1, Kind: "recognize", RequestID: "synthetic", PID: 7, WindowID: 42, LeaseToken: strings.Repeat("a", 64), MaxTextBytes: 32, TimeoutMS: 1000}
	valid := `{"version":1,"request_id":"synthetic","pid":7,"window_id":42,"captured_at_milliseconds":1,"status":"ok","window":{"pid":7,"window_id":42,"bundle_id":"test.synthetic","title":"Synthetic","title_available":true,"title_truncated":false,"frame":{"x":0,"y":0,"width":100,"height":100},"full_onscreen":true,"unobscured":true,"permission_granted":true,"lease_token":"` + strings.Repeat("a", 64) + `"},"text":"Mira owns rollback","lines":[{"text":"Mira owns rollback","confidence":0.95,"rect":{"x":0.1,"y":0.1,"width":0.8,"height":0.1}}],"truncated":false}`
	got, err := decodeScreenTextReply([]byte(valid), request)
	if err != nil || got.Text == nil || *got.Text != "Mira owns rollback" {
		t.Fatalf("valid bounded reply rejected: %v", err)
	}
	for _, data := range []string{
		strings.Replace(valid, `"pid":7`, `"pid":8`, 1),
		strings.Replace(valid, `"request_id":"synthetic"`, `"request_id":"late"`, 1),
		strings.Replace(valid, `"text":"Mira owns rollback","lines"`, `"text":"invented","lines"`, 1),
		strings.Replace(valid, `"confidence":0.95`, `"confidence":1.1`, 1),
		strings.Replace(valid, `"full_onscreen":true`, `"full_onscreen":false`, 1),
		strings.Replace(valid, `"status":"ok"`, `"status":"permission_required"`, 1),
		valid + ` {}`,
		strings.Replace(valid, `"truncated":false}`, `"truncated":false,"pixels":"hidden"}`, 1),
	} {
		if _, err := decodeScreenTextReply([]byte(data), request); !errors.Is(err, ErrScreenTextInvalid) {
			t.Fatalf("unsafe reply accepted: err=%v", err)
		}
	}
}

func TestScreenTextCancelledRequestStopsNativeHandle(t *testing.T) {
	var starts, cancels atomic.Int32
	bridge := newScreenTextBridge(screenTextNativeAPI{
		start:  func(*byte, int32) uint64 { starts.Add(1); return 7 },
		poll:   func(uint64, *byte, int32) int32 { return 0 },
		cancel: func(uint64) { cancels.Add(1) },
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := bridge.RecognizeWindow(ctx, ScreenTextRequest{RequestID: "cancelled", PID: 7, WindowID: 42, LeaseToken: strings.Repeat("a", 64)})
	if !errors.Is(err, context.DeadlineExceeded) || starts.Load() != 1 || cancels.Load() != 1 {
		t.Fatalf("cancel failed: err=%v starts=%d cancels=%d", err, starts.Load(), cancels.Load())
	}
	if err := bridge.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = bridge.TopmostWindow(context.Background())
	var typed *ScreenTextError
	if !errors.As(err, &typed) || typed.Status != "closed" || starts.Load() != 1 {
		t.Fatalf("closed bridge started work: %v", err)
	}
}

func TestScreenTextInvalidRequestNeverStartsCapture(t *testing.T) {
	bridge := newScreenTextBridge(screenTextNativeAPI{start: func(*byte, int32) uint64 { t.Fatal("invalid request crossed native boundary"); return 0 }})
	for _, request := range []ScreenTextRequest{
		{RequestID: "bad", PID: 7, WindowID: 42},
		{RequestID: "bad", PID: 0, WindowID: 42, LeaseToken: strings.Repeat("a", 64)},
		{RequestID: "bad", PID: 7, WindowID: 42, LeaseToken: strings.Repeat("a", 64), MaxTextBytes: 262145},
		{RequestID: "bad", PID: 7, WindowID: 42, LeaseToken: strings.Repeat("a", 64), Timeout: 9 * time.Second},
	} {
		if _, err := bridge.RecognizeWindow(context.Background(), request); !errors.Is(err, ErrScreenTextInvalid) {
			t.Fatalf("invalid request accepted: %v", err)
		}
	}
}

func TestScreenTextNativeLibraryABIWithoutScreenAccess(t *testing.T) {
	path := os.Getenv("BLUR_SCREEN_TEXT_TEST_LIBRARY")
	if path == "" {
		t.Skip("native dylib supplied only by the isolated synthetic test script")
	}
	bridge, err := OpenScreenTextBridge(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := bridge.Close(); err != nil {
			t.Error(err)
		}
	})
	if !bridge.Available() {
		t.Fatal("native bridge not available")
	}
	if bridge.api.start(nil, 0) != 0 {
		t.Fatal("nil request accepted")
	}
	invalid := []byte(`{"version":0,"kind":"topmost","request_id":"synthetic-invalid"}`)
	if bridge.api.start(&invalid[0], int32(len(invalid))) != 0 {
		t.Fatal("invalid request accepted")
	}
	if bridge.api.poll(math.MaxUint64, nil, 0) != -1 {
		t.Fatal("invalid handle accepted")
	}
}
