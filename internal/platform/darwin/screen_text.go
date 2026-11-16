package darwin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

var (
	ErrScreenTextUnavailable = errors.New("darwin: visual text capture unavailable")
	ErrScreenTextInvalid     = errors.New("darwin: invalid visual text capture request or reply")
)

const screenTextMaxReplyBytes = 2 * 1024 * 1024

// ScreenTextWindow is a topmost on-screen candidate, not an Accessibility focus
// assertion. Visual capture has no secure-field or browser privacy semantics.
type ScreenTextWindow struct {
	PID               int32          `json:"pid"`
	WindowID          uint32         `json:"window_id"`
	BundleID          string         `json:"bundle_id"`
	Title             string         `json:"title"`
	TitleAvailable    bool           `json:"title_available"`
	TitleTruncated    bool           `json:"title_truncated"`
	Frame             ScreenTextRect `json:"frame"`
	FullOnscreen      bool           `json:"full_onscreen"`
	Unobscured        bool           `json:"unobscured"`
	PermissionGranted bool           `json:"permission_granted"`
	LeaseToken        string         `json:"lease_token"`
}

type ScreenTextRect struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type ScreenTextLine struct {
	Text       string         `json:"text"`
	Confidence float64        `json:"confidence"`
	Rect       ScreenTextRect `json:"rect"`
}

type ScreenTextRequest struct {
	RequestID    string
	PID          int32
	WindowID     uint32
	LeaseToken   string
	MaxTextBytes int
	Timeout      time.Duration
	// Region is an explicitly verified source rectangle in global, top-left
	// display points. Native capture must crop before acquiring any pixels.
	Region *ScreenTextRect
}

type ScreenTextResult struct {
	Window     ScreenTextWindow
	Text       string
	Lines      []ScreenTextLine
	Truncated  bool
	CapturedAt time.Time
	Region     *ScreenTextRect
}

type screenTextWireRequest struct {
	Version      int             `json:"version"`
	Kind         string          `json:"kind"`
	RequestID    string          `json:"request_id"`
	PID          int32           `json:"pid,omitempty"`
	WindowID     uint32          `json:"window_id,omitempty"`
	LeaseToken   string          `json:"lease_token,omitempty"`
	MaxTextBytes int             `json:"max_text_bytes,omitempty"`
	TimeoutMS    int             `json:"timeout_ms"`
	Region       *ScreenTextRect `json:"region,omitempty"`
}

type screenTextWireReply struct {
	Version                int               `json:"version"`
	RequestID              string            `json:"request_id"`
	PID                    int32             `json:"pid"`
	WindowID               uint32            `json:"window_id"`
	CapturedAtMilliseconds int64             `json:"captured_at_milliseconds"`
	Status                 string            `json:"status"`
	Window                 *ScreenTextWindow `json:"window,omitempty"`
	Text                   *string           `json:"text,omitempty"`
	Lines                  []ScreenTextLine  `json:"lines,omitempty"`
	Truncated              bool              `json:"truncated"`
	Region                 *ScreenTextRect   `json:"region,omitempty"`
}

func decodeScreenTextReply(data []byte, request screenTextWireRequest) (screenTextWireReply, error) {
	var reply screenTextWireReply
	if len(data) == 0 || len(data) > screenTextMaxReplyBytes || !utf8.Valid(data) {
		return reply, ErrScreenTextInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&reply); err != nil {
		return screenTextWireReply{}, ErrScreenTextInvalid
	}
	var tail any
	if decoder.Decode(&tail) != io.EOF || reply.Version != 1 || reply.RequestID != request.RequestID || reply.PID != request.PID || reply.WindowID != request.WindowID || reply.CapturedAtMilliseconds <= 0 || !validScreenTextStatus(reply.Status) {
		return screenTextWireReply{}, ErrScreenTextInvalid
	}
	if reply.Window != nil && !validScreenTextWindow(*reply.Window) {
		return screenTextWireReply{}, ErrScreenTextInvalid
	}
	if request.Kind == "topmost" {
		if reply.Region != nil || reply.Text != nil || len(reply.Lines) != 0 || reply.Truncated || reply.Status == "ok" && reply.Window == nil {
			return screenTextWireReply{}, ErrScreenTextInvalid
		}
		return reply, nil
	}
	if request.Kind != "recognize" {
		return screenTextWireReply{}, ErrScreenTextInvalid
	}
	if !sameScreenTextRegion(request.Region, reply.Region) {
		return screenTextWireReply{}, ErrScreenTextInvalid
	}
	if reply.Status != "ok" {
		if reply.Text != nil && *reply.Text != "" || len(reply.Lines) != 0 {
			return screenTextWireReply{}, ErrScreenTextInvalid
		}
		return reply, nil
	}
	if reply.Window == nil || reply.Window.PID != request.PID || reply.Window.WindowID != request.WindowID || reply.Window.LeaseToken != request.LeaseToken || !reply.Window.PermissionGranted || !reply.Window.FullOnscreen || !reply.Window.Unobscured || reply.Text == nil || *reply.Text == "" || len(*reply.Text) > request.MaxTextBytes || len(reply.Lines) == 0 || len(reply.Lines) > 512 {
		return screenTextWireReply{}, ErrScreenTextInvalid
	}
	if request.Region != nil && !screenTextRegionContained(*request.Region, reply.Window.Frame) {
		return screenTextWireReply{}, ErrScreenTextInvalid
	}
	lines := make([]string, len(reply.Lines))
	for i, line := range reply.Lines {
		if line.Text == "" || len(line.Text) > request.MaxTextBytes || math.IsNaN(line.Confidence) || math.IsInf(line.Confidence, 0) || line.Confidence < 0 || line.Confidence > 1 || !validScreenTextLineRect(line.Rect) {
			return screenTextWireReply{}, ErrScreenTextInvalid
		}
		lines[i] = line.Text
	}
	if strings.Join(lines, "\n") != *reply.Text {
		return screenTextWireReply{}, ErrScreenTextInvalid
	}
	return reply, nil
}

func sameScreenTextRegion(a, b *ScreenTextRect) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b && validScreenTextRegion(*a)
}

func validScreenTextRegion(r ScreenTextRect) bool {
	return finiteScreenTextRect(r) && r.Width >= 1 && r.Height >= 1 && r.Width <= 32768 && r.Height <= 32768 && math.Abs(r.X) <= 1000000 && math.Abs(r.Y) <= 1000000
}

func screenTextRegionContained(r, window ScreenTextRect) bool {
	return validScreenTextRegion(r) && r.X >= window.X && r.Y >= window.Y && r.X+r.Width <= window.X+window.Width && r.Y+r.Height <= window.Y+window.Height
}

func validScreenTextStatus(status string) bool {
	switch status {
	case "ok", "invalid_request", "permission_required", "no_window", "target_changed", "not_visible", "timeout", "cancelled", "capture_unavailable", "image_limit", "ocr_unavailable", "no_text", "reply_limit":
		return true
	default:
		return false
	}
}

func validScreenTextWindow(window ScreenTextWindow) bool {
	return window.PID > 0 && window.WindowID > 0 && len(window.BundleID) <= 256 && utf8.ValidString(window.BundleID) && len(window.Title) <= 512 && utf8.ValidString(window.Title) && (window.TitleAvailable || window.Title == "" && !window.TitleTruncated) && (window.PermissionGranted || !window.TitleAvailable) && validScreenTextToken(window.LeaseToken) && finiteScreenTextRect(window.Frame) && window.Frame.Width >= 1 && window.Frame.Height >= 1 && window.Frame.Width <= 32768 && window.Frame.Height <= 32768 && math.Abs(window.Frame.X) <= 1000000 && math.Abs(window.Frame.Y) <= 1000000
}

func finiteScreenTextRect(r ScreenTextRect) bool {
	for _, v := range []float64{r.X, r.Y, r.Width, r.Height} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	return true
}

func validScreenTextLineRect(r ScreenTextRect) bool {
	return finiteScreenTextRect(r) && r.X >= -0.00001 && r.Y >= -0.00001 && r.Width >= 0 && r.Height >= 0 && r.X+r.Width <= 1.00001 && r.Y+r.Height <= 1.00001
}

func validScreenTextToken(token string) bool {
	if len(token) != 64 {
		return false
	}
	for _, c := range token {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func validScreenTextID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

type ScreenTextError struct{ Status string }

func (e *ScreenTextError) Error() string {
	switch e.Status {
	case "permission_required":
		return "Screen Recording permission is required for visual text capture"
	case "target_changed":
		return "the foreground window changed during visual text capture"
	case "not_visible":
		return "visual text capture requires a fully visible, unobscured window"
	case "timeout":
		return "visual text capture timed out"
	case "no_text":
		return "the window image exposed no recognized text"
	case "no_window":
		return "no foreground window is available for visual text capture"
	case "closed":
		return "visual text capture is closed"
	default:
		return "visual text capture is unavailable"
	}
}

type screenTextNativeAPI struct {
	start  func(*byte, int32) uint64
	poll   func(uint64, *byte, int32) int32
	cancel func(uint64)
}

// ScreenTextBridge never prompts or enables visual capture. The caller must
// obtain consent and enforce app, site, private-window and secure-input policy.
// Close cancels this wrapper's requests; the native library stays loaded until
// process exit because late framework callbacks may still execute its code.
type ScreenTextBridge struct {
	mu      sync.Mutex
	api     screenTextNativeAPI
	closed  bool
	handles map[uint64]struct{}
}

func newScreenTextBridge(api screenTextNativeAPI) *ScreenTextBridge {
	return &ScreenTextBridge{api: api, handles: make(map[uint64]struct{})}
}

func (b *ScreenTextBridge) Available() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return !b.closed
}

func (b *ScreenTextBridge) Close() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	b.closed = true
	handles := make([]uint64, 0, len(b.handles))
	for handle := range b.handles {
		handles = append(handles, handle)
	}
	b.handles = make(map[uint64]struct{})
	b.mu.Unlock()
	for _, handle := range handles {
		b.api.cancel(handle)
	}
	return nil
}

var screenTextRequestSequence atomic.Uint64

func (b *ScreenTextBridge) TopmostWindow(ctx context.Context) (ScreenTextWindow, error) {
	id := "topmost-" + strconv.FormatUint(screenTextRequestSequence.Add(1), 10) + "-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	reply, err := b.execute(ctx, screenTextWireRequest{Version: 1, Kind: "topmost", RequestID: id, TimeoutMS: 1000})
	if err != nil {
		return ScreenTextWindow{}, err
	}
	return *reply.Window, nil
}

func (b *ScreenTextBridge) RecognizeWindow(ctx context.Context, request ScreenTextRequest) (ScreenTextResult, error) {
	if request.MaxTextBytes == 0 {
		request.MaxTextBytes = 262144
	}
	if request.Timeout == 0 {
		request.Timeout = 8 * time.Second
	}
	if !validScreenTextID(request.RequestID) || request.PID <= 0 || request.WindowID == 0 || !validScreenTextToken(request.LeaseToken) || request.MaxTextBytes < 1 || request.MaxTextBytes > 262144 || request.Timeout < 100*time.Millisecond || request.Timeout > 8*time.Second {
		return ScreenTextResult{}, ErrScreenTextInvalid
	}
	if request.Region != nil {
		region := *request.Region
		if !validScreenTextRegion(region) {
			return ScreenTextResult{}, ErrScreenTextInvalid
		}
		request.Region = &region
	}
	reply, err := b.execute(ctx, screenTextWireRequest{Version: 1, Kind: "recognize", RequestID: request.RequestID, PID: request.PID, WindowID: request.WindowID, LeaseToken: request.LeaseToken, MaxTextBytes: request.MaxTextBytes, TimeoutMS: int(request.Timeout / time.Millisecond), Region: request.Region})
	if err != nil {
		return ScreenTextResult{}, err
	}
	return ScreenTextResult{Window: *reply.Window, Text: *reply.Text, Lines: reply.Lines, Truncated: reply.Truncated, CapturedAt: time.UnixMilli(reply.CapturedAtMilliseconds), Region: reply.Region}, nil
}

func (b *ScreenTextBridge) execute(ctx context.Context, request screenTextWireRequest) (screenTextWireReply, error) {
	var empty screenTextWireReply
	if ctx == nil {
		return empty, ErrScreenTextInvalid
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if b == nil {
		return empty, ErrScreenTextUnavailable
	}
	data, err := json.Marshal(request)
	if err != nil || len(data) == 0 || len(data) > 4096 {
		return empty, ErrScreenTextInvalid
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return empty, &ScreenTextError{Status: "closed"}
	}
	handle := b.api.start(&data[0], int32(len(data)))
	runtime.KeepAlive(data)
	if handle != 0 {
		b.handles[handle] = struct{}{}
	}
	b.mu.Unlock()
	if handle == 0 {
		return empty, ErrScreenTextUnavailable
	}
	defer func() { b.mu.Lock(); delete(b.handles, handle); b.mu.Unlock(); b.api.cancel(handle) }()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(time.Duration(request.TimeoutMS)*time.Millisecond + 250*time.Millisecond)
	defer deadline.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return empty, err
		}
		b.mu.Lock()
		closed := b.closed
		b.mu.Unlock()
		if closed {
			return empty, &ScreenTextError{Status: "closed"}
		}
		size := b.api.poll(handle, nil, 0)
		if size < 0 || size > screenTextMaxReplyBytes {
			return empty, ErrScreenTextInvalid
		}
		if size > 0 {
			result := make([]byte, int(size))
			if b.api.poll(handle, &result[0], size) != size {
				return empty, ErrScreenTextInvalid
			}
			runtime.KeepAlive(result)
			if err := ctx.Err(); err != nil {
				return empty, err
			}
			b.mu.Lock()
			closed = b.closed
			b.mu.Unlock()
			if closed {
				return empty, &ScreenTextError{Status: "closed"}
			}
			reply, err := decodeScreenTextReply(result, request)
			if err != nil {
				return empty, err
			}
			if reply.Status != "ok" {
				return empty, &ScreenTextError{Status: reply.Status}
			}
			return reply, nil
		}
		select {
		case <-ctx.Done():
			return empty, ctx.Err()
		case <-deadline.C:
			return empty, &ScreenTextError{Status: "timeout"}
		case <-ticker.C:
		}
	}
}
