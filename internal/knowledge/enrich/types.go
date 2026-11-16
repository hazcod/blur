// Package enrich builds an inferred, correctable Library from encrypted source
// evidence. Inferred mentions stay distinct from explicit user-authored entries.
package enrich

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/knowledge/derive"
	"github.com/hazcod/blur/internal/storage"
)

const (
	ChunkBytes           = 1024
	ChunkOverlap         = 128
	MaxSourceEntities    = 64
	MaxNameEntries       = 128
	MaxEntityBytes       = 96 << 10
	MaxProgressBytes     = 12 << 10
	MaxCorrectionBytes   = 512
	Version              = "library-v8"
	FailureInvalidOutput = "invalid_output"
)

var (
	ErrInvalid     = errors.New("library enrichment: invalid request")
	ErrSourceGone  = errors.New("library enrichment: source gone")
	ErrStale       = errors.New("library enrichment: stale source progress")
	ErrSuppressed  = errors.New("library enrichment: user correction takes precedence")
	ErrCapacity    = errors.New("library enrichment: capacity reached")
	ErrUnavailable = errors.New("library enrichment: unavailable")
)

type Progress struct {
	SourceID   string   `json:"source_id"`
	ModelKey   string   `json:"model_key"`
	Next       int      `json:"next"`
	Complete   bool     `json:"complete"`
	Suppressed bool     `json:"suppressed,omitempty"`
	Entities   []string `json:"entities,omitempty"`
	// Declined is terminal only for this source and ModelKey. Next remains the
	// last committed prefix; Complete stays false because the tail was not read.
	Declined    bool   `json:"declined,omitempty"`
	FailureCode string `json:"failure_code,omitempty"`
}

type Chunk struct {
	SourceID, ModelKey string
	Start, End, Next   int
	Complete           bool
	Proposals          []derive.Proposal
	Declined           bool
	FailureCode        string
	RetryInvalid       bool
}

type Checkpoint struct {
	After           string `json:"after,omitempty"`
	ModelKey        string `json:"model_key"`
	Complete        bool   `json:"complete"` // Legacy observation directory only.
	CoverageLimited bool   `json:"coverage_limited,omitempty"`
	CaptureAfter    string `json:"capture_after,omitempty"`
	CaptureComplete bool   `json:"capture_complete,omitempty"`
	RetryInvalid    bool   `json:"retry_invalid,omitempty"`
}

type PageRequest struct {
	After string
	Limit int
}

type Repository interface {
	EncryptedAtRest() bool
	GetEnrichmentProgress(context.Context, string) (Progress, bool, error)
	ApplyEnrichment(context.Context, Chunk) error
	GetEnrichmentCheckpoint(context.Context) (Checkpoint, bool, error)
	PutEnrichmentCheckpoint(context.Context, Checkpoint) error
	ListInferredKnowledge(context.Context, PageRequest) (knowledge.Page, error)
	GetInferredKnowledge(context.Context, string) (knowledge.Entity, bool, error)
	FindInferredKnowledge(context.Context, string) ([]knowledge.Entity, error)
	CorrectInferredFact(context.Context, string, string, string) error
	DismissInferredFact(context.Context, string, string) error
	DeleteInferredKnowledge(context.Context, string) error
}

// FactConfirmer accepts the exact visible proposal without changing its
// wording or turning captured attribution into a user-authored correction.
type FactConfirmer interface {
	ConfirmInferredFact(context.Context, string, knowledge.Fact) error
}

// Sources may also implement storage.SourceEvidenceTimeReader and
// storage.CaptureEvidencePageReader. Those ports expose published multipart
// sources without changing legacy observation identities or directory order.
type Sources interface {
	ListObservationPage(context.Context, storage.ObservationPageRequest) (storage.ObservationPage, error)
	GetObservationWithCaptureTime(context.Context, string) (capture.SanitizedObservation, time.Time, bool, error)
}

func ValidHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			if c < 'a' || c > 'f' {
				return false
			}
		}
	}
	return true
}

func IsEntityID(id string) bool {
	return strings.HasPrefix(id, "auto-") && ValidHash(strings.TrimPrefix(id, "auto-"))
}

func EntityID(kind knowledge.Kind, name, app, domain string) string {
	hash := sha256.Sum256([]byte(Version + "\x00entity\x00" + string(kind) + "\x00" + knowledge.NormalizeExact(name) + "\x00" + app + "\x00" + domain))
	return "auto-" + hex.EncodeToString(hash[:])
}

func FactID(sourceID, text string) string {
	hash := sha256.Sum256([]byte(Version + "\x00fact\x00" + sourceID + "\x00" + knowledge.NormalizeExact(text)))
	return "fact-" + hex.EncodeToString(hash[:])
}

func ModelKey(digest string) string {
	hash := sha256.Sum256([]byte(Version + "\x00" + digest))
	return hex.EncodeToString(hash[:])
}

func ValidProgress(progress Progress) bool {
	if !ValidHash(progress.SourceID) || !ValidHash(progress.ModelKey) || progress.Next < 0 || progress.Next > capture.MaxObservationTextBytes || len(progress.Entities) > MaxSourceEntities || progress.Declined && progress.Complete || progress.FailureCode != "" && (!progress.Declined || progress.FailureCode != FailureInvalidOutput) {
		return false
	}
	seen := map[string]bool{}
	for _, id := range progress.Entities {
		if !IsEntityID(id) || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

func ValidCorrection(text string) bool {
	return len(text) > 0 && len(text) <= MaxCorrectionBytes && strings.TrimSpace(text) == text && utf8.ValidString(text) && !strings.ContainsRune(text, 0)
}

func NextChunk(text string, start int) (end, next int, complete bool, err error) {
	if !utf8.ValidString(text) || len(text) > capture.MaxObservationTextBytes || start < 0 || start > len(text) || start < len(text) && !utf8.RuneStart(text[start]) {
		return 0, 0, false, ErrInvalid
	}
	if start == len(text) {
		return start, start, true, nil
	}
	end = min(start+ChunkBytes, len(text))
	for end < len(text) && !utf8.RuneStart(text[end]) {
		end--
	}
	if end == len(text) {
		return end, end, true, nil
	}
	next = end - ChunkOverlap
	for next < end && !utf8.RuneStart(text[next]) {
		next++
	}
	return end, next, false, nil
}
