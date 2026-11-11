// Package derive validates local-model suggestions made from a bounded set of
// sanitized, source-linked observations. It does not persist or confirm them.
package derive

import (
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/knowledge"
)

const (
	MaxEvidenceSources    = 8
	MaxEvidenceItemBytes  = 4 << 10
	MaxEvidenceBytes      = 16 << 10
	MaxSuggestions        = 3
	MaxSuggestionBytes    = 512
	MaxSuggestionNameSize = 128
	MaxOutputTokens       = 192
)

var ErrInvalid = errors.New("knowledge derivation: invalid evidence or suggestion")

// Evidence contains only an observation's immutable identifier and its
// already-sanitized text. Callers must select it from one explicit context.
type Evidence struct {
	SourceID string `json:"source_id"`
	Text     string `json:"text"`
}

// Proposal is untrusted until a user accepts it. The model cannot choose
// identifiers, state, aliases, relationships, or merge targets.
type Proposal struct {
	Kind     knowledge.Kind `json:"kind"`
	Name     string         `json:"name"`
	Fact     string         `json:"fact"`
	SourceID string         `json:"source_id"`
}

// ValidateInput enforces the exact source and byte budget shared by every
// provider implementation. IDs are canonical lowercase SHA-256 content IDs.
func ValidateInput(input []Evidence) error {
	if len(input) == 0 || len(input) > MaxEvidenceSources {
		return ErrInvalid
	}
	seen := make(map[string]struct{}, len(input))
	bytesUsed := 0
	for _, item := range input {
		if !validSourceID(item.SourceID) || !validCleanText(item.Text, MaxEvidenceItemBytes) {
			return ErrInvalid
		}
		if _, ok := seen[item.SourceID]; ok {
			return ErrInvalid
		}
		seen[item.SourceID] = struct{}{}
		bytesUsed += len(item.SourceID) + len(item.Text) + 16
		if bytesUsed > MaxEvidenceBytes {
			return ErrInvalid
		}
	}
	return nil
}

// ValidateSuggestions proves every returned item is small, typed, and cites
// one exact input source. It deliberately does not try to infer factual truth.
func ValidateSuggestions(input []Evidence, suggestions []Proposal) error {
	if ValidateInput(input) != nil || len(suggestions) > MaxSuggestions {
		return ErrInvalid
	}
	sources := make(map[string]struct{}, len(input))
	for _, item := range input {
		sources[item.SourceID] = struct{}{}
	}
	seen := make(map[string]struct{}, len(suggestions))
	for _, suggestion := range suggestions {
		if !validKind(suggestion.Kind) || !validCleanText(suggestion.Name, MaxSuggestionNameSize) || !validCleanText(suggestion.Fact, MaxSuggestionBytes) || len(suggestion.Name)+len(suggestion.Fact) > MaxSuggestionBytes || !validSourceID(suggestion.SourceID) {
			return ErrInvalid
		}
		if _, ok := sources[suggestion.SourceID]; !ok {
			return ErrInvalid
		}
		key := string(suggestion.Kind) + "\x00" + knowledge.NormalizeExact(suggestion.Name) + "\x00" + knowledge.NormalizeExact(suggestion.Fact) + "\x00" + suggestion.SourceID
		if _, ok := seen[key]; ok {
			return ErrInvalid
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validKind(kind knowledge.Kind) bool {
	switch kind {
	case knowledge.Person, knowledge.Project, knowledge.Topic, knowledge.Place, knowledge.Workflow:
		return true
	default:
		return false
	}
}

func validSourceID(value string) bool {
	if len(value) != 64 {
		return false
	}
	for i := range value {
		if !(value[i] >= '0' && value[i] <= '9' || value[i] >= 'a' && value[i] <= 'f') {
			return false
		}
	}
	return true
}

func validCleanText(value string, max int) bool {
	if value == "" || len(value) > max || !utf8.ValidString(value) || strings.TrimSpace(value) != value || strings.IndexByte(value, 0) >= 0 {
		return false
	}
	for _, r := range value {
		if r < 0x20 && r != '\n' && r != '\t' || r == 0x7f {
			return false
		}
	}
	return true
}
