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
	// OutputDecoderRevision admits one bounded repair pass after a decoder
	// change, without discarding already completed same-model derivation.
	OutputDecoderRevision = 1
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
	CompactionParts   int      `json:"compaction_parts,omitempty"`
	CompactionSlots   int      `json:"compaction_slots,omitempty"`
	CompactionVersion uint32   `json:"compaction_version,omitempty"`
	Compaction        string   `json:"compaction,omitempty"`
	ClaimsLimited     bool     `json:"claims_limited,omitempty"`
	OutputRevision    uint32   `json:"output_revision,omitempty"`
	SourceID          string   `json:"source_id"`
	ModelKey          string   `json:"model_key"`
	Next              int      `json:"next"`
	Complete          bool     `json:"complete"`
	Suppressed        bool     `json:"suppressed,omitempty"`
	Entities          []string `json:"entities,omitempty"`
	// Declined is terminal only for this source and ModelKey. Next remains the
	// last committed prefix; Complete stays false because the tail was not read.
	Declined    bool   `json:"declined,omitempty"`
	FailureCode string `json:"failure_code,omitempty"`
}

type Chunk struct {
	PreserveClaims     bool
	SpanBytes          int // Zero retains legacy chunk framing; compaction adapts to admission.
	CompactionVersion  uint32
	Compaction         string
	ClaimsLimited      bool
	OutputRevision     uint32
	SourceID, ModelKey string
	Start, End, Next   int
	Complete           bool
	Proposals          []derive.Proposal
	Declined           bool
	FailureCode        string
	RetryInvalid       bool
}

// CompactionBlock retains one source span's generated view. There is no
// whole-source summary size or fixed maximum number of summaries.
type CompactionBlock struct {
	SourceID, ModelKey      string
	Index, Start, End, Next int
	Text                    string
}
type CompactionPage struct {
	Blocks  []CompactionBlock
	Next    int
	HasMore bool
}
type CompactionReader interface {
	ListSourceCompactions(context.Context, string, int, int) (CompactionPage, error)
}

type Checkpoint struct {
	OutputRevision  uint32 `json:"output_revision,omitempty"`
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
	ListInferredFactsPage(context.Context, string, knowledge.FactPageRequest) (knowledge.FactPage, error)
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
	if progress.CompactionParts < 0 || progress.CompactionSlots < progress.CompactionParts || progress.CompactionSlots > capture.MaxObservationTextBytes {
		return false
	}
	if !(progress.CompactionVersion == 1 && progress.CompactionParts > 0 && progress.Compaction == "") && !ValidCompaction(progress.CompactionVersion, progress.Compaction) {
		return false
	}
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

func ValidCompaction(version uint32, text string) bool {
	if version == 0 {
		return text == ""
	}
	if version != 1 || len(text) == 0 || len(text) > derive.MaxCompactionBlockTextBytes || strings.TrimSpace(text) != text || !utf8.ValidString(text) {
		return false
	}
	for _, r := range text {
		if r < 0x20 && r != '\n' && r != '\t' || r == 0x7f {
			return false
		}
	}
	return true
}

func ValidCorrection(text string) bool {
	return len(text) > 0 && len(text) <= MaxCorrectionBytes && strings.TrimSpace(text) == text && utf8.ValidString(text) && !strings.ContainsRune(text, 0)
}

func NextChunk(text string, start int) (end, next int, complete bool, err error) {
	return NextSizedChunk(text, start, ChunkBytes)
}

func NextSizedChunk(text string, start, size int) (end, next int, complete bool, err error) {
	if size < utf8.UTFMax || size > derive.MaxEvidenceItemBytes || !utf8.ValidString(text) || len(text) > capture.MaxObservationTextBytes || start < 0 || start > len(text) || start < len(text) && !utf8.RuneStart(text[start]) {
		return 0, 0, false, ErrInvalid
	}
	if start == len(text) {
		return start, start, true, nil
	}
	end = min(start+size, len(text))
	for end < len(text) && !utf8.RuneStart(text[end]) {
		end--
	}
	if end == len(text) {
		return end, end, true, nil
	}
	next = max(start+1, end-min(ChunkOverlap, size/4))
	for next < end && !utf8.RuneStart(text[next]) {
		next++
	}
	return end, next, false, nil
}
