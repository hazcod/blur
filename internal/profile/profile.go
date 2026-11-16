// Package profile stores explicit user facts and writing rules separately from
// inferred Library claims. Nothing in a capture can create a profile rule.
package profile

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/search"
)

const (
	MaxTextBytes      = 1024
	MaxRecordBytes    = 4096
	MaxPageSize       = 32
	MaxContextBytes   = 2048
	MaxContextRecords = 16
	MaxScanRecords    = 2048
)

var (
	ErrInvalid  = errors.New("profile: invalid record")
	ErrConflict = errors.New("profile: record changed")
	ErrCapacity = errors.New("profile: context capacity reached")
)

type Kind string

const (
	Fact        Kind = "fact"
	WritingRule Kind = "writing_rule"
)

type Scope struct {
	AppID    string `json:"app_id,omitempty"`
	Domain   string `json:"domain,omitempty"`
	Project  string `json:"project,omitempty"`
	Audience string `json:"audience,omitempty"`
}
type Record struct {
	ID           string    `json:"id"`
	Revision     uint64    `json:"revision"`
	Kind         Kind      `json:"kind"`
	Text         string    `json:"text"`
	Scope        Scope     `json:"scope"`
	Enabled      bool      `json:"enabled"`
	UserAuthored bool      `json:"user_authored"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}
type PageRequest struct {
	After string
	Limit int
}
type Page struct {
	Records []Record
	Next    string
	HasMore bool
}
type Repository interface {
	EncryptedAtRest() bool
	PutProfile(context.Context, Record, uint64) error
	GetProfile(context.Context, string) (Record, bool, error)
	ListProfile(context.Context, PageRequest) (Page, error)
	DeleteProfile(context.Context, string, uint64) error
	DeleteAllProfile(context.Context) error
}

func ValidID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, r := range id {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
func ValidText(text string) bool { return clean(text, MaxTextBytes) }
func clean(text string, max int) bool {
	return len(text) > 0 && len(text) <= max && utf8.ValidString(text) && strings.TrimSpace(text) == text && !strings.ContainsRune(text, 0)
}
func validTime(at time.Time) bool { return !at.IsZero() && at.Year() >= 2020 && at.Year() <= 2100 }
func (s Scope) Validate() error {
	if s.AppID != "" {
		app, err := search.CanonicalAppID(s.AppID)
		if err != nil || app != s.AppID {
			return ErrInvalid
		}
	}
	if s.Domain != "" {
		domain, err := search.CanonicalDomain(s.Domain)
		if err != nil || domain != s.Domain {
			return ErrInvalid
		}
	}
	for _, value := range []string{s.Project, s.Audience} {
		if value != "" && (!clean(value, 128) || strings.ContainsAny(value, "\r\n")) {
			return ErrInvalid
		}
	}
	return nil
}
func (r Record) Validate() error {
	if !ValidID(r.ID) || r.Revision == 0 || r.Revision > 1<<48 || r.Kind != Fact && r.Kind != WritingRule || !ValidText(r.Text) || r.Scope.Validate() != nil || !r.UserAuthored || !validTime(r.CreatedAt) || !validTime(r.UpdatedAt) || r.UpdatedAt.Before(r.CreatedAt) {
		return ErrInvalid
	}
	return nil
}

type Context struct{ AppIDs, Domains, Projects, Audiences []string }

func normalized(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}
func includes(values []string, expected string) bool {
	for _, value := range values {
		if normalized(value) == normalized(expected) {
			return true
		}
	}
	return false
}
func (s Scope) Applies(c Context) bool {
	return (s.AppID == "" || includes(c.AppIDs, s.AppID)) && (s.Domain == "" || includes(c.Domains, s.Domain)) && (s.Project == "" || includes(c.Projects, s.Project)) && (s.Audience == "" || includes(c.Audiences, s.Audience))
}
func (s Scope) Specificity() int {
	n := 0
	for _, v := range []string{s.AppID, s.Domain, s.Project, s.Audience} {
		if v != "" {
			n++
		}
	}
	return n
}

// MentionsScope matches complete words, so an Atlas rule never applies merely
// because a request mentions AtlasKit. Explicit current instructions can still
// override a stored writing rule.
func MentionsScope(question, value string) bool {
	q, v := []rune(normalized(question)), []rune(normalized(value))
	if len(v) == 0 {
		return false
	}
	for start := 0; start+len(v) <= len(q); start++ {
		if string(q[start:start+len(v)]) != string(v) {
			continue
		}
		before := start == 0 || !unicode.IsLetter(q[start-1]) && !unicode.IsNumber(q[start-1])
		end := start + len(v)
		after := end == len(q) || !unicode.IsLetter(q[end]) && !unicode.IsNumber(q[end])
		if before && after {
			return true
		}
	}
	return false
}
