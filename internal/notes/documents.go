package notes

import (
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

var (
	ErrDocumentConflict = errors.New("notes: document changed; reload before saving")
	ErrDocumentNotFound = errors.New("notes: document not found")
	ErrDocumentCapacity = errors.New("notes: document metadata capacity reached")
)

// Document is one editable Markdown note. SourceID identifies immutable proof
// for this revision; ID survives edits. Text is the sanitized export payload.
type Document struct {
	ID        string    `json:"id"`
	Revision  uint64    `json:"revision"`
	SourceID  string    `json:"source_id"`
	Text      string    `json:"-"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type DocumentPageRequest struct {
	BeforeUpdatedAt time.Time
	BeforeID        string
	Limit, MaxBytes int
}

type DocumentPage struct {
	Documents     []Document
	NextUpdatedAt time.Time
	NextID        string
	HasMore       bool
	// CatchingUp indicates bounded, resumable admission of older saved notes.
	CatchingUp bool
}

func validNoteDomain(domain string) bool {
	if domain == "local" {
		return true
	}
	parts := strings.Split(domain, ".")
	if len(parts) != 4 || parts[2] != "notes" || parts[3] != "local" || !strings.HasPrefix(parts[0], "r") {
		return false
	}
	revision, err := strconv.ParseUint(parts[0][1:], 10, 48)
	if err != nil || revision == 0 || parts[0] != "r"+strconv.FormatUint(revision, 10) {
		return false
	}
	id, err := hex.DecodeString(parts[1])
	defer clear(id)
	return err == nil && len(id) == 32 && hex.EncodeToString(id) == parts[1]
}
