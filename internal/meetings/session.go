// Package meetings coordinates bounded transcript text and explicit session
// controls. It owns no audio device, model, file, or persistence implementation.
package meetings

import (
	"context"
	"errors"
	"time"
)

type State string

const (
	Ready    State = "ready"
	Running  State = "running"
	Paused   State = "paused"
	Stopping State = "stopping"
	Stopped  State = "stopped"
	Failed   State = "failed"
)

type Issue string

const (
	NoIssue       Issue = ""
	IssueCanceled Issue = "canceled"
	IssueSink     Issue = "persistence_failed"
	IssueConflict Issue = "conflicting_final"
	IssueLimit    Issue = "limit_reached"
)

var (
	ErrInvalid    = errors.New("meetings: invalid input")
	ErrState      = errors.New("meetings: operation not available in this state")
	ErrEpoch      = errors.New("meetings: expired input epoch")
	ErrOrder      = errors.New("meetings: non-monotonic transcript event")
	ErrConflict   = errors.New("meetings: conflicting finalized evidence")
	ErrLimit      = errors.New("meetings: session limit reached")
	ErrEncryption = errors.New("meetings: authenticated encrypted idempotent sink required")
	ErrSink       = errors.New("meetings: persistence failed")
	ErrCanceled   = errors.New("meetings: operation canceled")
)

type EventKind string

const (
	Partial EventKind = "partial"
	Final   EventKind = "final"
)

type Event struct {
	Kind      EventKind
	SessionID string
	TrackID   string
	SegmentID string
	Revision  uint64
	Start     time.Duration
	End       time.Duration
	Text      string
}

type Token struct {
	SessionID string
	Epoch     uint64
}

type ManualNoteInput struct {
	ID   string
	At   time.Duration
	Text string
	// New notes use an absolute wall time and At=0. The omitted zero field
	// preserves canonical legacy record/HMAC bytes when reading old sources.
	RecordedAt time.Time `json:",omitzero"`
}

type RecordKind string

const (
	TranscriptFinal RecordKind = "transcript_final"
	UserNote        RecordKind = "manual_note"
)

// Record.Key is stable across uncertain-write retries. A sink must atomically
// return the same source for the same key and reject a different payload.
type Record struct {
	Key       string
	Kind      RecordKind
	SessionID string
	Event     Event
	Note      ManualNoteInput
}

type Receipt struct{ SourceID string }

type EncryptionMode string

const AuthenticatedEncryption EncryptionMode = "authenticated_encryption"

type SinkCapabilities struct {
	Encryption       EncryptionMode
	AtomicIdempotent bool
}

// RecordLookup is the durable identity index used for session continuation.
// It checks the original immutable payload and deletion fences atomically.
// A matching live key returns its original receipt; a different payload returns
// ErrConflict and an erased key returns ErrDeleted. An absent key returns false.
// Lookup must not create or restore evidence.
type RecordLookup interface {
	LookupRecord(context.Context, Record) (Receipt, bool, error)
}

// Sink is a trusted adapter port, not an encryption implementation. New fails
// closed unless the adapter explicitly declares both required guarantees.
type Sink interface {
	Capabilities() SinkCapabilities
	Append(context.Context, Record) (Receipt, error)
}

// Zero cumulative limits are unlimited when the sink implements RecordLookup.
// A nonzero cumulative limit is an explicit caller-selected work budget. Sinks
// without a durable identity index use finite legacy defaults instead.
type Limits struct {
	MaxSegments        int
	MaxEventBytes      int
	MaxTranscriptBytes int
	MaxNotes           int
	MaxNoteBytes       int
	MaxManualBytes     int
	MaxDuration        time.Duration
}

type Config struct {
	SessionID string
	Tracks    []string
	Limits    Limits
}

type FinalSegment struct {
	Event
	SourceID string
}

type ManualNote struct {
	ManualNoteInput
	SourceID string
}

// Snapshot contains the live tail for durable-indexed sessions. Complete
// evidence belongs to Repository.MeetingRecords and must be read in pages.
type Snapshot struct {
	SessionID string
	State     State
	Epoch     uint64
	Issue     Issue
	Finals    []FinalSegment
	Partials  []Event
	Notes     []ManualNote
	Pending   *Record
}
