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
	ErrMeetingAudioUnavailable = errors.New("darwin: meeting audio unavailable")
	ErrMeetingAudioInvalid     = errors.New("darwin: invalid meeting audio request or event")
	ErrMeetingAudioControl     = errors.New("darwin: meeting audio operation rejected for current state or epoch")
)

type MeetingAudioAvailability struct {
	SpeechAvailable          bool     `json:"speech_available"`
	MicrophonePermission     string   `json:"microphone_permission"`
	SystemPermission         bool     `json:"system_permission"`
	InstalledLocales         []string `json:"installed_locales"`
	RequestedLocaleInstalled bool     `json:"requested_locale_installed"`
}

type MeetingAudioEvent struct {
	Version      int                       `json:"version"`
	SessionID    string                    `json:"session_id"`
	Epoch        uint64                    `json:"epoch"`
	Kind         string                    `json:"kind"`
	State        string                    `json:"state,omitempty"`
	Error        string                    `json:"error,omitempty"`
	Track        string                    `json:"track,omitempty"`
	Level        *float64                  `json:"level,omitempty"`
	SegmentID    string                    `json:"segment_id,omitempty"`
	Revision     uint64                    `json:"revision,omitempty"`
	StartMS      *int64                    `json:"start_ms,omitempty"`
	EndMS        *int64                    `json:"end_ms,omitempty"`
	Final        *bool                     `json:"final,omitempty"`
	Text         string                    `json:"text,omitempty"`
	Availability *MeetingAudioAvailability `json:"availability,omitempty"`
}

type MeetingAudioStart struct {
	SessionID      string
	Locale         string
	Tracks         []string
	StartupTimeout time.Duration
}

// MeetingAudioOperationError contains a closed, content-free setup failure code.
type MeetingAudioOperationError struct{ Code string }

func (e MeetingAudioOperationError) Error() string { return "darwin: meeting audio setup: " + e.Code }

type meetingAudioRequest struct {
	Version       int      `json:"version"`
	Kind          string   `json:"kind"`
	SessionID     string   `json:"session_id"`
	Locale        string   `json:"locale"`
	Tracks        []string `json:"tracks,omitempty"`
	ExplicitStart bool     `json:"explicit_start,omitempty"`
	ExplicitSetup bool     `json:"explicit_setup,omitempty"`
	StartupMS     int      `json:"startup_ms,omitempty"`
}

type meetingAudioBatch struct {
	Version   int                 `json:"version"`
	SessionID string              `json:"session_id"`
	Events    []MeetingAudioEvent `json:"events"`
}

type meetingAudioNativeAPI struct {
	open    func(*byte, int32) uint64
	poll    func(uint64, *byte, int32) int32
	control func(uint64, *byte, int32) int32
	cancel  func(uint64)
}

type MeetingAudioBridge struct {
	mu       sync.Mutex
	api      meetingAudioNativeAPI
	closed   bool
	sessions map[uint64]*MeetingAudioSession
}

type MeetingAudioSession struct {
	bridge    *MeetingAudioBridge
	handle    uint64
	id        string
	pollGate  chan struct{}
	pendingMu sync.Mutex
	pending   []byte
	closed    atomic.Bool
}

func newMeetingAudioBridge(api meetingAudioNativeAPI) *MeetingAudioBridge {
	return &MeetingAudioBridge{api: api, sessions: make(map[uint64]*MeetingAudioSession)}
}

func (b *MeetingAudioBridge) Available() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return !b.closed
}

func (b *MeetingAudioBridge) Close() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	b.closed = true
	sessions := make([]*MeetingAudioSession, 0, len(b.sessions))
	for _, session := range b.sessions {
		sessions = append(sessions, session)
	}
	b.mu.Unlock()
	for _, session := range sessions {
		if err := session.Close(); err != nil {
			return err
		}
	}
	return nil
}

var meetingAudioProbeSequence atomic.Uint64

// RequestAccess may show OS permission UI and must only follow an explicit user
// action. It never starts audio. A canceled wait cannot dismiss an OS prompt.
func (b *MeetingAudioBridge) RequestAccess(ctx context.Context, tracks []string) error {
	if len(tracks) == 0 || len(tracks) > 2 {
		return ErrMeetingAudioInvalid
	}
	seen := make(map[string]bool, len(tracks))
	for _, track := range tracks {
		if seen[track] || (track != "microphone" && track != "system") {
			return ErrMeetingAudioInvalid
		}
		seen[track] = true
	}
	return b.setup(ctx, "request_access", "en-US", append([]string(nil), tracks...))
}

// InstallLocale explicitly downloads/reserves Apple speech assets. The OS may
// retry a failed download later; canceling the wait does not revoke that consent.
func (b *MeetingAudioBridge) InstallLocale(ctx context.Context, locale string) error {
	if !meetingAudioLocale(locale) {
		return ErrMeetingAudioInvalid
	}
	return b.setup(ctx, "install_locale", locale, nil)
}

func (b *MeetingAudioBridge) setup(ctx context.Context, kind, locale string, tracks []string) error {
	if ctx == nil {
		return ErrMeetingAudioInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	id := "setup-" + strconv.FormatUint(meetingAudioProbeSequence.Add(1), 10)
	session, err := b.open(ctx, meetingAudioRequest{Version: 1, Kind: kind, SessionID: id, Locale: locale, Tracks: tracks, ExplicitSetup: true})
	if err != nil {
		return err
	}
	defer session.cancel()
	events, err := session.Poll(ctx)
	if err != nil {
		return err
	}
	if len(events) != 1 || events[0].Kind != "operation" {
		return ErrMeetingAudioInvalid
	}
	if events[0].State != "completed" {
		return MeetingAudioOperationError{Code: events[0].Error}
	}
	return nil
}

// Probe only reads capability, already-installed locales and permission status.
// It never requests access, downloads assets, constructs or runs a transcriber.
func (b *MeetingAudioBridge) Probe(ctx context.Context, locale string) (MeetingAudioAvailability, error) {
	id := "probe-" + strconv.FormatUint(meetingAudioProbeSequence.Add(1), 10)
	session, err := b.open(ctx, meetingAudioRequest{Version: 1, Kind: "probe", SessionID: id, Locale: locale})
	if err != nil {
		return MeetingAudioAvailability{}, err
	}
	defer session.cancel()
	events, err := session.Poll(ctx)
	if err != nil {
		return MeetingAudioAvailability{}, err
	}
	if len(events) != 1 || events[0].Kind != "availability" || events[0].Availability == nil {
		return MeetingAudioAvailability{}, ErrMeetingAudioInvalid
	}
	return *events[0].Availability, nil
}

// Start accepts an explicit request. It returns a starting session; recording is
// confirmed only by a later running state event. No track may silently downgrade.
func (b *MeetingAudioBridge) Start(ctx context.Context, configuration MeetingAudioStart) (*MeetingAudioSession, error) {
	if configuration.StartupTimeout == 0 {
		configuration.StartupTimeout = 8 * time.Second
	}
	if !meetingAudioID(configuration.SessionID) || !meetingAudioLocale(configuration.Locale) || len(configuration.Tracks) == 0 || len(configuration.Tracks) > 2 || configuration.StartupTimeout < 100*time.Millisecond || configuration.StartupTimeout > 8*time.Second {
		return nil, ErrMeetingAudioInvalid
	}
	tracks := make(map[string]bool, len(configuration.Tracks))
	for _, track := range configuration.Tracks {
		if tracks[track] || (track != "microphone" && track != "system") {
			return nil, ErrMeetingAudioInvalid
		}
		tracks[track] = true
	}
	return b.open(ctx, meetingAudioRequest{Version: 1, Kind: "start", SessionID: configuration.SessionID, Locale: configuration.Locale, Tracks: append([]string(nil), configuration.Tracks...), ExplicitStart: true, StartupMS: int(configuration.StartupTimeout / time.Millisecond)})
}

func (b *MeetingAudioBridge) open(ctx context.Context, request meetingAudioRequest) (*MeetingAudioSession, error) {
	if ctx == nil || !meetingAudioID(request.SessionID) || !meetingAudioLocale(request.Locale) {
		return nil, ErrMeetingAudioInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if b == nil {
		return nil, ErrMeetingAudioUnavailable
	}
	data, err := json.Marshal(request)
	if err != nil || len(data) == 0 || len(data) > 4096 {
		return nil, ErrMeetingAudioInvalid
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, ErrMeetingAudioUnavailable
	}
	handle := b.api.open(&data[0], int32(len(data)))
	runtime.KeepAlive(data)
	if handle == 0 {
		return nil, ErrMeetingAudioUnavailable
	}
	session := &MeetingAudioSession{bridge: b, handle: handle, id: request.SessionID, pollGate: make(chan struct{}, 1)}
	b.sessions[handle] = session
	return session, nil
}

// Poll waits for one bounded event batch. Cancelling this wait does not stop a
// meeting; explicit Control(stop) or Close owns the recording lifecycle.
func (s *MeetingAudioSession) Poll(ctx context.Context) ([]MeetingAudioEvent, error) {
	if ctx == nil {
		return nil, ErrMeetingAudioInvalid
	}
	if s == nil {
		return nil, ErrMeetingAudioUnavailable
	}
	select {
	case s.pollGate <- struct{}{}:
		defer func() { <-s.pollGate }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.closed.Load() {
		return nil, ErrMeetingAudioUnavailable
	}
	s.pendingMu.Lock()
	pending := s.pending
	s.pending = nil
	s.pendingMu.Unlock()
	if len(pending) > 0 {
		return decodeMeetingAudioBatch(pending, s.id)
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if s.closed.Load() {
			return nil, ErrMeetingAudioUnavailable
		}
		size := s.bridge.api.poll(s.handle, nil, 0)
		if size < 0 || size > 1048576 {
			return nil, ErrMeetingAudioInvalid
		}
		if size > 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			data := make([]byte, int(size))
			if s.bridge.api.poll(s.handle, &data[0], size) != size {
				return nil, ErrMeetingAudioInvalid
			}
			runtime.KeepAlive(data)
			if err := ctx.Err(); err != nil {
				s.pendingMu.Lock()
				if !s.closed.Load() {
					s.pending = data
				}
				s.pendingMu.Unlock()
				return nil, err
			}
			if s.closed.Load() {
				return nil, ErrMeetingAudioUnavailable
			}
			return decodeMeetingAudioBatch(data, s.id)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

// Control acknowledges admission only. The caller must consume events through
// paused/completed before presenting those states or discarding final batches.
func (s *MeetingAudioSession) Control(operation string, expectedEpoch uint64) error {
	if s == nil || s.closed.Load() {
		return ErrMeetingAudioUnavailable
	}
	if expectedEpoch == 0 || (operation != "pause" && operation != "resume" && operation != "stop") {
		return ErrMeetingAudioInvalid
	}
	request := struct {
		Version   int    `json:"version"`
		SessionID string `json:"session_id"`
		Epoch     uint64 `json:"epoch"`
		Operation string `json:"operation"`
	}{1, s.id, expectedEpoch, operation}
	data, err := json.Marshal(request)
	if err != nil || len(data) == 0 || len(data) > 512 {
		return ErrMeetingAudioInvalid
	}
	result := s.bridge.api.control(s.handle, &data[0], int32(len(data)))
	runtime.KeepAlive(data)
	if result != 1 {
		return ErrMeetingAudioControl
	}
	return nil
}

// Close discards unconsumed events and cancels the session. Native admission
// remains occupied until its actual stream and analyzers finish. The dylib must
// remain loaded because native shutdown/callbacks can outlive this Go wrapper.
func (s *MeetingAudioSession) Close() error { s.cancel(); return nil }

func (s *MeetingAudioSession) cancel() {
	if s == nil || s.closed.Swap(true) {
		return
	}
	s.pendingMu.Lock()
	s.pending = nil
	s.pendingMu.Unlock()
	s.bridge.api.cancel(s.handle)
	s.bridge.mu.Lock()
	delete(s.bridge.sessions, s.handle)
	s.bridge.mu.Unlock()
}

func decodeMeetingAudioBatch(data []byte, id string) ([]MeetingAudioEvent, error) {
	if len(data) == 0 || len(data) > 1048576 || !utf8.Valid(data) {
		return nil, ErrMeetingAudioInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var batch meetingAudioBatch
	if err := decoder.Decode(&batch); err != nil {
		return nil, ErrMeetingAudioInvalid
	}
	var tail any
	if decoder.Decode(&tail) != io.EOF || batch.Version != 1 || batch.SessionID != id || len(batch.Events) == 0 || len(batch.Events) > 16 {
		return nil, ErrMeetingAudioInvalid
	}
	for _, event := range batch.Events {
		if event.Version != 1 || event.SessionID != id {
			return nil, ErrMeetingAudioInvalid
		}
		switch event.Kind {
		case "operation":
			if event.Epoch != 0 || !meetingAudioNoTranscript(event) || (event.State != "completed" && event.State != "failed") || (event.State == "completed" && event.Error != "") || (event.State == "failed" && !meetingAudioError(event.Error)) {
				return nil, ErrMeetingAudioInvalid
			}
		case "availability":
			if event.Epoch != 0 || event.Level != nil || event.Availability == nil || event.State != "" || event.Error != "" || event.Text != "" || event.Track != "" || event.SegmentID != "" || event.Revision != 0 || event.StartMS != nil || event.EndMS != nil || event.Final != nil || !validMeetingAudioAvailability(*event.Availability) {
				return nil, ErrMeetingAudioInvalid
			}
		case "state":
			if event.Epoch == 0 || !meetingAudioState(event.State) || event.Error != "" || !meetingAudioNoTranscript(event) {
				return nil, ErrMeetingAudioInvalid
			}
		case "error":
			if event.Epoch == 0 || event.State != "" || !meetingAudioError(event.Error) || !meetingAudioNoTranscript(event) {
				return nil, ErrMeetingAudioInvalid
			}
		case "level":
			if event.Epoch == 0 || (event.Track != "microphone" && event.Track != "system") || event.Level == nil || math.IsNaN(*event.Level) || math.IsInf(*event.Level, 0) || *event.Level < 0 || *event.Level > 1 || event.State != "" || event.Error != "" || event.Availability != nil || event.Text != "" || event.SegmentID != "" || event.Revision != 0 || event.StartMS != nil || event.EndMS != nil || event.Final != nil {
				return nil, ErrMeetingAudioInvalid
			}
		case "transcript", "preview":
			// Offsets become time.Duration nanoseconds in the meeting engine.
			// Bound that conversion, rather than imposing a session lifetime cap.
			if event.Epoch == 0 || event.Level != nil || event.State != "" || event.Error != "" || event.Availability != nil || (event.Track != "microphone" && event.Track != "system") || !meetingAudioID(event.SegmentID) || event.Revision == 0 || event.StartMS == nil || event.EndMS == nil || *event.StartMS < 0 || *event.EndMS < *event.StartMS || *event.EndMS > math.MaxInt64/int64(time.Millisecond) || event.Final == nil || strings.TrimSpace(event.Text) == "" || len(event.Text) > 16384 {
				return nil, ErrMeetingAudioInvalid
			}
			if *event.Final != (event.Kind == "transcript") {
				return nil, ErrMeetingAudioInvalid
			}
		default:
			return nil, ErrMeetingAudioInvalid
		}
	}
	return batch.Events, nil
}

func meetingAudioNoTranscript(event MeetingAudioEvent) bool {
	return event.Level == nil && event.Availability == nil && event.Text == "" && event.Track == "" && event.SegmentID == "" && event.Revision == 0 && event.StartMS == nil && event.EndMS == nil && event.Final == nil
}

func meetingAudioState(state string) bool {
	switch state {
	case "starting", "running", "pausing", "paused", "stopping", "completed":
		return true
	default:
		return false
	}
}

func meetingAudioError(code string) bool {
	switch code {
	case "invalid_request", "speech_unavailable", "assets_missing", "microphone_permission", "system_permission", "device_unavailable", "audio_format", "audio_clock", "audio_conversion", "audio_overflow", "audio_capture_overflow", "audio_analyzer_overflow", "audio_capture_stopped", "audio_analyzer_stopped", "transcript_limit", "transcription_failed", "device_changed", "device_failed", "device_stop_failed", "start_failed", "startup_timeout", "stop_failed", "stop_timeout", "event_overflow", "setup_unavailable", "setup_failed", "setup_timeout", "locale_unsupported", "assets_install_failed":
		return true
	default:
		return false
	}
}

func validMeetingAudioAvailability(value MeetingAudioAvailability) bool {
	switch value.MicrophonePermission {
	case "authorized", "denied", "restricted", "not_determined", "unknown":
	default:
		return false
	}
	if len(value.InstalledLocales) > 256 {
		return false
	}
	for _, locale := range value.InstalledLocales {
		if !meetingAudioLocale(locale) {
			return false
		}
	}
	return true
}

func meetingAudioID(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for _, c := range value {
		if (c < '0' || c > '9') && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

func meetingAudioLocale(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for _, c := range value {
		if (c < '0' || c > '9') && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && c != '-' {
			return false
		}
	}
	return true
}
