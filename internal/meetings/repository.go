package meetings

import (
	"context"
	"errors"
	"strings"
	"time"
)

const (
	MaxMeetingTitleBytes = 512
	MaxLocaleBytes       = 64
	MaxPageRows          = 50
	MaxPageBytes         = 256 << 10
	DefaultPageBytes     = 64 << 10
)

var (
	ErrNotFound = errors.New("meetings: meeting not found")
	ErrDeleted  = errors.New("meetings: evidence was deleted")
)

// Meeting metadata is independent of transcript text. Revision advances only
// for explicit metadata updates; RecordCount is repository-owned live evidence.
type Meeting struct {
	ID          string
	Title       string
	Locale      string
	StartedAt   time.Time
	EndedAt     time.Time
	State       State
	Tracks      []string
	Revision    uint64
	RecordCount uint64
	// Appended and omitted when zero to preserve canonical legacy header bytes.
	RecordingStartedAt time.Time `json:",omitzero"`
}

func (m Meeting) Validate() error {
	if !validID(m.ID, 64) || len(m.Title) > MaxMeetingTitleBytes || !validText(m.Title) || len(m.Locale) > MaxLocaleBytes || !validText(m.Locale) || strings.TrimSpace(m.Locale) != m.Locale || !validMeetingInstant(m.StartedAt) || m.Revision == 0 || m.Revision >= 1<<48 || len(m.Tracks) == 0 || len(m.Tracks) > 8 {
		return ErrInvalid
	}
	if !m.RecordingStartedAt.IsZero() && (!validMeetingInstant(m.RecordingStartedAt) || m.RecordingStartedAt.Before(m.StartedAt)) {
		return ErrInvalid
	}
	if !m.EndedAt.IsZero() && (!validMeetingInstant(m.EndedAt) || m.EndedAt.Before(m.StartedAt) || !m.RecordingStartedAt.IsZero() && m.EndedAt.Before(m.RecordingStartedAt)) || m.State == Stopped && m.EndedAt.IsZero() {
		return ErrInvalid
	}
	switch m.State {
	case Ready, Running, Paused, Stopped, Failed:
	default:
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, track := range m.Tracks {
		if !validID(track, 32) || seen[track] {
			return ErrInvalid
		}
		seen[track] = true
	}
	return nil
}

// PageRequest.After is an opaque numeric cursor: pass the returned Next value
// without computing it. Lists are newest-created first; record pages append
// order first. Zero limits select 50 rows and a 64KiB text/metadata budget.
type PageRequest struct {
	After    uint64
	Limit    int
	MaxBytes int
}

func (p PageRequest) Normalized() (PageRequest, error) {
	if p.Limit == 0 {
		p.Limit = MaxPageRows
	}
	if p.MaxBytes == 0 {
		p.MaxBytes = DefaultPageBytes
	}
	if p.After >= 1<<48 || p.Limit < 1 || p.Limit > MaxPageRows || p.MaxBytes < 256 || p.MaxBytes > MaxPageBytes {
		return PageRequest{}, ErrInvalid
	}
	return p, nil
}

type MeetingPage struct {
	Meetings []Meeting
	Next     uint64
	More     bool
}

type StoredRecord struct {
	Sequence uint64
	Record   Record
	SourceID string
}

type RecordPage struct {
	Records []StoredRecord
	Next    uint64
	More    bool
}

// AgendaTail is a bounded live view, independent of forward history pages.
// Cursor is the last allocated ordinal, including erased slots, and is used
// only for subsequent append polling. Older means earlier ordinal slots remain
// outside this tail; full evidence remains available through MeetingRecords.
type AgendaTail struct {
	Meeting Meeting
	Records []StoredRecord
	Cursor  uint64
	Older   bool
}

// AgendaTailReader authenticates Ready/no-recording/no-final eligibility and
// returns metadata and a newest-first-selected tail from one repository view.
// Records are returned in ascending ordinal order. Work is bounded even when
// old deleted slots prevent filling the requested live tail.
type AgendaTailReader interface {
	ReadyAgendaTail(context.Context, string, PageRequest) (AgendaTail, error)
}

// Repository embeds the explicit authenticated-encryption/idempotency sink
// contract. PutMeeting creates Revision 1 with expected=0; updates supply the
// previous revision and its successor. RecordCount is never caller-controlled.
// Deleting a source/meeting must fence stale Record.Key retries permanently.
type Repository interface {
	Sink
	PutMeeting(context.Context, Meeting, uint64) error
	GetMeeting(context.Context, string) (Meeting, bool, error)
	ListMeetings(context.Context, PageRequest) (MeetingPage, error)
	MeetingRecords(context.Context, string, PageRequest) (RecordPage, error)
	DeleteMeeting(context.Context, string) error
}
