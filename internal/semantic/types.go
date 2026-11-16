// Package semantic implements source-linked semantic memory retrieval. It
// retains vectors and provenance, while primary text stays in encrypted sources.
package semantic

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/capture"
)

const (
	DefaultDimensions  = 256
	MaxDimensions      = 1024
	ChunkBytes         = 1536
	ChunkOverlap       = 128
	MaxChunksPerSource = 896
	MaxResults         = 50
	MaxScopeSources    = 65536
	MaxDocumentBytes   = (4 << 20) - 1024
	MaxPageRows        = 32
	MaxPageBytes       = 8 << 20
	FormatPlain        = "plain-v1"
	FormatGemma        = "gemma-v1"
	FormatNomic        = "nomic-v1"
	SkipEmpty          = "empty"
	SkipSelf           = "self"
)

var (
	ErrInvalid       = errors.New("semantic: invalid request or document")
	ErrModelMismatch = errors.New("semantic: incompatible embedding generation")
	ErrUnavailable   = errors.New("semantic: index unavailable")
	ErrSourceGone    = errors.New("semantic: source is no longer available")
)

type Model struct {
	Name       string `json:"name"`
	Digest     string `json:"digest"`
	Dimensions int    `json:"dimensions"`
	Format     string `json:"format"`
}

func (m Model) Validate() error {
	if m.Name == "" || len(m.Name) > 128 || !utf8.ValidString(m.Name) || strings.ContainsAny(m.Name, "\x00\r\n") || !ValidID(m.Digest) || m.Dimensions < 1 || m.Dimensions > MaxDimensions {
		return ErrInvalid
	}
	switch m.Format {
	case FormatPlain, FormatGemma, FormatNomic:
		return nil
	default:
		return ErrInvalid
	}
}

func (m Model) Key() string {
	if m.Validate() != nil {
		return ""
	}
	hash := sha256.Sum256([]byte(fmt.Sprintf("blur.semantic.v1\x00%s\x00%d\x00%s", m.Digest, m.Dimensions, m.Format)))
	return hex.EncodeToString(hash[:])
}

type Span struct{ Start, End int }

type Chunk struct {
	Start  int       `json:"start"`
	End    int       `json:"end"`
	Vector []float32 `json:"-"`
}

type Document struct {
	SourceID   string    `json:"source_id"`
	Model      Model     `json:"model"`
	AppID      string    `json:"app_id"`
	Domain     string    `json:"domain"`
	CapturedAt time.Time `json:"captured_at"`
	TextBytes  int       `json:"text_bytes"`
	SkipReason string    `json:"skip_reason,omitempty"`
	Chunks     []Chunk   `json:"chunks"`
}

func (d Document) Validate() error {
	if !ValidID(d.SourceID) || d.Model.Validate() != nil || !canonicalIdentifier(d.AppID, 128) || !canonicalIdentifier(d.Domain, 253) || d.TextBytes < 0 || d.TextBytes > capture.MaxObservationTextBytes || d.CapturedAt.IsZero() || !time.Unix(0, d.CapturedAt.UnixNano()).Equal(d.CapturedAt) || len(d.Chunks) > MaxChunksPerSource {
		return ErrInvalid
	}
	if d.SkipReason != "" {
		if len(d.Chunks) != 0 || d.SkipReason != SkipEmpty && d.SkipReason != SkipSelf {
			return ErrInvalid
		}
		return nil
	}
	if len(d.Chunks) == 0 {
		return ErrInvalid
	}
	covered, previousStart := 0, -1
	for _, chunk := range d.Chunks {
		if chunk.Start <= previousStart || chunk.Start > covered || chunk.Start < 0 || chunk.End <= chunk.Start || chunk.End > d.TextBytes || chunk.End-chunk.Start > ChunkBytes || !validVector(chunk.Vector, d.Model.Dimensions) {
			return ErrInvalid
		}
		covered, previousStart = chunk.End, chunk.Start
	}
	if covered != d.TextBytes {
		return ErrInvalid
	}
	return nil
}

func (d Document) Clone() Document {
	copyDoc := d
	copyDoc.Chunks = make([]Chunk, len(d.Chunks))
	for index, chunk := range d.Chunks {
		copyDoc.Chunks[index] = Chunk{Start: chunk.Start, End: chunk.End, Vector: append([]float32(nil), chunk.Vector...)}
	}
	return copyDoc
}

type Query struct {
	Vector            []float32
	Limit             int
	AppIDs, Domains   []string
	Start, End        time.Time
	ScopeSourceIDs    []string
	resolveCapturedAt func(context.Context, string, time.Time, time.Time) (time.Time, bool, error)
}

type Hit struct {
	SourceID, AppID, Domain string
	Score                   float32
	Start, End              int
	CapturedAt              time.Time
	SourceBytes             int
}

type PageRequest struct {
	After, ModelKey string
	Limit, MaxBytes int
}

type Page struct {
	Documents []Document
	Bytes     int
	Next      string
	HasMore   bool
}

type Checkpoint struct {
	ModelKey        string `json:"model_key"`
	After           string `json:"after,omitempty"`
	Indexed         uint64 `json:"indexed"`
	Skipped         uint64 `json:"skipped"`
	Complete        bool   `json:"complete"` // Legacy observation directory only.
	CaptureAfter    string `json:"capture_after,omitempty"`
	CaptureComplete bool   `json:"capture_complete,omitempty"`
}

type Repository interface {
	EncryptedAtRest() bool
	PutSemanticDocument(context.Context, Document) error
	GetSemanticDocument(context.Context, string) (Document, bool, error)
	ListSemanticDocuments(context.Context, PageRequest) (Page, error)
	GetSemanticCheckpoint(context.Context, string) (Checkpoint, bool, error)
	PutSemanticCheckpoint(context.Context, Checkpoint) error
}

type Embedder interface {
	Identity(context.Context) (Model, error)
	EmbedDocuments(context.Context, Model, []string) ([][]float32, error)
	EmbedQuery(context.Context, Model, string) ([]float32, error)
}

func ValidID(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func canonicalIdentifier(value string, limit int) bool {
	if len(value) > limit {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '.' && character != '-' && character != '_' {
			return false
		}
	}
	return true
}

func validVector(vector []float32, dimensions int) bool {
	if len(vector) != dimensions || dimensions < 1 || dimensions > MaxDimensions {
		return false
	}
	var length float64
	for _, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return false
		}
		length += float64(value) * float64(value)
	}
	return math.Abs(length-1) <= 0.005
}
