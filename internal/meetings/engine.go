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
	mu               sync.Mutex
	gate             chan struct{}
	id               string
	trackOrder       []string
	tracks           map[string]*trackState
	limits           Limits
	sink             Sink
	lookup           RecordLookup
	finalCount       uint64
	transcriptBytes  uint64
	noteCount        uint64
	totalManualBytes uint64
	state            State
	epoch            uint64
	issue            Issue
	finals           []FinalSegment
	finalByKey       map[string]int
	finalBytes       int
	notes            []ManualNote
	noteByID         map[string]int
	manualBytes      int
	pending          *Record
	pendingLookup    bool
	pendingState     State
	transition       bool
}

// MaxCaptureDuration is the largest offset carried by time.Duration. Actual
// wall-clock provenance must also fit the canonical Unix-nanosecond timestamps.
const MaxCaptureDuration = time.Duration(1<<63 - 1)

const hotRows = 20
const hotBytes = 128 << 10

// DefaultLimits bounds legacy sinks without a durable identity index. New uses
// unlimited cumulative budgets for RecordLookup sinks unless the caller sets
// an explicit budget. Single-record and representation bounds always apply.
func DefaultLimits() Limits {
	return Limits{MaxSegments: 32768, MaxEventBytes: 16 << 10, MaxTranscriptBytes: 16 << 20,
		MaxNotes: 256, MaxNoteBytes: 64 << 10, MaxManualBytes: 1 << 20, MaxDuration: MaxCaptureDuration}
}

func normalizeLimits(limits Limits, durable bool) (Limits, bool) {
	defaults := DefaultLimits()
	for _, pair := range [][2]*int{
		{&limits.MaxEventBytes, &defaults.MaxEventBytes}, {&limits.MaxNoteBytes, &defaults.MaxNoteBytes},
	} {
		if *pair[0] == 0 {
			*pair[0] = *pair[1]
		}
		if *pair[0] < 1 || *pair[0] > *pair[1] {
			return Limits{}, false
		}
	}
	for _, pair := range [][2]*int{
		{&limits.MaxSegments, &defaults.MaxSegments}, {&limits.MaxTranscriptBytes, &defaults.MaxTranscriptBytes},
		{&limits.MaxNotes, &defaults.MaxNotes}, {&limits.MaxManualBytes, &defaults.MaxManualBytes},
	} {
		if *pair[0] < 0 {
			return Limits{}, false
		}
		if *pair[0] == 0 && !durable {
			*pair[0] = *pair[1]
		}
	}
	if limits.MaxDuration == 0 {
		limits.MaxDuration = defaults.MaxDuration
	}
	return limits, limits.MaxDuration > 0
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
	lookup, indexed := sink.(RecordLookup)
	limits, ok := normalizeLimits(config.Limits, indexed)
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
		trackOrder: order, limits: limits, sink: sink, lookup: lookup, state: Ready,
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

func (s *Session) transcriptUsageLocked() (bytes uint64, partials int, ok bool) {
	bytes = s.transcriptBytes
	for _, track := range s.tracks {
		if track.partial != nil {
			if uint64(len(track.partial.Text)) > ^uint64(0)-bytes {
				return 0, 0, false
			}
			bytes += uint64(len(track.partial.Text))
			partials++
		}
	}
	return bytes, partials, true
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
	if s.lookup != nil {
		lookupEvent := e
		lookupEvent.Kind = Final
		record := Record{Key: key, Kind: TranscriptFinal, SessionID: s.id, Event: lookupEvent}
		s.mu.Unlock()
		receipt, found, err := s.lookup.LookupRecord(ctx, record)
		s.mu.Lock()
		if authorityErr := s.checkTokenLocked(token); authorityErr != nil {
			s.mu.Unlock()
			return authorityErr
		}
		if ctxErr := contextError(ctx); ctxErr != nil {
			if e.Kind == Final {
				ctxErr = s.retainLookupFailureLocked(record, Paused, ctxErr)
			} else if errors.Is(ctxErr, ErrCanceled) {
				s.failLocked(IssueCanceled)
			}
			s.mu.Unlock()
			return ctxErr
		}
		if e.Kind == Partial && (found || errors.Is(err, ErrConflict)) {
			s.mu.Unlock()
			return ErrOrder
		}
		if err != nil {
			if e.Kind == Final {
				err = s.retainLookupFailureLocked(record, Paused, err)
			} else {
				err = s.lookupFailureLocked(err)
			}
			s.mu.Unlock()
			return err
		}
		if found {
			if !validReceipt(receipt) {
				if e.Kind == Final {
					err = s.retainLookupFailureLocked(record, Paused, ErrSink)
				} else {
					err = s.lookupFailureLocked(ErrSink)
				}
				s.mu.Unlock()
				return err
			}
			s.mu.Unlock()
			return nil
		}
	}
	return s.admitEventLocked(ctx, e, key)
}

// The admission gate and mu are held on entry; this returns with mu released.
func (s *Session) admitEventLocked(ctx context.Context, e Event, key string) error {
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
	if e.Kind == Final && s.finalCount == ^uint64(0) {
		s.failLocked(IssueLimit)
		s.mu.Unlock()
		return ErrLimit
	}
	usage, partials, usageOK := s.transcriptUsageLocked()
	if !usageOK {
		s.failLocked(IssueLimit)
		s.mu.Unlock()
		return ErrLimit
	}
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
		usage -= uint64(len(previous.Text))
	} else {
		if e.Start < track.lastEnd {
			s.mu.Unlock()
			return ErrOrder
		}
		if s.finalCount == ^uint64(0) || s.limits.MaxSegments > 0 && (s.finalCount >= uint64(s.limits.MaxSegments) || uint64(partials) >= uint64(s.limits.MaxSegments)-s.finalCount) {
			s.failLocked(IssueLimit)
			s.mu.Unlock()
			return ErrLimit
		}
	}
	if uint64(len(e.Text)) > ^uint64(0)-usage || s.limits.MaxTranscriptBytes > 0 && usage+uint64(len(e.Text)) > uint64(s.limits.MaxTranscriptBytes) {
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
	s.pendingLookup = false
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
	// A control may invalidate authority while durable lookup releases mu.
	// Keep the admitted note's original recovery state if that control fails.
	recovery := s.state
	if recovery == Running {
		recovery = Paused
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
	if s.lookup != nil {
		record := Record{Key: s.id + "/note/" + note.ID, Kind: UserNote, SessionID: s.id, Note: note}
		s.mu.Unlock()
		receipt, found, err := s.lookup.LookupRecord(ctx, record)
		s.mu.Lock()
		if ctxErr := contextError(ctx); ctxErr != nil {
			ctxErr = s.retainLookupFailureLocked(record, recovery, ctxErr)
			s.mu.Unlock()
			return ctxErr
		}
		if err != nil {
			err = s.retainLookupFailureLocked(record, recovery, err)
			s.mu.Unlock()
			return err
		}
		if found {
			if !validReceipt(receipt) {
				err = s.retainLookupFailureLocked(record, recovery, ErrSink)
				s.mu.Unlock()
				return err
			}
			s.mu.Unlock()
			return nil
		}
	}
	return s.admitNoteLocked(ctx, note, recovery)
}

// The admission gate and mu are held on entry; this returns with mu released.
func (s *Session) admitNoteLocked(ctx context.Context, note ManualNoteInput, recovery State) error {
	if index, found := s.noteByID[note.ID]; found {
		if sameManualNote(s.notes[index].ManualNoteInput, note) {
			s.mu.Unlock()
			return nil
		}
		s.failLocked(IssueConflict)
		s.mu.Unlock()
		return ErrConflict
	}
	if s.noteCount == ^uint64(0) || uint64(len(note.Text)) > ^uint64(0)-s.totalManualBytes || s.limits.MaxNotes > 0 && s.noteCount >= uint64(s.limits.MaxNotes) || s.limits.MaxManualBytes > 0 && s.totalManualBytes+uint64(len(note.Text)) > uint64(s.limits.MaxManualBytes) {
		s.failLocked(IssueLimit)
		s.mu.Unlock()
		return ErrLimit
	}
	note.ID, note.Text = strings.Clone(note.ID), strings.Clone(note.Text)
	record := Record{Key: s.id + "/note/" + note.ID, Kind: UserNote, SessionID: s.id, Note: note}
	s.pending = &record
	s.pendingLookup = false
	s.pendingState = s.noteRecoveryLocked(recovery)
	s.mu.Unlock()
	return s.appendRecord(ctx, record)
}

func (s *Session) retainLookupFailureLocked(record Record, recovery State, err error) error {
	if !errors.Is(err, ErrConflict) && !errors.Is(err, ErrDeleted) && !errors.Is(err, ErrLimit) && !errors.Is(err, ErrInvalid) {
		record.Key, record.SessionID = strings.Clone(record.Key), strings.Clone(record.SessionID)
		record.Event = cloneEvent(record.Event)
		record.Note.ID, record.Note.Text = strings.Clone(record.Note.ID), strings.Clone(record.Note.Text)
		s.pending = &record
		s.pendingLookup = true
		s.pendingState = s.noteRecoveryLocked(recovery)
	}
	return s.lookupFailureLocked(err)
}

func (s *Session) noteRecoveryLocked(recovery State) State {
	switch s.state {
	case Ready, Paused, Stopped:
		return s.state
	case Stopping:
		return Stopped
	}
	return recovery
}

func (s *Session) lookupFailureLocked(err error) error {
	if errors.Is(err, ErrConflict) {
		s.failLocked(IssueConflict)
		return ErrConflict
	}
	if errors.Is(err, ErrDeleted) {
		s.failLocked(IssueSink)
		return ErrDeleted
	}
	if errors.Is(err, context.Canceled) {
		s.failLocked(IssueCanceled)
		return errors.Join(ErrCanceled, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		s.failLocked(IssueCanceled)
		return errors.Join(ErrCanceled, context.DeadlineExceeded)
	}
	if errors.Is(err, ErrCanceled) {
		s.failLocked(IssueCanceled)
		return ErrCanceled
	}
	if errors.Is(err, ErrLimit) {
		s.failLocked(IssueLimit)
		return ErrLimit
	}
	s.failLocked(IssueSink)
	return ErrSink
}

func (s *Session) trimFinalsLocked() {
	for len(s.finals) > hotRows || s.finalBytes > hotBytes {
		s.finalBytes -= len(s.finals[0].Text)
		copy(s.finals, s.finals[1:])
		last := len(s.finals) - 1
		s.finals[last] = FinalSegment{}
		s.finals = s.finals[:last]
	}
}

func (s *Session) trimNotesLocked() {
	for len(s.notes) > hotRows || s.manualBytes > hotBytes {
		s.manualBytes -= len(s.notes[0].Text)
		copy(s.notes, s.notes[1:])
		last := len(s.notes) - 1
		s.notes[last] = ManualNote{}
		s.notes = s.notes[:last]
	}
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
	if errors.Is(err, ErrDeleted) {
		return s.appendFailure(IssueSink, ErrDeleted)
	}
	if errors.Is(err, ErrLimit) {
		return s.appendFailure(IssueLimit, ErrLimit)
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
		if s.lookup == nil {
			s.finalByKey[record.Key] = len(s.finals)
		}
		s.finals = append(s.finals, FinalSegment{Event: record.Event, SourceID: receipt.SourceID})
		s.finalBytes += len(record.Event.Text)
		s.finalCount++
		s.transcriptBytes += uint64(len(record.Event.Text))
		if s.lookup != nil {
			s.trimFinalsLocked()
		}
		track := s.tracks[record.Event.TrackID]
		track.lastEnd = record.Event.End
		track.partial = nil
	case UserNote:
		if s.lookup == nil {
			s.noteByID[record.Note.ID] = len(s.notes)
		}
		s.notes = append(s.notes, ManualNote{ManualNoteInput: record.Note, SourceID: receipt.SourceID})
		s.manualBytes += len(record.Note.Text)
		s.noteCount++
		s.totalManualBytes += uint64(len(record.Note.Text))
		if s.lookup != nil {
			s.trimNotesLocked()
		}
	}
	s.pending = nil
	s.pendingLookup = false
	return nil
}

func (s *Session) retryLookupRecord(ctx context.Context, record Record, recovery State) error {
	receipt, found, err := s.lookup.LookupRecord(ctx, record)
	if ctxErr := s.checkContext(ctx); ctxErr != nil {
		return ctxErr
	}
	s.mu.Lock()
	if err != nil {
		err = s.lookupFailureLocked(err)
		s.mu.Unlock()
		return err
	}
	if found && !validReceipt(receipt) {
		err = s.lookupFailureLocked(ErrSink)
		s.mu.Unlock()
		return err
	}
	s.pending, s.pendingLookup = nil, false
	if found {
		s.mu.Unlock()
		return nil
	}
	switch record.Kind {
	case TranscriptFinal:
		return s.admitEventLocked(ctx, record.Event, record.Key)
	case UserNote:
		return s.admitNoteLocked(ctx, record.Note, recovery)
	default:
		s.mu.Unlock()
		return ErrInvalid
	}
}

// Retry reuses the exact pending record and stable key after a failed lookup
// or uncertain write. Lookup retries repeat normal ordering/budget admission.
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
	lookup := s.pendingLookup
	s.mu.Unlock()
	defer s.endTransition()
	if err := s.acquire(ctx); err != nil {
		return err
	}
	defer s.release()
	var err error
	if lookup {
		err = s.retryLookupRecord(ctx, record, restore)
	} else {
		err = s.appendRecord(ctx, record)
	}
	if err != nil {
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
