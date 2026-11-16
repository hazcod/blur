package storage

import (
	"context"
	"time"

	"github.com/hazcod/blur/internal/capture"
)

// SourceEvidenceWindow joins legacy observations and immutable capture parts.
// CapturedAt records the latest authenticated visit inside the requested range,
// not an indexing or migration time. Continuation identities are opaque real
// source IDs; callers must return both Next fields unchanged. Equal-time parts
// follow their edition's order, which need not be SourceID lexical order.
// Pending explicitly indicates that durable metadata/part indexing is unfinished.
type SourceEvidenceWindow struct {
	Sources        []capture.Evidence
	CapturedAt     []time.Time
	NextCapturedAt time.Time
	NextSourceID   string
	Bytes          int
	HasMore        bool
	Pending        bool
}

type SourceEvidenceWindowReader interface {
	ListSourceEvidenceWindow(context.Context, ObservationWindowRequest) (SourceEvidenceWindow, error)
}
