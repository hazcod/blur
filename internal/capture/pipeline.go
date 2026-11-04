// Package capture enforces the probe, policy, bounded read, and redaction boundary.
package capture

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nielsgroen/blur/internal/platform"
	"github.com/nielsgroen/blur/internal/privacy"
)

type Limits struct {
	MaxBytes    int
	MaxNodes    int
	MaxDepth    int
	MaxDuration time.Duration
}

func (l Limits) valid() bool {
	return l.MaxBytes > 0 && l.MaxNodes > 0 && l.MaxDepth > 0 && l.MaxDuration > 0
}

type Exclusions struct {
	Apps    []string
	Domains []string
}

type ErrorCode string

const (
	InvalidLimits     ErrorCode = "invalid_limits"
	InvalidExclusions ErrorCode = "invalid_exclusions"
	Unavailable       ErrorCode = "unavailable"
	ProbeFailed       ErrorCode = "probe_failed"
	Excluded          ErrorCode = "excluded"
	InvalidProbe      ErrorCode = "invalid_probe"
	ReadFailed        ErrorCode = "read_failed"
	LimitExceeded     ErrorCode = "limit_exceeded"
	Cancelled         ErrorCode = "cancelled"
	TimedOut          ErrorCode = "timed_out"
)

type CaptureError struct {
	Code  ErrorCode
	cause error
}

func (e *CaptureError) Error() string              { return "capture: " + string(e.Code) }
func (e *CaptureError) Unwrap() error              { return e.cause }
func captureError(code ErrorCode, err error) error { return &CaptureError{Code: code, cause: err} }
func contextError(err error) error {
	if errors.Is(err, context.Canceled) {
		return captureError(Cancelled, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return captureError(TimedOut, context.DeadlineExceeded)
	}
	return nil
}

// The only capture value allowed to leave this package.
type SanitizedObservation struct {
	ContentID        string          `json:"content_id"`
	AppID            string          `json:"app_id"`
	Domain           string          `json:"domain"`
	Text             string          `json:"text"`
	Truncated        bool            `json:"truncated"`
	RedactionVersion int             `json:"redaction_version"`
	RedactionCounts  []privacy.Count `json:"redaction_counts"`
}

// The frame and node deliberately have no exported representation or JSON tags.
type rawFrame struct{ roots []*rawNode }
type rawNode struct {
	declaredBytes int
	children      []*rawNode
	value         func(context.Context, int) (string, error)
}
type readBounded func(context.Context, platform.CaptureProbe, Limits) (rawFrame, error)

type Pipeline struct {
	Source     platform.CaptureSource
	Read       readBounded
	Limits     Limits
	Exclusions Exclusions
}

func (p Pipeline) Capture(ctx context.Context) (SanitizedObservation, error) {
	var empty SanitizedObservation
	if !p.Limits.valid() {
		return empty, captureError(InvalidLimits, nil)
	}
	exclusions, ok := canonicalExclusions(p.Exclusions)
	if !ok {
		return empty, captureError(InvalidExclusions, nil)
	}
	if err := contextError(ctx.Err()); err != nil {
		return empty, err
	}
	if p.Source == nil || p.Read == nil {
		return empty, captureError(ReadFailed, nil)
	}
	ctx, cancel := context.WithTimeout(ctx, p.Limits.MaxDuration)
	defer cancel()
	report, err := p.Source.Capability(ctx)
	if ce := contextError(ctx.Err()); ce != nil {
		return empty, ce
	}
	if err != nil || report.Validate() != nil || report.ID != "capture" || report.State != platform.Available {
		return empty, captureError(Unavailable, nil)
	}
	probe, err := p.Source.Probe(ctx)
	if ce := contextError(ctx.Err()); ce != nil {
		return empty, ce
	}
	if err != nil {
		return empty, captureError(ProbeFailed, nil)
	}
	app, domain, ok := normalizeProbe(probe)
	if !ok {
		return empty, captureError(InvalidProbe, nil)
	}
	if probe.Secure || probe.Elevated || probe.Private || excluded(app, domain, exclusions) {
		return empty, captureError(Excluded, nil)
	}
	frame, err := p.Read(ctx, probe, p.Limits)
	if ce := contextError(ctx.Err()); ce != nil {
		return empty, ce
	}
	if err != nil {
		return empty, captureError(ReadFailed, nil)
	}
	nodes, err := validateFrame(frame, p.Limits)
	if err != nil {
		return empty, captureError(LimitExceeded, nil)
	}
	var b strings.Builder
	remaining := p.Limits.MaxBytes
	for _, node := range nodes {
		if ce := contextError(ctx.Err()); ce != nil {
			return empty, ce
		}
		if node.value == nil {
			continue
		}
		separator := 0
		if b.Len() > 0 {
			if remaining == 0 {
				return empty, captureError(LimitExceeded, nil)
			}
			remaining--
			separator = 1
		}
		value, readErr := node.value(ctx, remaining)
		if ce := contextError(ctx.Err()); ce != nil {
			return empty, ce
		}
		if readErr != nil {
			return empty, captureError(ReadFailed, nil)
		}
		if !utf8.ValidString(value) {
			return empty, captureError(ReadFailed, nil)
		}
		if len(value) > remaining || len(value) > node.declaredBytes {
			return empty, captureError(LimitExceeded, nil)
		}
		if value == "" {
			remaining += separator
		}
		if separator == 1 && value != "" {
			b.WriteByte('\n')
		}
		remaining -= len(value)
		b.WriteString(value)
	}
	text, counts := privacy.Redact(b.String())
	text, truncated := truncateUTF8(text, p.Limits.MaxBytes)
	obs := SanitizedObservation{AppID: app, Domain: domain, Text: text, Truncated: truncated, RedactionVersion: privacy.Version, RedactionCounts: counts}
	obs.ContentID = contentID(obs)
	return obs, nil
}

func normalizeProbe(probe platform.CaptureProbe) (string, string, bool) {
	if !validTargetID(probe.TargetID) {
		return "", "", false
	}
	app, appOK := normalizeAppID(probe.AppID)
	domain, domainOK := normalizeDomain(probe.Domain)
	if !appOK || !domainOK {
		return "", "", false
	}
	return app, domain, true
}
func validTargetID(id string) bool {
	if id == "" || len(id) > 256 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '.' || c == '_' || c == ':' || c == '-' {
			continue
		}
		return false
	}
	return true
}
func normalizeAppID(raw string) (string, bool) {
	if len(raw) > 128 {
		return "", false
	}
	s := strings.ToLower(strings.TrimSpace(raw))
	return s, identifier(s)
}
func normalizeDomain(raw string) (string, bool) {
	if len(raw) > 253 {
		return "", false
	}
	s := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw), "."))
	return s, identifier(s) && !strings.Contains(s, "..") && !strings.HasPrefix(s, ".") && !strings.HasSuffix(s, ".")
}
func canonicalExclusions(e Exclusions) (Exclusions, bool) {
	var out Exclusions
	for _, raw := range e.Apps {
		app, ok := normalizeAppID(raw)
		if !ok || app == "" {
			return Exclusions{}, false
		}
		out.Apps = append(out.Apps, app)
	}
	for _, raw := range e.Domains {
		domain, ok := normalizeDomain(raw)
		if !ok || domain == "" {
			return Exclusions{}, false
		}
		out.Domains = append(out.Domains, domain)
	}
	return out, true
}
func identifier(s string) bool {
	for _, r := range s {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '.' && r != '-' && r != '_' {
			return false
		}
	}
	return true
}
func excluded(app, domain string, e Exclusions) bool {
	for _, x := range e.Apps {
		if app != "" && app == x {
			return true
		}
	}
	for _, x := range e.Domains {
		if x != "" && domain != "" && (domain == x || strings.HasSuffix(domain, "."+x)) {
			return true
		}
	}
	return false
}

func validateFrame(f rawFrame, l Limits) ([]*rawNode, error) {
	var out []*rawNode
	seen := map[*rawNode]bool{}
	bytes := 0
	var walk func(*rawNode, int) error
	walk = func(n *rawNode, depth int) error {
		if n == nil || seen[n] || depth > l.MaxDepth || len(out) >= l.MaxNodes || n.declaredBytes < 0 || n.declaredBytes > l.MaxBytes-bytes {
			return platform.ErrReadLimit
		}
		seen[n] = true
		bytes += n.declaredBytes
		out = append(out, n)
		for _, child := range n.children {
			if err := walk(child, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	for _, root := range f.roots {
		if err := walk(root, 1); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func truncateUTF8(s string, max int) (string, bool) {
	if len(s) <= max {
		return s, false
	}
	end := max
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end], true
}
func writePart(h hash.Hash, s string) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(s)))
	h.Write(n[:])
	h.Write([]byte(s))
}
func contentID(o SanitizedObservation) string {
	h := sha256.New()
	writePart(h, o.AppID)
	writePart(h, o.Domain)
	writePart(h, o.Text)
	if o.Truncated {
		writePart(h, "1")
	} else {
		writePart(h, "0")
	}
	writePart(h, string(rune(o.RedactionVersion)))
	for _, c := range o.RedactionCounts {
		writePart(h, c.Rule)
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(c.Count))
		h.Write(n[:])
	}
	return hex.EncodeToString(h.Sum(nil))
}
