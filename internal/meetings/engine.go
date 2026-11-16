package meetings

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

type trackState struct {
	partial *Event
	lastEnd time.Duration
}

// Session serializes admission without allocating a worker goroutine or an
// internal event queue. Controls invalidate callback epochs before waiting for
// a previously admitted sink write to finish.
type Session struct {
	mu           sync.Mutex
	gate         chan struct{}
	id           string
	trackOrder   []string
	tracks       map[string]*trackState
	limits       Limits
	sink         Sink
	state        State
	epoch        uint64
	issue        Issue
	finals       []FinalSegment
	finalByKey   map[string]int
	finalBytes   int
	notes        []ManualNote
	noteByID     map[string]int
	manualBytes  int
	pending      *Record
	pendingState State
	transition   bool
}

// DefaultLimits is also the hard upper bound for configurable limits. A
// retention limit fails visibly; finalized keys are never evicted here.
func DefaultLimits() Limits {
	return Limits{MaxSegments: 32768, MaxEventBytes: 16 << 10, MaxTranscriptBytes: 16 << 20,
		MaxNotes: 256, MaxNoteBytes: 64 << 10, MaxManualBytes: 1 << 20, MaxDuration: 8 * time.Hour}
}

func normalizeLimits(limits Limits) (Limits, bool) {
	defaults := DefaultLimits()
	for _, pair := range [][2]*int{
		{&limits.MaxSegments, &defaults.MaxSegments}, {&limits.MaxEventBytes, &defaults.MaxEventBytes},
		{&limits.MaxTranscriptBytes, &defaults.MaxTranscriptBytes}, {&limits.MaxNotes, &defaults.MaxNotes},
		{&limits.MaxNoteBytes, &defaults.MaxNoteBytes}, {&limits.MaxManualBytes, &defaults.MaxManualBytes},
	} {
		if *pair[0] == 0 {
			*pair[0] = *pair[1]
		}
		if *pair[0] < 1 || *pair[0] > *pair[1] {
			return Limits{}, false
		}
	}
	if limits.MaxDuration == 0 {
		limits.MaxDuration = defaults.MaxDuration
	}
	return limits, limits.MaxDuration > 0 && limits.MaxDuration <= defaults.MaxDuration
}

func validID(id string, max int) bool {
	if len(id) == 0 || len(id) > max || id == "." || id == ".." {
		return false
	}
	for _, r := range id {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return true
}

func validText(text string) bool {
	return utf8.ValidString(text) && strings.TrimSpace(text) != "" && !strings.ContainsRune(text, 0)
}

func hasEncryptedCapability(sink Sink) bool {
	if sink == nil {
		return false
	}
	capabilities := sink.Capabilities()
	return capabilities.Encryption == AuthenticatedEncryption && capabilities.AtomicIdempotent
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrCanceled, err)
	}
	return nil
}

func New(ctx context.Context, config Config, sink Sink) (*Session, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	limits, ok := normalizeLimits(config.Limits)
	if !ok || !validID(config.SessionID, 64) || len(config.Tracks) < 1 || len(config.Tracks) > 8 {
		return nil, ErrInvalid
	}
	tracks := make(map[string]*trackState, len(config.Tracks))
	var order []string
	for _, id := range config.Tracks {
		if !validID(id, 32) || tracks[id] != nil {
			return nil, ErrInvalid
		}
		id = strings.Clone(id)
		tracks[id], order = &trackState{}, append(order, id)
	}
	if !hasEncryptedCapability(sink) {
		return nil, ErrEncryption
	}
	s := &Session{id: strings.Clone(config.SessionID), gate: make(chan struct{}, 1), tracks: tracks,
		trackOrder: order, limits: limits, sink: sink, state: Ready,
		finalByKey: map[string]int{}, noteByID: map[string]int{}}
	s.gate <- struct{}{}
	return s, nil
}

func (s *Session) failLocked(issue Issue) {
	s.state, s.issue = Failed, issue
	s.epoch++
}

func (s *Session) checkContext(ctx context.Context) error {
	if s == nil || s.gate == nil {
		return ErrState
	}
	err := contextError(ctx)
	if errors.Is(err, ErrCanceled) {
		s.mu.Lock()
		s.failLocked(IssueCanceled)
		s.mu.Unlock()
	}
	return err
}

func (s *Session) acquire(ctx context.Context) error {
	if err := s.checkContext(ctx); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return s.checkContext(ctx)
	case <-s.gate:
		if err := s.checkContext(ctx); err != nil {
			s.release()
			return err
		}
		return nil
	}
}

func (s *Session) release() { s.gate <- struct{}{} }

func (s *Session) endTransition() {
	s.mu.Lock()
	s.transition = false
	s.mu.Unlock()
}

func (s *Session) enterRunning(ctx context.Context, from State) (Token, error) {
	if s == nil || s.gate == nil {
		return Token{}, ErrState
	}
	s.mu.Lock()
	if s.transition || s.state != from || s.pending != nil {
		s.mu.Unlock()
		return Token{}, ErrState
	}
	s.transition = true
	epoch := s.epoch
	s.mu.Unlock()
	defer s.endTransition()
	if err := s.acquire(ctx); err != nil {
		return Token{}, err
	}
	defer s.release()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != from || s.epoch != epoch || s.pending != nil {
		return Token{}, ErrState
	}
	s.state, s.issue = Running, NoIssue
	s.epoch++
	return Token{SessionID: s.id, Epoch: s.epoch}, nil
}

func (s *Session) Start(ctx context.Context) (Token, error)  { return s.enterRunning(ctx, Ready) }
func (s *Session) Resume(ctx context.Context) (Token, error) { return s.enterRunning(ctx, Paused) }

func (s *Session) clearPartialsLocked() {
	for _, track := range s.tracks {
		track.partial = nil
	}
}

func issueError(issue Issue) error {
	switch issue {
	case IssueCanceled:
		return ErrCanceled
	case IssueConflict:
		return ErrConflict
	case IssueLimit:
		return ErrLimit
	default:
		return ErrSink
	}
}

func (s *Session) control(ctx context.Context, stopping bool) error {
	if s == nil || s.gate == nil {
		return ErrState
	}
	s.mu.Lock()
	if s.transition {
		s.mu.Unlock()
		return ErrState
	}
	if stopping && s.state == Stopped {
		s.mu.Unlock()
		return nil
	}
	if s.state != Running && s.state != Paused && (!stopping || s.state != Ready && s.state != Stopping) {
		s.mu.Unlock()
		return ErrState
	}
	if err := contextError(ctx); err != nil {
		if errors.Is(err, ErrCanceled) {
			s.failLocked(IssueCanceled)
		}
		s.mu.Unlock()
		return err
	}
	s.transition = true
	if stopping {
		s.state = Stopping
		if s.pending != nil {
			s.pendingState = Stopped
		}
	} else {
		s.state = Paused
		if s.pending != nil {
			s.pendingState = Paused
		}
	}
	s.epoch++
	s.clearPartialsLocked()
	s.mu.Unlock()
	defer s.endTransition()
	if err := s.acquire(ctx); err != nil {
		return err
	}
	defer s.release()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == Failed {
		return issueError(s.issue)
	}
	if stopping {
		s.state = Stopped
	}
	return nil
}

// Pause/Stop reject old-epoch events immediately, then drain the one admitted
// persistence call. The caller owns capture shutdown and must drain native final
// events before invoking Stop; the engine owns no device.
func (s *Session) Pause(ctx context.Context) error { return s.control(ctx, false) }
func (s *Session) Stop(ctx context.Context) error  { return s.control(ctx, true) }

func (s *Session) checkTokenLocked(token Token) error {
	if token.SessionID != s.id || token.Epoch != s.epoch {
		return ErrEpoch
	}
	if s.state != Running {
		return ErrState
	}
	return nil
}

// Callback authority is checked atomically before cancellation can fail the
// session. An old callback's canceled context does not own the current epoch.
func (s *Session) checkInputContext(ctx context.Context, token Token) error {
	if s == nil || s.gate == nil {
		return ErrState
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkTokenLocked(token); err != nil {
		return err
	}
	err := contextError(ctx)
	if errors.Is(err, ErrCanceled) {
		s.failLocked(IssueCanceled)
	}
	return err
}

func (s *Session) acquireInput(ctx context.Context, token Token) error {
	if err := s.checkInputContext(ctx, token); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return s.checkInputContext(ctx, token)
	case <-s.gate:
		if err := s.checkInputContext(ctx, token); err != nil {
			s.release()
			return err
		}
		return nil
	}
}

func cloneEvent(e Event) Event {
	e.SessionID, e.TrackID, e.SegmentID, e.Text = strings.Clone(e.SessionID), strings.Clone(e.TrackID), strings.Clone(e.SegmentID), strings.Clone(e.Text)
	return e
}

func (s *Session) transcriptUsageLocked() (bytes, partials int) {
	bytes = s.finalBytes
	for _, track := range s.tracks {
		if track.partial != nil {
			bytes += len(track.partial.Text)
			partials++
		}
	}
	return bytes, partials
}

func (s *Session) validateEventLocked(e Event) error {
	if e.Kind != Partial && e.Kind != Final || e.SessionID != s.id || !validID(e.TrackID, 32) || s.tracks[e.TrackID] == nil || !validID(e.SegmentID, 64) || e.Revision == 0 || e.Start < 0 || e.End < e.Start {
		return ErrInvalid
	}
	if len(e.Text) > s.limits.MaxEventBytes || e.End > s.limits.MaxDuration {
		return ErrLimit
	}
	if !validText(e.Text) {
		return ErrInvalid
	}
	return nil
}

func (s *Session) Submit(ctx context.Context, token Token, e Event) error {
	// Do not wait on a slow sink just to reject an invalidated callback.
	if err := s.acquireInput(ctx, token); err != nil {
		return err
	}
	defer s.release()
	s.mu.Lock()
	if err := s.checkTokenLocked(token); err != nil {
		s.mu.Unlock()
		return err
	}
	if err := s.validateEventLocked(e); err != nil {
		if errors.Is(err, ErrLimit) {
			s.failLocked(IssueLimit)
		}
		s.mu.Unlock()
		return err
	}
	key := s.id + "/transcript/" + e.TrackID + "/" + e.SegmentID
	if index, found := s.finalByKey[key]; found {
		if s.finals[index].Event == e {
			s.mu.Unlock()
			return nil
		}
		if e.Kind == Partial {
			s.mu.Unlock()
			return ErrOrder
		}
		s.failLocked(IssueConflict)
		s.mu.Unlock()
		return ErrConflict
	}
	track := s.tracks[e.TrackID]
	usage, partials := s.transcriptUsageLocked()
	if previous := track.partial; previous != nil {
		if previous.SegmentID != e.SegmentID || e.Start != previous.Start || e.End < previous.End || e.Revision < previous.Revision {
			s.mu.Unlock()
			return ErrOrder
		}
		if e.Revision == previous.Revision {
			finalized := *previous
			finalized.Kind = e.Kind
			if finalized != e {
				s.mu.Unlock()
				return ErrOrder
			}
		}
		usage -= len(previous.Text)
	} else {
		if e.Start < track.lastEnd {
			s.mu.Unlock()
			return ErrOrder
		}
		if len(s.finals)+partials >= s.limits.MaxSegments {
			s.failLocked(IssueLimit)
			s.mu.Unlock()
			return ErrLimit
		}
	}
	if usage+len(e.Text) > s.limits.MaxTranscriptBytes {
		s.failLocked(IssueLimit)
		s.mu.Unlock()
		return ErrLimit
	}
	e = cloneEvent(e)
	if e.Kind == Partial {
		track.partial = &e
		s.mu.Unlock()
		return nil
	}
	record := Record{Key: key, Kind: TranscriptFinal, SessionID: s.id, Event: e}
	track.partial = nil
	s.pending = &record
	s.pendingState = Paused
	s.mu.Unlock()
	return s.appendRecord(ctx, record)
}

func (s *Session) AddNote(ctx context.Context, note ManualNoteInput) error {
	if err := s.acquire(ctx); err != nil {
		return err
	}
	defer s.release()
	s.mu.Lock()
	if s.state == Failed || s.state == Stopping {
		s.mu.Unlock()
		return ErrState
	}
	if !validID(note.ID, 64) || !validManualNoteTime(note) {
		s.mu.Unlock()
		return ErrInvalid
	}
	if len(note.Text) > s.limits.MaxNoteBytes {
		s.failLocked(IssueLimit)
		s.mu.Unlock()
		return ErrLimit
	}
	if !validText(note.Text) {
		s.mu.Unlock()
		return ErrInvalid
	}
	if index, found := s.noteByID[note.ID]; found {
		if sameManualNote(s.notes[index].ManualNoteInput, note) {
			s.mu.Unlock()
			return nil
		}
		s.failLocked(IssueConflict)
		s.mu.Unlock()
		return ErrConflict
	}
	if len(s.notes) >= s.limits.MaxNotes || s.manualBytes+len(note.Text) > s.limits.MaxManualBytes {
		s.failLocked(IssueLimit)
		s.mu.Unlock()
		return ErrLimit
	}
	note.ID, note.Text = strings.Clone(note.ID), strings.Clone(note.Text)
	record := Record{Key: s.id + "/note/" + note.ID, Kind: UserNote, SessionID: s.id, Note: note}
	s.pending = &record
	s.pendingState = s.state
	if s.pendingState == Running {
		s.pendingState = Paused
	}
	s.mu.Unlock()
	return s.appendRecord(ctx, record)
}

func validReceipt(receipt Receipt) bool {
	if len(receipt.SourceID) == 0 || len(receipt.SourceID) > 256 || !utf8.ValidString(receipt.SourceID) {
		return false
	}
	for _, r := range receipt.SourceID {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

func (s *Session) appendFailure(issue Issue, err error) error {
	s.mu.Lock()
	s.failLocked(issue)
	s.mu.Unlock()
	return err
}

func (s *Session) appendRecord(ctx context.Context, record Record) error {
	if err := s.checkContext(ctx); err != nil {
		return err
	}
	if !hasEncryptedCapability(s.sink) {
		return s.appendFailure(IssueSink, ErrEncryption)
	}
	receipt, err := s.sink.Append(ctx, record)
	if ctxErr := s.checkContext(ctx); ctxErr != nil {
		return ctxErr
	}
	if errors.Is(err, context.Canceled) {
		return s.appendFailure(IssueCanceled, errors.Join(ErrCanceled, context.Canceled))
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return s.appendFailure(IssueCanceled, errors.Join(ErrCanceled, context.DeadlineExceeded))
	}
	if errors.Is(err, ErrConflict) {
		return s.appendFailure(IssueConflict, ErrConflict)
	}
	if err != nil || !validReceipt(receipt) {
		// Adapter errors may contain source content. Do not format or wrap
		// them into user/log-facing errors owned by this package.
		return s.appendFailure(IssueSink, ErrSink)
	}
	receipt.SourceID = strings.Clone(receipt.SourceID)
	s.mu.Lock()
	defer s.mu.Unlock()
	switch record.Kind {
	case TranscriptFinal:
		s.finalByKey[record.Key] = len(s.finals)
		s.finals = append(s.finals, FinalSegment{Event: record.Event, SourceID: receipt.SourceID})
		s.finalBytes += len(record.Event.Text)
		track := s.tracks[record.Event.TrackID]
		track.lastEnd = record.Event.End
		track.partial = nil
	case UserNote:
		s.noteByID[record.Note.ID] = len(s.notes)
		s.notes = append(s.notes, ManualNote{ManualNoteInput: record.Note, SourceID: receipt.SourceID})
		s.manualBytes += len(record.Note.Text)
	}
	s.pending = nil
	return nil
}

// Retry reuses the pending record and stable key after an uncertain write.
// Running writes recover paused; Ready and Stopped note writes keep that state.
func (s *Session) Retry(ctx context.Context) error {
	if s == nil || s.gate == nil {
		return ErrState
	}
	s.mu.Lock()
	if s.transition || s.state != Failed || s.pending == nil {
		s.mu.Unlock()
		return ErrState
	}
	s.transition = true
	record := *s.pending
	restore := s.pendingState
	s.mu.Unlock()
	defer s.endTransition()
	if err := s.acquire(ctx); err != nil {
		return err
	}
	defer s.release()
	if err := s.appendRecord(ctx, record); err != nil {
		return err
	}
	s.mu.Lock()
	s.state, s.issue = restore, NoIssue
	s.epoch++
	s.mu.Unlock()
	return nil
}

func (s *Session) Snapshot() Snapshot {
	if s == nil || s.gate == nil {
		return Snapshot{State: Failed}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result := Snapshot{SessionID: s.id, State: s.state, Epoch: s.epoch, Issue: s.issue,
		Finals: append([]FinalSegment(nil), s.finals...), Notes: append([]ManualNote(nil), s.notes...)}
	for _, id := range s.trackOrder {
		if partial := s.tracks[id].partial; partial != nil {
			result.Partials = append(result.Partials, *partial)
		}
	}
	if s.pending != nil {
		pending := *s.pending
		result.Pending = &pending
	}
	return result
}
