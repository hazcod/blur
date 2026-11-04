// Package capture enforces the probe, policy, bounded read, and redaction boundary.
package capture

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
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
	return l.MaxBytes > 0 && l.MaxBytes <= MaxObservationTextBytes && l.MaxNodes > 0 && l.MaxDepth > 0 && l.MaxDuration > 0
}

type Exclusions struct {
	Apps    []string
	Domains []string
}

type ErrorCode string

const (
	InvalidLimits      ErrorCode = "invalid_limits"
	InvalidExclusions  ErrorCode = "invalid_exclusions"
	Unavailable        ErrorCode = "unavailable"
	ProbeFailed        ErrorCode = "probe_failed"
	Excluded           ErrorCode = "excluded"
	InvalidProbe       ErrorCode = "invalid_probe"
	ReadFailed         ErrorCode = "read_failed"
	LimitExceeded      ErrorCode = "limit_exceeded"
	Cancelled          ErrorCode = "cancelled"
	TimedOut           ErrorCode = "timed_out"
	InvalidObservation ErrorCode = "invalid_observation"
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

// The only capture value allowed to leave this package. Its fields are private
// so only the capture pipeline or sanitizer can derive counts and identity.
type SanitizedObservation struct {
	contentID        string
	appID            string
	domain           string
	text             string
	truncated        bool
	redactionVersion int
	redactionCounts  []privacy.Count
}

const MaxObservationTextBytes = 1 << 20

func (o SanitizedObservation) ContentID() string                { return strings.Clone(o.contentID) }
func (o SanitizedObservation) AppID() string                    { return strings.Clone(o.appID) }
func (o SanitizedObservation) Domain() string                   { return strings.Clone(o.domain) }
func (o SanitizedObservation) Text() string                     { return strings.Clone(o.text) }
func (o SanitizedObservation) Truncated() bool                  { return o.truncated }
func (o SanitizedObservation) RedactionVersion() int            { return o.redactionVersion }
func (o SanitizedObservation) RedactionCounts() []privacy.Count { return copyCounts(o.redactionCounts) }
func copyCounts(in []privacy.Count) []privacy.Count {
	if in == nil {
		return nil
	}
	out := make([]privacy.Count, len(in))
	for i, c := range in {
		out[i] = privacy.Count{Rule: strings.Clone(c.Rule), Count: c.Count}
	}
	return out
}

// Clone returns a fully independent value for store boundaries.
func (o SanitizedObservation) Clone() SanitizedObservation {
	o.contentID = strings.Clone(o.contentID)
	o.appID = strings.Clone(o.appID)
	o.domain = strings.Clone(o.domain)
	o.text = strings.Clone(o.text)
	o.redactionCounts = copyCounts(o.redactionCounts)
	return o
}

// MarshalJSON exposes only sanitized, bounded fields. JSON cannot construct a
// trusted value: authenticated bounded wire decoding is deferred to Task 6+.
func (o SanitizedObservation) MarshalJSON() ([]byte, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		ContentID        string          `json:"content_id"`
		AppID            string          `json:"app_id"`
		Domain           string          `json:"domain"`
		Text             string          `json:"text"`
		Truncated        bool            `json:"truncated"`
		RedactionVersion int             `json:"redaction_version"`
		RedactionCounts  []privacy.Count `json:"redaction_counts"`
	}{o.contentID, o.appID, o.domain, o.text, o.truncated, o.redactionVersion, o.redactionCounts})
}
func (*SanitizedObservation) UnmarshalJSON([]byte) error {
	return captureError(InvalidObservation, nil)
}

// SanitizeObservation is a bounded capture-owned constructor for non-pipeline
// in-process callers. It never accepts a caller-provided count or content ID.
func SanitizeObservation(appID, domain, rawText string, maxBytes int) (SanitizedObservation, error) {
	var empty SanitizedObservation
	if maxBytes <= 0 || maxBytes > MaxObservationTextBytes || len(appID) > 128 || len(domain) > 253 || len(rawText) > maxBytes || !utf8.ValidString(appID) || !utf8.ValidString(domain) || !utf8.ValidString(rawText) || strings.ContainsRune(rawText, 0) {
		return empty, captureError(InvalidObservation, nil)
	}
	app, appOK := normalizeAppID(appID)
	host, hostOK := normalizeDomain(domain)
	if !appOK || !hostOK {
		return empty, captureError(InvalidObservation, nil)
	}
	for _, part := range []string{app, host} {
		redacted, _ := privacy.Redact(part)
		if redacted != part {
			return empty, captureError(InvalidObservation, nil)
		}
	}
	text, counts := privacy.Redact(rawText)
	text, truncated := truncateUTF8(text, maxBytes)
	if truncated {
		text = safeTruncatedText(text)
	}
	o := SanitizedObservation{appID: app, domain: host, text: text, truncated: truncated, redactionVersion: privacy.Version, redactionCounts: copyCounts(counts)}
	o.contentID = contentID(o)
	if err := o.Validate(); err != nil {
		return empty, err
	}
	return o, nil
}

// Validate is the authoritative storage-boundary check. All failures are
// content-free; it enforces the current redactor's fixed point.
func (o SanitizedObservation) Validate() error {
	invalid := func() error { return captureError(InvalidObservation, nil) }
	// Cheap bounds and element checks must precede redaction, hashing, or copy.
	if len(o.contentID) != 64 || len(o.appID) > 128 || len(o.domain) > 253 || len(o.text) > MaxObservationTextBytes || len(o.redactionCounts) > 32 || o.redactionVersion != privacy.Version {
		return invalid()
	}
	last := ""
	for _, count := range o.redactionCounts {
		if len(count.Rule) > 64 || !validRedactionRule(count.Rule) || count.Rule <= last || count.Count <= 0 || count.Count > MaxObservationTextBytes {
			return invalid()
		}
		last = count.Rule
	}
	if !utf8.ValidString(o.appID) || !utf8.ValidString(o.domain) || !utf8.ValidString(o.text) || strings.ContainsRune(o.text, 0) {
		return invalid()
	}
	app, appOK := normalizeAppID(o.appID)
	domain, domainOK := normalizeDomain(o.domain)
	if !appOK || !domainOK || app != o.appID || domain != o.domain {
		return invalid()
	}
	for _, part := range []string{o.appID, o.domain, o.text} {
		redacted, _ := privacy.Redact(part)
		if redacted != part {
			return invalid()
		}
	}
	if o.contentID != contentID(o) {
		return invalid()
	}
	return nil
}

func validRedactionRule(rule string) bool {
	switch rule {
	case "PRIVATE_KEY", "AUTHORIZATION", "URL_CREDENTIAL", "URL_QUERY", "MALFORMED_URL", "SECRET", "SECURITY_CODE", "TOKEN", "SSN", "CARD", "IBAN":
		return true
	default:
		return false
	}
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
	return SanitizeObservation(app, domain, b.String(), p.Limits.MaxBytes)
}

// A byte cap may bisect a redaction placeholder or leave a partial labelled
// value. Back off a bounded number of runes, then drop the prefix if needed.
func safeTruncatedText(text string) string {
	for n := 0; n < 64; n++ {
		redacted, _ := privacy.Redact(text)
		if redacted == text {
			return text
		}
		if text == "" {
			return text
		}
		_, width := utf8.DecodeLastRuneInString(text)
		text = text[:len(text)-width]
	}
	return ""
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
	writePart(h, o.appID)
	writePart(h, o.domain)
	writePart(h, o.text)
	if o.truncated {
		writePart(h, "1")
	} else {
		writePart(h, "0")
	}
	writePart(h, string(rune(o.redactionVersion)))
	for _, c := range o.redactionCounts {
		writePart(h, c.Rule)
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(c.Count))
		h.Write(n[:])
	}
	return hex.EncodeToString(h.Sum(nil))
}
