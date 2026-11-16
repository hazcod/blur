package client

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hazcod/blur/internal/meetings"
	"github.com/hazcod/blur/internal/platform/darwin"
)

// These ports keep device access out of controller tests. Opening the library
// and probing capabilities neither starts audio nor requests permissions.
type meetingAudio interface {
	Probe(context.Context, string) (darwin.MeetingAudioAvailability, error)
	RequestAccess(context.Context, []string) error
	InstallLocale(context.Context, string) error
	StartMeeting(context.Context, darwin.MeetingAudioStart) (meetingAudioStream, error)
	Close() error
}
type meetingAudioStream interface {
	Poll(context.Context) ([]darwin.MeetingAudioEvent, error)
	Control(string, uint64) error
	Close() error
}
type nativeMeetingAudio struct{ *darwin.MeetingAudioBridge }

func (b nativeMeetingAudio) StartMeeting(ctx context.Context, c darwin.MeetingAudioStart) (meetingAudioStream, error) {
	return b.Start(ctx, c)
}

func openMeetingAudio() (meetingAudio, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, darwin.ErrMeetingAudioUnavailable
	}
	bridge, err := darwin.OpenMeetingAudioBridge(filepath.Join(filepath.Dir(executable), "..", "Frameworks", "libBlurMeetingAudio.dylib"))
	if err != nil {
		return nil, err
	}
	return nativeMeetingAudio{bridge}, nil
}

func meetingID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}

type MeetingLiveView struct {
	Meeting          meetings.Meeting
	State            string
	Issue            string // closed, content-free code; never a raw native error
	Preview          map[string]string
	Tail             []meetings.StoredRecord
	Availability     darwin.MeetingAudioAvailability
	Levels           map[string]MeetingAudioLevel
	AudioRecording   bool
	RecordingElapsed time.Duration
	RecordingSince   time.Time
	LivePhrase       string
}

// RecordingLength counts only intervals acknowledged as recording by the
// native driver. Pauses and setup/download time do not advance the clock.
func (v MeetingLiveView) RecordingLength(now time.Time) time.Duration {
	elapsed := max(time.Duration(0), v.RecordingElapsed)
	if v.AudioRecording && !v.RecordingSince.IsZero() && now.After(v.RecordingSince) {
		elapsed += now.Sub(v.RecordingSince)
	}
	return elapsed
}

func (v *MeetingLiveView) setAudioRecording(recording bool, now time.Time) {
	if v.AudioRecording == recording {
		return
	}
	if recording {
		v.RecordingSince = now
	} else {
		v.RecordingElapsed = v.RecordingLength(now)
		v.RecordingSince = time.Time{}
	}
	v.AudioRecording = recording
}

type MeetingAudioLevel struct {
	Value float64
	At    time.Time
}

func (v MeetingLiveView) AudioLevel(now time.Time) float64 {
	if v.State != "running" {
		return 0
	}
	level := 0.0
	for _, sample := range v.Levels {
		if !sample.At.After(now) && now.Sub(sample.At) <= 500*time.Millisecond {
			level = max(level, sample.Value)
		}
	}
	return level
}

type activeMeeting struct {
	metadata    meetings.Meeting
	engine      *meetings.Session
	token       meetings.Token
	stream      meetingAudioStream
	nativeEpoch uint64
	cancel      context.CancelFunc
	cursor      uint64
}

// meetingController serializes durable writes and native acknowledgements.
// Invalidate cancels before waiting on gate, then acts as a synchronous write
// barrier for source deletion and retention. It never calls back into Client.
type meetingController struct {
	gate        sync.Mutex
	mu          sync.Mutex
	repo        meetings.Repository
	open        func() (meetingAudio, error)
	audio       meetingAudio
	active      *activeMeeting
	view        MeetingLiveView
	generation  uint64
	closed      bool
	setupCancel context.CancelFunc
	changes     chan struct{}
	wg          sync.WaitGroup
	now         func() time.Time
	onSource    func(string)
	writable    func() bool
}

func newMeetingController(repo meetings.Repository, open func() (meetingAudio, error)) *meetingController {
	return &meetingController{repo: repo, open: open, changes: make(chan struct{}, 1), now: time.Now}
}

func (m *meetingController) notify() {
	select {
	case m.changes <- struct{}{}:
	default:
	}
}

func (m *meetingController) snapshot() MeetingLiveView {
	if m == nil {
		return MeetingLiveView{Issue: "storage_unavailable"}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	v := m.view
	v.Meeting.Tracks = append([]string(nil), v.Meeting.Tracks...)
	v.Availability.InstalledLocales = append([]string(nil), v.Availability.InstalledLocales...)
	v.Preview = make(map[string]string, len(m.view.Preview))
	for track, text := range m.view.Preview {
		v.Preview[track] = text
	}
	v.Tail = append([]meetings.StoredRecord(nil), v.Tail...)
	v.Levels = make(map[string]MeetingAudioLevel, len(m.view.Levels))
	for track, sample := range m.view.Levels {
		v.Levels[track] = sample
	}
	return v
}

func (m *meetingController) ensureAudio() error {
	if m.audio == nil {
		b, err := m.open()
		if err != nil {
			return err
		}
		m.audio = b
	}
	return nil
}

func (m *meetingController) probe(ctx context.Context, locale string) error {
	m.gate.Lock()
	defer m.gate.Unlock()
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return ErrClientClosed
	}
	if err := m.ensureAudio(); err != nil {
		return err
	}
	a, err := m.audio.Probe(ctx, locale)
	if err == nil {
		m.mu.Lock()
		m.view.Availability = a
		m.mu.Unlock()
		m.notify()
	}
	return err
}

func (m *meetingController) installLocale(ctx context.Context, locale string) error {
	m.gate.Lock()
	defer m.gate.Unlock()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrClientClosed
	}
	m.setupCancel = cancel
	m.mu.Unlock()
	defer func() { m.mu.Lock(); m.setupCancel = nil; m.mu.Unlock() }()
	if err := m.ensureAudio(); err != nil {
		return err
	}
	return m.audio.InstallLocale(ctx, locale)
}

func (m *meetingController) create(ctx context.Context, title, locale string, tracks []string) (*activeMeeting, error) {
	metadata := meetings.Meeting{ID: meetingID(), Title: strings.TrimSpace(title), Locale: strings.TrimSpace(locale), Tracks: append([]string(nil), tracks...), StartedAt: m.now().UTC(), State: meetings.Ready, Revision: 1}
	if metadata.Title == "" {
		metadata.Title = "Untitled note"
	}
	if err := metadata.Validate(); err != nil {
		return nil, err
	}
	engine, err := meetings.New(ctx, meetings.Config{SessionID: metadata.ID, Tracks: metadata.Tracks}, m.repo)
	if err != nil {
		return nil, err
	}
	if err := m.repo.PutMeeting(ctx, metadata, 0); err != nil {
		return nil, err
	}
	return &activeMeeting{metadata: metadata, engine: engine}, nil
}

func (m *meetingController) newNotes(ctx context.Context, title, locale string, tracks []string) (string, error) {
	m.gate.Lock()
	defer m.gate.Unlock()
	if m.writable != nil && !m.writable() {
		return "", ErrMemoryChanging
	}
	m.mu.Lock()
	blocked := m.closed || m.active != nil && m.view.State != "completed" && m.view.State != "failed" && m.view.State != "ready"
	generation := m.generation
	m.mu.Unlock()
	if blocked {
		return "", meetings.ErrState
	}
	// A typed note needs no enabled audio input. Keep a potential input for a
	// later explicit Start without weakening the recorded-track invariants.
	if len(tracks) == 0 {
		tracks = []string{"microphone"}
	}
	a, err := m.create(ctx, title, locale, tracks)
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	if m.closed || m.generation != generation {
		m.mu.Unlock()
		return "", ErrMemoryChanging
	}
	m.active = a
	m.view = MeetingLiveView{Meeting: a.metadata, State: "ready", Preview: map[string]string{}}
	m.mu.Unlock()
	m.notify()
	return a.metadata.ID, nil
}

// restoreReadyAgenda never restores a recording engine or loads a recorded
// transcript. Ready agendas contain bounded manual notes only. Paging retains
// only the display tail and the repository's opaque consumed cursor, including
// erased slots. An excessive deletion history fails explicitly at the scan
// bound rather than silently starting with an incomplete tail/cursor.
func (m *meetingController) restoreReadyAgenda(ctx context.Context, id string) (*activeMeeting, []meetings.StoredRecord, error) {
	metadata, found, err := m.repo.GetMeeting(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if !found {
		return nil, nil, meetings.ErrNotFound
	}
	if err := metadata.Validate(); err != nil {
		return nil, nil, err
	}
	if metadata.State != meetings.Ready || !metadata.RecordingStartedAt.IsZero() || !metadata.EndedAt.IsZero() {
		return nil, nil, meetings.ErrState
	}
	limits := meetings.DefaultLimits()
	if metadata.RecordCount > uint64(limits.MaxNotes) {
		return nil, nil, meetings.ErrLimit
	}
	active := &activeMeeting{metadata: metadata}
	var tail []meetings.StoredRecord
	notes, bytes := 0, 0
	for pass := 0; pass <= limits.MaxNotes; pass++ {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		page, err := m.repo.MeetingRecords(ctx, id, meetings.PageRequest{After: active.cursor, Limit: 20, MaxBytes: 128 << 10})
		if err != nil {
			return nil, nil, err
		}
		if page.Next < active.cursor || page.More && page.Next == active.cursor {
			return nil, nil, meetings.ErrInvalid
		}
		sequence := active.cursor
		for _, row := range page.Records {
			if row.Record.Kind != meetings.UserNote || row.Record.SessionID != id || row.Sequence <= sequence || row.Sequence > page.Next {
				return nil, nil, meetings.ErrState
			}
			sequence = row.Sequence
			notes++
			bytes += len(row.Record.Note.Text)
			if notes > limits.MaxNotes || bytes > limits.MaxManualBytes {
				return nil, nil, meetings.ErrLimit
			}
			tail = append(tail, row)
			for len(tail) > 20 || meetingTailBytes(tail) > 128<<10 {
				tail = tail[1:]
			}
		}
		active.cursor = page.Next
		if !page.More {
			if uint64(notes) != metadata.RecordCount {
				return nil, nil, meetings.ErrState
			}
			return active, tail, nil
		}
	}
	return nil, nil, meetings.ErrLimit
}

func (m *meetingController) start(parent context.Context, title, locale string, tracks []string, selectedID ...string) error {
	m.gate.Lock()
	defer m.gate.Unlock()
	if len(selectedID) > 1 {
		return meetings.ErrInvalid
	}
	if m.writable != nil && !m.writable() {
		return ErrMemoryChanging
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrClientClosed
	}
	if m.active != nil && m.view.State != "completed" && m.view.State != "failed" && m.view.State != "ready" && m.view.State != "" {
		m.mu.Unlock()
		return meetings.ErrState
	}
	generation := m.generation
	active := m.active
	var agendaTail []meetings.StoredRecord
	if active != nil && active.metadata.State == meetings.Ready {
		copyOfReady := *active
		active = &copyOfReady
		locale, tracks = active.metadata.Locale, active.metadata.Tracks
		agendaTail = append([]meetings.StoredRecord(nil), m.view.Tail...)
	}
	m.setupCancel = cancel
	m.mu.Unlock()
	defer func() { m.mu.Lock(); m.setupCancel = nil; m.mu.Unlock() }()
	if len(selectedID) == 1 {
		var err error
		active, agendaTail, err = m.restoreReadyAgenda(ctx, selectedID[0])
		if err != nil {
			return err
		}
		locale, tracks = active.metadata.Locale, active.metadata.Tracks
		m.mu.Lock()
		if m.closed || m.generation != generation || ctx.Err() != nil {
			m.mu.Unlock()
			if err := ctx.Err(); err != nil {
				return err
			}
			return ErrMemoryChanging
		}
		m.active = active
		m.view = MeetingLiveView{Meeting: active.metadata, State: "ready", Preview: map[string]string{}, Tail: agendaTail}
		m.mu.Unlock()
		m.notify()
		// Invalidation may inspect the published Ready selection while native
		// setup is in progress. Build the attempt on a separate value, as with
		// an already-live Ready agenda, until admission is complete.
		readyCopy := *active
		active = &readyCopy
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := m.ensureAudio(); err != nil {
		return err
	}
	a, err := m.audio.Probe(ctx, locale)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	if m.closed || m.generation != generation {
		m.mu.Unlock()
		return ErrMemoryChanging
	}
	m.view.Availability = a
	m.mu.Unlock()
	if !a.SpeechAvailable {
		return darwin.MeetingAudioOperationError{Code: "speech_unavailable"}
	}
	if !a.RequestedLocaleInstalled {
		return darwin.MeetingAudioOperationError{Code: "assets_missing"}
	}
	needAccess := false
	for _, track := range tracks {
		if track == "microphone" && a.MicrophonePermission != "authorized" || track == "system" && !a.SystemPermission {
			needAccess = true
		}
	}
	if needAccess {
		if err := m.audio.RequestAccess(ctx, tracks); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if active == nil || active.metadata.State != meetings.Ready {
		active, err = m.create(ctx, title, locale, tracks)
		if err != nil {
			return err
		}
	} else {
		// A typed agenda may precede recording. Reuse its exact meeting and
		// frozen track/language selection, preserving source time offsets.
		locale, tracks = active.metadata.Locale, active.metadata.Tracks
	}
	previousOrigin := active.metadata.RecordingStartedAt
	active.metadata.RecordingStartedAt = m.now().UTC()
	// Admission can be rejected before the native starting acknowledgement.
	// Keep the saved agenda retryable; only that acknowledgement publishes the
	// recording anchor. No accepted transcript exists before it.
	active.engine, err = meetings.New(ctx, meetings.Config{SessionID: active.metadata.ID, Tracks: tracks}, m.repo)
	if err != nil {
		return err
	}
	active.token, err = active.engine.Start(ctx)
	if err != nil {
		return err
	}
	active.stream, err = m.audio.StartMeeting(ctx, darwin.MeetingAudioStart{SessionID: active.metadata.ID, Locale: locale, Tracks: tracks})
	if err != nil {
		active.metadata.RecordingStartedAt = previousOrigin
		m.mu.Lock()
		if !m.closed && m.generation == generation {
			m.active = active
			m.view = MeetingLiveView{Meeting: active.metadata, State: "ready", Preview: map[string]string{}, Tail: agendaTail, Availability: a}
		}
		m.mu.Unlock()
		return err
	}
	runCtx, runCancel := context.WithCancel(context.Background())
	active.cancel = runCancel
	active.nativeEpoch = 1
	m.mu.Lock()
	if m.closed || generation != m.generation {
		m.mu.Unlock()
		runCancel()
		_ = active.stream.Close()
		return ErrMemoryChanging
	}
	m.active = active
	m.view = MeetingLiveView{Meeting: active.metadata, State: "starting", Preview: map[string]string{}, Tail: agendaTail, Availability: a}
	m.wg.Add(1)
	m.mu.Unlock()
	m.notify()
	go m.poll(runCtx, active, generation)
	return nil
}

func (m *meetingController) persistState(ctx context.Context, a *activeMeeting) error {
	old := a.metadata.Revision
	a.metadata.Revision++
	if err := m.repo.PutMeeting(ctx, a.metadata, old); err != nil {
		a.metadata.Revision = old
		return err
	}
	return nil
}

func (m *meetingController) current(a *activeMeeting, generation uint64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return !m.closed && m.active == a && m.generation == generation
}

func (m *meetingController) poll(ctx context.Context, a *activeMeeting, generation uint64) {
	defer m.wg.Done()
	defer func() { _ = a.stream.Close() }()
	for {
		events, err := a.stream.Poll(ctx)
		m.gate.Lock()
		if !m.current(a, generation) || ctx.Err() != nil {
			m.gate.Unlock()
			return
		}
		finished := false
		if err != nil {
			m.fail(ctx, a, "audio_interrupted", err)
			finished = true
		} else {
			for _, event := range events {
				if event.SessionID != a.metadata.ID {
					m.fail(ctx, a, "invalid_event")
					finished = true
					break
				}
				finished, err = m.consume(ctx, a, event)
				if err != nil {
					m.fail(ctx, a, "save_failed", err)
					finished = true
				}
				if finished {
					break
				}
			}
		}
		m.gate.Unlock()
		m.notify()
		if finished {
			return
		}
	}
}

func (m *meetingController) consume(ctx context.Context, a *activeMeeting, e darwin.MeetingAudioEvent) (bool, error) {
	m.mu.Lock()
	admitted := m.active == a && !m.closed && ctx.Err() == nil
	m.mu.Unlock()
	if !admitted {
		return true, nil
	}
	if e.Kind == "error" {
		m.fail(ctx, a, e.Error)
		return true, nil
	}
	if e.Kind == "state" {
		if e.Epoch < a.nativeEpoch {
			return false, nil
		}
		a.nativeEpoch = e.Epoch
		switch e.State {
		case "running":
			a.metadata.State = meetings.Running
		case "pausing":
			if err := a.engine.Pause(ctx); err != nil {
				return true, err
			}
		case "paused":
			a.metadata.State = meetings.Paused
		case "starting":
			if a.metadata.State == meetings.Paused {
				var err error
				a.token, err = a.engine.Resume(ctx)
				if err != nil {
					return true, err
				}
			}
			// The native driver can emit final words while its start call is
			// still awaiting completion. This explicit acknowledgement admits
			// evidence; the UI remains "starting" until the running event.
			a.metadata.State = meetings.Running
		case "completed":
			// Native completion follows all final batches; only now seal the
			// durable engine. Stopping it at the button would drop final words.
			if err := a.engine.Stop(ctx); err != nil {
				return true, err
			}
			a.metadata.State, a.metadata.EndedAt = meetings.Stopped, m.now().UTC()
		case "error", "cancelled":
			m.fail(ctx, a, "audio_interrupted")
			return true, nil
		}
		if err := m.persistState(ctx, a); err != nil {
			return true, err
		}
		m.mu.Lock()
		if m.active != a || ctx.Err() != nil {
			m.mu.Unlock()
			return true, nil
		}
		m.view.Meeting, m.view.State = a.metadata, e.State
		switch e.State {
		case "running":
			m.view.setAudioRecording(true, m.now())
		case "paused", "completed", "starting":
			m.view.setAudioRecording(false, m.now())
		}
		if e.State != "running" {
			m.view.Preview = map[string]string{}
			m.view.LivePhrase = ""
			m.view.Levels = nil
		}
		m.mu.Unlock()
		logMeetingState(e.State)
		if e.State == "completed" {
			a.cancel()
			return true, nil
		}
		return false, nil
	}
	if e.Epoch != a.nativeEpoch {
		return false, nil
	}
	if e.Kind == "level" && e.Level != nil {
		m.mu.Lock()
		if m.view.State == "running" {
			if m.view.Levels == nil {
				m.view.Levels = map[string]MeetingAudioLevel{}
			}
			m.view.Levels[e.Track] = MeetingAudioLevel{Value: *e.Level, At: m.now()}
		}
		m.mu.Unlock()
		return false, nil
	}
	if e.StartMS == nil || e.EndMS == nil {
		return false, nil
	}
	if e.Kind == "preview" {
		// SpeechAnalyzer previews have moving ranges, unlike engine partials.
		// They are volatile display text, never evidence or model input.
		m.mu.Lock()
		if m.active != a || ctx.Err() != nil {
			m.mu.Unlock()
			return true, nil
		}
		if m.view.Preview == nil {
			m.view.Preview = map[string]string{}
		}
		m.view.Preview[e.Track] = e.Text
		m.view.LivePhrase = e.Text
		m.mu.Unlock()
		return false, nil
	}
	if e.Kind == "transcript" && e.Final != nil && *e.Final {
		err := a.engine.Submit(ctx, a.token, meetings.Event{Kind: meetings.Final, SessionID: a.metadata.ID, TrackID: e.Track, SegmentID: e.SegmentID, Revision: e.Revision, Start: time.Duration(*e.StartMS) * time.Millisecond, End: time.Duration(*e.EndMS) * time.Millisecond, Text: e.Text})
		if err != nil {
			return true, err
		}
		m.mu.Lock()
		if m.active != a || ctx.Err() != nil {
			m.mu.Unlock()
			return true, nil
		}
		m.view.LivePhrase = e.Text
		m.mu.Unlock()
		return false, m.refreshTail(ctx, a)
	}
	return false, nil
}

func (m *meetingController) refreshTail(ctx context.Context, a *activeMeeting) error {
	page, err := m.repo.MeetingRecords(ctx, a.metadata.ID, meetings.PageRequest{After: a.cursor, Limit: 16, MaxBytes: 128 << 10})
	if err != nil {
		return err
	}
	a.cursor = page.Next
	m.mu.Lock()
	if m.active != a {
		m.mu.Unlock()
		return nil
	}
	m.view.Tail = append(m.view.Tail, page.Records...)
	for len(m.view.Tail) > 20 || meetingTailBytes(m.view.Tail) > 128<<10 {
		m.view.Tail = m.view.Tail[1:]
	}
	m.mu.Unlock()
	for _, row := range page.Records {
		if m.onSource != nil {
			m.onSource(row.SourceID)
		}
	}
	return nil
}

func meetingTailBytes(rows []meetings.StoredRecord) int {
	n := 0
	for _, row := range rows {
		n += len(row.Record.Event.Text) + len(row.Record.Note.Text)
	}
	return n
}

func (m *meetingController) fail(ctx context.Context, a *activeMeeting, code string, causes ...error) {
	var cause error
	if len(causes) > 0 {
		cause = causes[0]
	}
	logMeetingFailure(slog.Default(), code, cause)
	a.metadata.State, a.metadata.EndedAt = meetings.Failed, m.now().UTC()
	_ = m.persistState(ctx, a)
	m.mu.Lock()
	if m.active == a {
		m.view.Meeting, m.view.State, m.view.Issue = a.metadata, "failed", code
		m.view.setAudioRecording(false, m.now())
		m.view.Preview = map[string]string{}
		m.view.Levels = nil
	}
	m.mu.Unlock()
	if a.cancel != nil {
		a.cancel()
	}
}

func (m *meetingController) control(ctx context.Context, operation string) error {
	m.gate.Lock()
	defer m.gate.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	a, closed := m.active, m.closed
	m.mu.Unlock()
	if closed {
		return ErrClientClosed
	}
	if a == nil || a.stream == nil {
		return meetings.ErrState
	}
	if err := a.stream.Control(operation, a.nativeEpoch); err != nil {
		return err
	}
	return nil
}

func (m *meetingController) addNote(ctx context.Context, id string, note meetings.ManualNoteInput) error {
	m.gate.Lock()
	defer m.gate.Unlock()
	if m.writable != nil && !m.writable() {
		return ErrMemoryChanging
	}
	_, ok, err := m.repo.GetMeeting(ctx, id)
	if err != nil || !ok {
		return errors.Join(meetings.ErrNotFound, err)
	}
	m.mu.Lock()
	a, closed := m.active, m.closed
	m.mu.Unlock()
	if closed {
		return ErrClientClosed
	}
	if a != nil && a.metadata.ID == id {
		// Manual persistence is independent of the transcription engine. A
		// failed note write must preserve its draft without poisoning running
		// speech admission or trapping the note in a failed engine state.
		if _, err := m.repo.Append(ctx, meetings.Record{Key: id + "/note/" + note.ID, SessionID: id, Kind: meetings.UserNote, Note: note}); err != nil {
			return err
		}
		return m.refreshTail(ctx, a)
	}
	receipt, err := m.repo.Append(ctx, meetings.Record{Key: id + "/note/" + note.ID, SessionID: id, Kind: meetings.UserNote, Note: note})
	if err == nil && m.onSource != nil {
		m.onSource(receipt.SourceID)
	}
	return err
}

func (m *meetingController) invalidate(closing bool) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.generation++
	m.closed = m.closed || closing
	if m.setupCancel != nil {
		m.setupCancel()
	}
	a := m.active
	if a != nil && a.cancel != nil {
		a.cancel()
	}
	m.active = nil
	m.view = MeetingLiveView{Issue: "memory_changed"}
	m.mu.Unlock()
	if a != nil && a.stream != nil {
		_ = a.stream.Close()
	}
	// All pre-invalidation writes have returned before deletion can proceed.
	m.gate.Lock()
	if a != nil && a.stream != nil && a.metadata.State != meetings.Stopped {
		a.metadata.State = meetings.Failed
		if a.metadata.EndedAt.IsZero() {
			a.metadata.EndedAt = m.now().UTC()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = m.persistState(ctx, a)
		cancel()
	}
	m.mu.Lock()
	m.view = MeetingLiveView{Issue: "memory_changed"}
	m.mu.Unlock()
	m.gate.Unlock()
	m.notify()
}

func (m *meetingController) close() {
	if m == nil {
		return
	}
	// Request graceful native drain, with a bounded deadline. If the device
	// fails to acknowledge, cancel and fence before encrypted storage closes.
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Second)
	if m.control(ctx, "stop") == nil {
		ticker := time.NewTicker(25 * time.Millisecond)
		defer ticker.Stop()
		for ctx.Err() == nil {
			v := m.snapshot()
			if v.State == "completed" || v.State == "failed" {
				break
			}
			select {
			case <-ctx.Done():
			case <-m.changes:
			case <-ticker.C:
			}
		}
	}
	cancel()
	m.invalidate(true)
	m.wg.Wait()
	m.gate.Lock()
	if m.audio != nil {
		_ = m.audio.Close()
	}
	m.gate.Unlock()
}
