// Package knowledge models a small, evidence-backed library. It contains no
// model client and never derives people or attributes from untrusted prose.
package knowledge

import (
	"errors"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxEntities    = 2048
	MaxPageSize    = 100
	MaxPageBytes   = 2 << 20
	MaxAliases     = 32
	MaxFacts       = 128
	MaxTextBytes   = 4096
	MaxEntityBytes = 1 << 20
)

type Kind string

const (
	Person   Kind = "person"
	Project  Kind = "project"
	Topic    Kind = "topic"
	Place    Kind = "place"
	Workflow Kind = "workflow"
)

type State string

const (
	Confirmed     State = "confirmed"
	Deterministic State = "deterministic"
	Proposed      State = "proposed"
	Dismissed     State = "dismissed"
	Merged        State = "merged"
)

var ErrInvalid = errors.New("knowledge: invalid record")

// Source names an immutable capture ID or makes explicit that the user wrote
// the content directly. Exactly one provenance form must be set.
type Source struct {
	ID           string `json:"id,omitempty"`
	UserAuthored bool   `json:"user_authored,omitempty"`
}

type Fact struct {
	ID      string   `json:"id"`
	Text    string   `json:"text"`
	State   State    `json:"state"`
	Sources []Source `json:"sources"`
}

type OpenLoop struct {
	ID      string   `json:"id"`
	Text    string   `json:"text"`
	State   State    `json:"state"`
	Sources []Source `json:"sources"`
}

// Entity is a browsable view over linked sources. Merged entities retain a
// destination ID; the durable repository keeps a reversible merge snapshot.
type Entity struct {
	ID                string     `json:"id"`
	Kind              Kind       `json:"kind"`
	Name              string     `json:"name"`
	Aliases           []string   `json:"aliases,omitempty"`
	State             State      `json:"state"`
	Facts             []Fact     `json:"facts,omitempty"`
	OpenLoops         []OpenLoop `json:"open_loops,omitempty"`
	RelatedIDs        []string   `json:"related_ids,omitempty"`
	ActivitySourceIDs []string   `json:"activity_source_ids,omitempty"`
	MergedInto        string     `json:"merged_into,omitempty"`
}

type Page struct {
	Entities []Entity `json:"entities"`
	Next     string   `json:"next,omitempty"`
	HasMore  bool     `json:"has_more"`
}

func validOpaqueID(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for i := range s {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func validSourceID(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := range s {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func validText(s string, max int) bool {
	return len(s) > 0 && len(s) <= max && utf8.ValidString(s) && strings.TrimSpace(s) == s && !strings.ContainsRune(s, 0)
}
func validState(s State) bool {
	return s == Confirmed || s == Deterministic || s == Proposed || s == Dismissed || s == Merged
}
func validKind(k Kind) bool {
	return k == Person || k == Project || k == Topic || k == Place || k == Workflow
}

func validSources(sources []Source) bool {
	if len(sources) == 0 || len(sources) > 64 {
		return false
	}
	seen := map[string]bool{}
	for _, source := range sources {
		if source.UserAuthored == (source.ID != "") {
			return false
		}
		if source.ID != "" && !validSourceID(source.ID) {
			return false
		}
		key := source.ID
		if source.UserAuthored {
			key = "user"
		}
		if seen[key] {
			return false
		}
		seen[key] = true
	}
	return true
}

func (e Entity) Validate() error {
	if !validOpaqueID(e.ID) || !validKind(e.Kind) || !validText(e.Name, MaxTextBytes) || !validState(e.State) {
		return ErrInvalid
	}
	if len(e.Aliases) > MaxAliases || len(e.Facts) > MaxFacts || len(e.OpenLoops) > MaxFacts || len(e.RelatedIDs) > MaxEntities || len(e.ActivitySourceIDs) > MaxFacts {
		return ErrInvalid
	}
	if e.State == Merged {
		if !validOpaqueID(e.MergedInto) || e.MergedInto == e.ID {
			return ErrInvalid
		}
	} else if e.MergedInto != "" {
		return ErrInvalid
	}
	seen := map[string]bool{NormalizeExact(e.Name): true}
	for _, alias := range e.Aliases {
		normalized := NormalizeExact(alias)
		if !validText(alias, MaxTextBytes) || normalized == "" || seen[normalized] {
			return ErrInvalid
		}
		seen[normalized] = true
	}
	seenIDs := map[string]bool{}
	for _, id := range e.RelatedIDs {
		if !validOpaqueID(id) || id == e.ID || seenIDs[id] {
			return ErrInvalid
		}
		seenIDs[id] = true
	}
	for _, id := range e.ActivitySourceIDs {
		if !validSourceID(id) || seenIDs[id] {
			return ErrInvalid
		}
		seenIDs[id] = true
	}
	seenIDs = map[string]bool{}
	for _, fact := range e.Facts {
		if !validOpaqueID(fact.ID) || seenIDs[fact.ID] || !validText(fact.Text, MaxTextBytes) || !validState(fact.State) || fact.State == Merged || !validSources(fact.Sources) {
			return ErrInvalid
		}
		seenIDs[fact.ID] = true
	}
	for _, loop := range e.OpenLoops {
		if !validOpaqueID(loop.ID) || seenIDs[loop.ID] || !validText(loop.Text, MaxTextBytes) || !validState(loop.State) || loop.State == Merged || !validSources(loop.Sources) {
			return ErrInvalid
		}
		seenIDs[loop.ID] = true
	}
	return nil
}

// NormalizeExact lowercases and folds runs of Unicode whitespace. The durable
// exact index HMACs this value; it never stores the normalized text on disk.
func NormalizeExact(value string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.TrimSpace(value) {
		if unicode.IsSpace(r) {
			space = b.Len() > 0
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

func Clone(e Entity) Entity {
	e.Aliases = append([]string(nil), e.Aliases...)
	e.RelatedIDs = append([]string(nil), e.RelatedIDs...)
	e.ActivitySourceIDs = append([]string(nil), e.ActivitySourceIDs...)
	e.Facts = append([]Fact(nil), e.Facts...)
	for i := range e.Facts {
		e.Facts[i].Sources = append([]Source(nil), e.Facts[i].Sources...)
	}
	e.OpenLoops = append([]OpenLoop(nil), e.OpenLoops...)
	for i := range e.OpenLoops {
		e.OpenLoops[i].Sources = append([]Source(nil), e.OpenLoops[i].Sources...)
	}
	return e
}

func SortEntities(entities []Entity) {
	sort.Slice(entities, func(i, j int) bool { return entities[i].ID < entities[j].ID })
}
