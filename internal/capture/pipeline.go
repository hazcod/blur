// Package capture enforces the probe, policy, bounded read, and redaction boundary.
package capture

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/appidentity"
	"github.com/hazcod/blur/internal/platform"
	"github.com/hazcod/blur/internal/privacy"
)

var errCaptureDomainChanged = errors.New("capture: active browser domain changed")

type Limits struct {
	MaxBytes    int
	MaxNodes    int
	MaxDepth    int
	MaxDuration time.Duration
}

const (
	MaxCaptureNodes = 4096
	MaxCaptureDepth = 64
)

// DefaultLimits is the production AX budget: bounded enough to cap a single
// event's CPU and memory cost while covering a typical focused window.
func DefaultLimits() Limits {
	return Limits{MaxBytes: 256 << 10, MaxNodes: 2048, MaxDepth: 32, MaxDuration: 750 * time.Millisecond}
}

func (l Limits) valid() bool {
	return l.MaxBytes > 0 && l.MaxBytes <= MaxObservationTextBytes &&
		l.MaxNodes > 0 && l.MaxNodes <= MaxCaptureNodes &&
		l.MaxDepth > 0 && l.MaxDepth <= MaxCaptureDepth &&
		l.MaxDuration > 0 && l.MaxDuration <= time.Minute
}

type Exclusions struct {
	// RequiredApps/RequiredDomains form an additional allowlist. When present,
	// a target must satisfy this guard as well as the user's normal scope.
	RequiredApps    []string
	RequiredDomains []string
	Apps            []string
	Domains         []string
	IncludedApps    []string
	IncludedDomains []string
	DefaultDisabled bool
}

// NormalizeExclusions returns the canonical, case-insensitive form used by
// the capture boundary. Invalid identifiers fail closed instead of being
// silently ignored.
func NormalizeExclusions(exclusions Exclusions) (Exclusions, bool) {
	return canonicalExclusions(exclusions)
}

type ErrorCode string

const (
	InvalidLimits            ErrorCode = "invalid_limits"
	InvalidExclusions        ErrorCode = "invalid_exclusions"
	InvalidProfiles          ErrorCode = "invalid_profiles"
	Unavailable              ErrorCode = "unavailable"
	PermissionRequired       ErrorCode = "permission_required"
	ScreenPermissionRequired ErrorCode = "screen_permission_required"
	ScreenTextUnavailable    ErrorCode = "screen_text_unavailable"
	ScreenNotVisible         ErrorCode = "screen_not_visible"
	BrowserUnavailable       ErrorCode = "browser_content_unavailable"
	DomainUnknown            ErrorCode = "browser_domain_unknown"
	NoContent                ErrorCode = "no_content"
	UserIdle                 ErrorCode = "user_idle"
	ProbeFailed              ErrorCode = "probe_failed"
	Excluded                 ErrorCode = "excluded"
	InvalidProbe             ErrorCode = "invalid_probe"
	ReadFailed               ErrorCode = "read_failed"
	LimitExceeded            ErrorCode = "limit_exceeded"
	Cancelled                ErrorCode = "cancelled"
	TimedOut                 ErrorCode = "timed_out"
	InvalidObservation       ErrorCode = "invalid_observation"
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
		return captureError(Cancelled, err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return captureError(TimedOut, err)
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
	// Focused-element breadcrumb (focus.go). Labels only, never a value; not
	// part of the content ID.
	focusedRole string
	focusedName string
	focusedPath string
}

const MaxObservationTextBytes = 1 << 20

const (
	MaxAuthenticatedObservationBytes = MaxObservationTextBytes + 8192
	observationRecordVersion         = 1
	// observationRecordVersionFocus appends a breadcrumb trailer after the
	// redaction counts. It is written only when a breadcrumb exists, so every
	// record without one stays byte-identical to version 1.
	observationRecordVersionFocus = 2
	observationRecordHeaderBytes  = 49
)

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
	o.focusedRole = strings.Clone(o.focusedRole)
	o.focusedName = strings.Clone(o.focusedName)
	o.focusedPath = strings.Clone(o.focusedPath)
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

// MarshalAuthenticatedObservation encodes a validated observation into the
// deterministic bounded binary form used inside authenticated store records.
func MarshalAuthenticatedObservation(o SanitizedObservation) ([]byte, error) {
	if o.Validate() != nil {
		return nil, captureError(InvalidObservation, nil)
	}
	if o.redactionVersion > 65535 || len(o.redactionCounts) > 32 {
		return nil, captureError(InvalidObservation, nil)
	}

	encodedID, err := hex.DecodeString(o.contentID)
	if err != nil || len(encodedID) != 32 {
		return nil, captureError(InvalidObservation, nil)
	}
	total := observationRecordHeaderBytes + len(o.appID) + len(o.domain) + len(o.text)
	for _, count := range o.redactionCounts {
		total += 5 + len(count.Rule)
	}
	if o.hasFocus() {
		total += 3 + len(o.focusedRole) + len(o.focusedName) + len(o.focusedPath)
	}
	if total > MaxAuthenticatedObservationBytes {
		return nil, captureError(InvalidObservation, nil)
	}

	encoded := make([]byte, total)
	encoded[0] = observationRecordVersion
	if o.hasFocus() {
		encoded[0] = observationRecordVersionFocus
	}
	if o.truncated {
		encoded[1] = 1
	}
	binary.BigEndian.PutUint16(encoded[2:4], uint16(o.redactionVersion))
	binary.BigEndian.PutUint32(encoded[4:8], uint32(len(o.appID)))
	binary.BigEndian.PutUint32(encoded[8:12], uint32(len(o.domain)))
	binary.BigEndian.PutUint32(encoded[12:16], uint32(len(o.text)))
	encoded[16] = byte(len(o.redactionCounts))
	copy(encoded[17:observationRecordHeaderBytes], encodedID)
	clear(encodedID)

	offset := observationRecordHeaderBytes
	copy(encoded[offset:], o.appID)
	offset += len(o.appID)
	copy(encoded[offset:], o.domain)
	offset += len(o.domain)
	copy(encoded[offset:], o.text)
	offset += len(o.text)
	for _, count := range o.redactionCounts {
		encoded[offset] = byte(len(count.Rule))
		binary.BigEndian.PutUint32(encoded[offset+1:offset+5], uint32(count.Count))
		offset += 5
		copy(encoded[offset:], count.Rule)
		offset += len(count.Rule)
	}
	if o.hasFocus() {
		encoded[offset] = byte(len(o.focusedRole))
		encoded[offset+1] = byte(len(o.focusedName))
		encoded[offset+2] = byte(len(o.focusedPath))
		offset += 3
		copy(encoded[offset:], o.focusedRole)
		offset += len(o.focusedRole)
		copy(encoded[offset:], o.focusedName)
		offset += len(o.focusedName)
		copy(encoded[offset:], o.focusedPath)
	}
	return encoded, nil
}

// RestoreAuthenticatedObservation parses only the canonical bounded record
// form. Durable callers must first authenticate its bytes with recordcodec.
func RestoreAuthenticatedObservation(encoded []byte) (SanitizedObservation, error) {
	var empty SanitizedObservation
	invalid := func() (SanitizedObservation, error) {
		return empty, captureError(InvalidObservation, nil)
	}
	if len(encoded) < observationRecordHeaderBytes || len(encoded) > MaxAuthenticatedObservationBytes ||
		(encoded[0] != observationRecordVersion && encoded[0] != observationRecordVersionFocus) || encoded[1]&^byte(1) != 0 {
		return invalid()
	}
	appLength := binary.BigEndian.Uint32(encoded[4:8])
	domainLength := binary.BigEndian.Uint32(encoded[8:12])
	textLength := binary.BigEndian.Uint32(encoded[12:16])
	countLength := int(encoded[16])
	if appLength > 128 || domainLength > 253 || textLength > MaxObservationTextBytes || countLength > 32 {
		return invalid()
	}
	baseLength := uint64(observationRecordHeaderBytes) + uint64(appLength) + uint64(domainLength) + uint64(textLength)
	if baseLength > uint64(len(encoded)) {
		return invalid()
	}

	offset := int(baseLength)
	counts := make([]privacy.Count, 0, countLength)
	for i := 0; i < countLength; i++ {
		if len(encoded)-offset < 5 {
			return invalid()
		}
		ruleLength := int(encoded[offset])
		count := binary.BigEndian.Uint32(encoded[offset+1 : offset+5])
		offset += 5
		if ruleLength == 0 || ruleLength > 64 || count == 0 || count > MaxObservationTextBytes || ruleLength > len(encoded)-offset {
			return invalid()
		}
		ruleBytes := encoded[offset : offset+ruleLength]
		if !utf8.Valid(ruleBytes) {
			return invalid()
		}
		counts = append(counts, privacy.Count{Rule: string(ruleBytes), Count: int(count)})
		offset += ruleLength
	}
	countsEnd := offset
	var focusRole, focusName, focusPath string
	if encoded[0] == observationRecordVersionFocus {
		if len(encoded)-offset < 3 {
			return invalid()
		}
		roleLength, nameLength, pathLength := int(encoded[offset]), int(encoded[offset+1]), int(encoded[offset+2])
		offset += 3
		if roleLength > maxProfileRoleLen || nameLength > MaxFocusFieldBytes || pathLength > MaxFocusFieldBytes || roleLength+nameLength+pathLength != len(encoded)-offset {
			return invalid()
		}
		focusRole = string(encoded[offset : offset+roleLength])
		offset += roleLength
		focusName = string(encoded[offset : offset+nameLength])
		offset += nameLength
		focusPath = string(encoded[offset : offset+pathLength])
		offset += pathLength
	}
	if offset != len(encoded) {
		return invalid()
	}

	appStart := observationRecordHeaderBytes
	domainStart := appStart + int(appLength)
	textStart := domainStart + int(domainLength)
	appBytes := encoded[appStart:domainStart]
	domainBytes := encoded[domainStart:textStart]
	textBytes := encoded[textStart : countsEnd-lenCountsBytes(counts)]
	if !utf8.Valid(appBytes) || !utf8.Valid(domainBytes) || !utf8.Valid(textBytes) {
		return invalid()
	}
	contentID := hex.EncodeToString(encoded[17:observationRecordHeaderBytes])
	o := SanitizedObservation{
		contentID:        contentID,
		appID:            string(appBytes),
		domain:           string(domainBytes),
		text:             string(textBytes),
		truncated:        encoded[1]&1 == 1,
		redactionVersion: int(binary.BigEndian.Uint16(encoded[2:4])),
		redactionCounts:  counts,
		focusedRole:      focusRole,
		focusedName:      focusName,
		focusedPath:      focusPath,
	}
	if o.Validate() != nil {
		return invalid()
	}
	canonical, err := MarshalAuthenticatedObservation(o)
	if err != nil || !bytes.Equal(canonical, encoded) {
		clear(canonical)
		return invalid()
	}
	clear(canonical)
	return o, nil
}

func lenCountsBytes(counts []privacy.Count) int {
	total := 0
	for _, count := range counts {
		total += 5 + len(count.Rule)
	}
	return total
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
	// Every field above is derived from canonical validated input and this
	// package's redactor. Keep the independent redaction/fixed-point check in
	// Validate at storage and pipeline boundaries rather than paying for it
	// again while constructing the value.
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
	if !validFocusFields(o.focusedRole, o.focusedName, o.focusedPath) {
		return invalid()
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
type rawFrame struct {
	roots          []*rawNode
	truncated      bool
	cleanup        func()
	nativeRootID   uintptr
	domain         string
	domainKnown    bool
	validateDomain func(context.Context, string) error
	validate       func(context.Context) error
	visual         func(context.Context, int, visualAdmission) (string, bool, error)
	browserRegion  visualRegionVerifier
	// focus optionally reads the focused-element breadcrumb (focus.go). It is
	// best effort and never fails a capture.
	focus func(context.Context) (rawFocus, bool)
}
type rawNode struct {
	role          string
	sensitive     bool
	declaredBytes int
	boundedValue  bool
	children      []*rawNode
	value         func(context.Context, int) (string, error)
}
type readBounded func(context.Context, platform.CaptureProbe, Limits) (rawFrame, error)

type Pipeline struct {
	Source     platform.CaptureSource
	Read       readBounded
	Limits     Limits
	Exclusions Exclusions
	Profiles   []AppProfile
	// CurrentExclusions rechecks live preferences at read and publication
	// boundaries. Persistence must additionally linearize policy with its write.
	CurrentExclusions func(context.Context) (Exclusions, error)
	// VisualAdmission linearizes the final native acquisition with preference
	// changes. The operation is bounded by the existing visual-stage context.
	VisualAdmission func(context.Context, string, string, func(context.Context) error) error
}

func (p Pipeline) Capture(ctx context.Context) (SanitizedObservation, error) {
	var empty SanitizedObservation
	if !p.Limits.valid() {
		return empty, captureError(InvalidLimits, nil)
	}
	parent := ctx
	exclusions, ok := canonicalExclusions(p.Exclusions)
	if !ok {
		return empty, captureError(InvalidExclusions, nil)
	}
	profileList, ok := profilesWithBuiltins(p.Profiles)
	if !ok {
		return empty, captureError(InvalidProfiles, nil)
	}
	profiles, ok := validateProfiles(profileList)
	if !ok {
		return empty, captureError(InvalidProfiles, nil)
	}
	if err := contextError(ctx.Err()); err != nil {
		return empty, err
	}
	if p.Source == nil || p.Read == nil {
		return empty, captureError(ReadFailed, nil)
	}
	if p.CurrentExclusions != nil {
		current, err := p.CurrentExclusions(ctx)
		if err != nil {
			return empty, captureError(Excluded, err)
		}
		exclusions, ok = canonicalExclusions(current)
		if !ok {
			return empty, captureError(InvalidExclusions, nil)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, p.Limits.MaxDuration)
	defer cancel()
	report, err := p.Source.Capability(ctx)
	if ce := contextError(ctx.Err()); ce != nil {
		return empty, contextError(contextualReadError(ReadStageCapability, ctx.Err()))
	}
	if err == nil && report.Validate() == nil && report.ID == "capture" && report.State == platform.PermissionRequired {
		return empty, captureError(PermissionRequired, nil)
	}
	if err != nil || report.Validate() != nil || report.ID != "capture" || report.State != platform.Available {
		return empty, captureError(Unavailable, nil)
	}
	if presence, ok := p.Source.(platform.CapturePresence); ok {
		idle, err := presence.UserIdle(ctx)
		if ce := contextError(ctx.Err()); ce != nil {
			return empty, contextError(contextualReadError(ReadStagePresence, ctx.Err()))
		}
		if err != nil {
			return empty, captureError(Unavailable, nil)
		}
		if idle {
			return empty, captureError(UserIdle, nil)
		}
	}
	probe, err := p.Source.Probe(ctx)
	if ce := contextError(ctx.Err()); ce != nil {
		return empty, contextError(contextualReadError(ReadStageProbe, ctx.Err()))
	}
	if err != nil {
		return empty, captureError(ProbeFailed, nil)
	}
	app, domain, ok := normalizeProbe(probe)
	if !ok {
		return empty, captureError(InvalidProbe, nil)
	}
	browser := appidentity.IsBrowserApp(app)
	domainPolicy := len(exclusions.Domains)+len(exclusions.IncludedDomains)+len(exclusions.RequiredDomains) > 0
	mayDiscoverBrowserDomain := browser && domain == "" && domainPolicy
	if probe.Secure || probe.Elevated || probe.Private || excluded(app, domain, exclusions) ||
		(!included(app, domain, exclusions) && !mayDiscoverBrowserDomain) {
		return empty, captureError(Excluded, nil)
	}
	frame, err := p.Read(ctx, probe, p.Limits)
	if frame.cleanup != nil {
		defer frame.cleanup()
	}
	if ce := contextError(ctx.Err()); ce != nil {
		if acquisitionErr := contextError(err); acquisitionErr != nil && errors.Is(err, ctx.Err()) && ReadIssueForError(acquisitionErr).Valid() {
			return empty, acquisitionErr
		}
		return empty, ce
	}
	if err != nil {
		if ce := contextError(err); ce != nil {
			return empty, ce
		}
		if errors.Is(err, errBrowserContentUnavailable) {
			return empty, captureError(BrowserUnavailable, nil)
		}
		return empty, captureError(ReadFailed, err)
	}
	if frame.domainKnown {
		domain, ok = normalizeDomain(frame.domain)
		if !ok || domain == "" {
			return empty, captureError(Excluded, nil)
		}
	} else if domain == "" {
		domain, _ = normalizeDomain(probe.Domain)
	}
	if browser && domainPolicy && domain == "" {
		return empty, captureError(DomainUnknown, nil)
	}
	if excluded(app, domain, exclusions) || !included(app, domain, exclusions) {
		return empty, captureError(Excluded, nil)
	}
	checkPolicy := func(checkCtx context.Context) error {
		if err := checkCtx.Err(); err != nil {
			return contextError(err)
		}
		if p.CurrentExclusions == nil {
			return nil
		}
		current, err := p.CurrentExclusions(checkCtx)
		if err != nil {
			return captureError(Excluded, err)
		}
		if !TargetAllowed(current, app, domain) {
			return captureError(Excluded, nil)
		}
		return nil
	}
	if err := checkPolicy(ctx); err != nil {
		return empty, err
	}
	nodes, err := validateFrame(frame, p.Limits)
	if err != nil {
		return empty, captureError(LimitExceeded, nil)
	}
	if frame.validate != nil {
		if err := frame.validate(ctx); err != nil {
			if ce := contextError(err); ce != nil {
				return empty, ce
			}
			return empty, captureError(Excluded, nil)
		}
	}
	var b strings.Builder
	visualFallbackNeeded := false
	remaining := p.Limits.MaxBytes
	for _, node := range nodes {
		if ce := contextError(ctx.Err()); ce != nil {
			return empty, ce
		}
		if node.sensitive || node.value == nil || ignoredAXRole(app, node.role, profiles) {
			continue
		}
		if (domainPolicy || frame.domainKnown) && frame.validateDomain != nil {
			if err := frame.validateDomain(ctx, domain); err != nil {
				if ce := contextError(err); ce != nil {
					return empty, ce
				}
				return empty, captureError(Excluded, nil)
			}
		}
		separator := 0
		if b.Len() > 0 {
			if remaining <= 1 {
				// Keep validated content already read. Continuation owns any remaining
				// text; reporting a read failure here discarded useful evidence.
				frame.truncated = true
				break
			}
			remaining--
			separator = 1
		}
		value, readErr := node.value(ctx, remaining)
		if ce := contextError(ctx.Err()); ce != nil {
			if acquisitionErr := contextError(readErr); acquisitionErr != nil && errors.Is(readErr, ctx.Err()) && ReadIssueForError(acquisitionErr).Valid() {
				issue := ReadIssueForError(acquisitionErr)
				if frame.visual != nil && recoverableVisualAXTextIssue(issue) && parent.Err() == nil {
					visualFallbackNeeded = true
					break
				}
				return empty, acquisitionErr
			}
			return empty, ce
		}
		if errors.Is(readErr, errAXValueTruncated) {
			frame.truncated = true
			readErr = nil
		}
		if readErr != nil {
			// A complete, policy-validated AX tree can still have unreadable text
			// values in apps with flaky AX providers. The guarded visual path repeats
			// the privacy/focus checks before OCR, so use it only for text-value errors.
			if frame.visual != nil && recoverableVisualAXTextIssue(ReadIssueForError(readErr)) {
				visualFallbackNeeded = true
				break
			}
			return empty, captureError(ReadFailed, readErr)
		}
		if (domainPolicy || frame.domainKnown) && frame.validateDomain != nil {
			if err := frame.validateDomain(ctx, domain); err != nil {
				if ce := contextError(err); ce != nil {
					return empty, ce
				}
				return empty, captureError(Excluded, nil)
			}
		}
		if !utf8.ValidString(value) {
			return empty, captureError(ReadFailed, nil)
		}
		if len(value) > remaining || (!node.boundedValue && len(value) > node.declaredBytes) {
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
	if frame.validate != nil {
		if err := frame.validate(ctx); err != nil {
			if ce := contextError(err); ce != nil {
				return empty, ce
			}
			return empty, captureError(Excluded, nil)
		}
	}
	if (strings.TrimSpace(b.String()) == "" || visualFallbackNeeded) && frame.visual != nil {
		// Visual recognition is a separate bounded stage; never extend the AX
		// deadline or detach the original caller's cancellation.
		// Whole-window OCR would include browser tabs, URL paths and other chrome
		// excluded by the verified AX WebArea reader. No pixel fallback until an
		// exact page crop has equivalent authority.
		if browser && frame.browserRegion == nil {
			return empty, captureError(BrowserUnavailable, nil)
		}
		if !visualFrameSafe(frame, nodes) {
			return empty, captureError(Excluded, nil)
		}
		visualCtx, visualCancel := context.WithTimeout(parent, MaxVisualDuration)
		defer visualCancel()
		ctx = visualCtx
		verify := func() error {
			if err := checkPolicy(ctx); err != nil {
				return err
			}
			if err := frame.validate(ctx); err != nil {
				if ce := contextError(err); ce != nil {
					return ce
				}
				return captureError(Excluded, nil)
			}
			if frame.validateDomain != nil && (domainPolicy || frame.domainKnown) {
				if err := frame.validateDomain(ctx, domain); err != nil {
					if ce := contextError(err); ce != nil {
						return ce
					}
					return captureError(Excluded, nil)
				}
			}
			return nil
		}
		if err := verify(); err != nil {
			return empty, err
		}
		admit := func(checkCtx context.Context, read func(context.Context) error) error {
			if err := checkPolicy(checkCtx); err != nil {
				return err
			}
			if p.VisualAdmission != nil {
				return p.VisualAdmission(checkCtx, app, domain, read)
			}
			return read(checkCtx)
		}
		text, _, err := frame.visual(ctx, p.Limits.MaxBytes, admit)
		if ce := contextError(ctx.Err()); ce != nil {
			return empty, ce
		}
		if err != nil {
			return empty, visualCaptureError(err)
		}
		if err := verify(); err != nil {
			return empty, err
		}
		if !utf8.ValidString(text) || len(text) > p.Limits.MaxBytes {
			return empty, captureError(LimitExceeded, nil)
		}
		b.Reset()
		b.WriteString(text)
		// OCR represents visible text only and can omit/misrecognize characters.
		frame.truncated = true
	}
	if err := checkPolicy(ctx); err != nil {
		return empty, err
	}
	observation, err := SanitizeObservation(app, domain, b.String(), p.Limits.MaxBytes)
	if err != nil {
		return empty, err
	}
	if frame.truncated && !observation.truncated {
		observation.truncated = true
		observation.contentID = contentID(observation)
	}
	if err := observation.Validate(); err != nil {
		return empty, err
	}
	if strings.TrimSpace(observation.Text()) == "" {
		return empty, captureError(NoContent, nil)
	}
	// Breadcrumb last: it only labels where the text was seen, runs under its
	// own 200 ms budget, and any failure leaves the fields empty.
	if role, name, path := readFocusForCapture(parent, frame.focus); role != "" || name != "" || path != "" {
		// A breadcrumb that does not validate is dropped; it never fails a capture.
		if labelled := observation.withFocusRaw(role, name, path); labelled.Validate() == nil {
			observation = labelled
		}
	}
	return observation, nil
}

func recoverableVisualAXTextIssue(issue ReadIssue) bool {
	return issue.Stage == ReadStageText || issue.Stage == ReadStageRange
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

// domainFromDocumentURL retains only a normalized DNS host. The URL and its
// path, query, fragment, and credentials remain transient and are never logged
// or added to an observation.
func domainFromDocumentURL(raw string) (string, bool) {
	if raw == "" || len(raw) > 2048 {
		return "", false
	}
	document, err := url.Parse(raw)
	if err != nil || document.Opaque != "" || document.Host == "" {
		return "", false
	}
	switch strings.ToLower(document.Scheme) {
	case "http", "https":
	default:
		return "", false
	}
	host := strings.ToLower(strings.TrimSuffix(document.Hostname(), "."))
	domain, ok := normalizeDomain(host)
	return domain, ok && domain != ""
}

func canonicalExclusions(e Exclusions) (Exclusions, bool) {
	var out Exclusions
	out.DefaultDisabled = e.DefaultDisabled
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
	for _, raw := range e.IncludedApps {
		app, ok := normalizeAppID(raw)
		if !ok || app == "" {
			return Exclusions{}, false
		}
		out.IncludedApps = append(out.IncludedApps, app)
	}
	for _, raw := range e.IncludedDomains {
		domain, ok := normalizeDomain(raw)
		if !ok || domain == "" {
			return Exclusions{}, false
		}
		out.IncludedDomains = append(out.IncludedDomains, domain)
	}
	for _, raw := range e.RequiredApps {
		app, ok := normalizeAppID(raw)
		if !ok || app == "" {
			return Exclusions{}, false
		}
		out.RequiredApps = append(out.RequiredApps, app)
	}
	for _, raw := range e.RequiredDomains {
		domain, ok := normalizeDomain(raw)
		if !ok || domain == "" {
			return Exclusions{}, false
		}
		out.RequiredDomains = append(out.RequiredDomains, domain)
	}
	return out, true
}
func identifier(s string) bool {
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '.' && r != '-' && r != '_' {
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

// included applies the positive allowlist only in default-disabled mode. A
// matching app or domain is sufficient; deny rules are checked first.
func included(app, domain string, e Exclusions) bool {
	if len(e.RequiredApps)+len(e.RequiredDomains) > 0 {
		admitted := false
		for _, required := range e.RequiredApps {
			admitted = admitted || app == required
		}
		for _, required := range e.RequiredDomains {
			admitted = admitted || domain == required || strings.HasSuffix(domain, "."+required)
		}
		if !admitted {
			return false
		}
	}
	if !e.DefaultDisabled {
		return true
	}
	for _, x := range e.IncludedApps {
		if app != "" && app == x {
			return true
		}
	}
	for _, x := range e.IncludedDomains {
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
		if n == nil || seen[n] || depth > l.MaxDepth || len(out) >= l.MaxNodes ||
			(n.boundedValue && (n.declaredBytes != -1 || n.value == nil && !n.sensitive)) ||
			(!n.boundedValue && (n.declaredBytes < 0 || n.declaredBytes > l.MaxBytes-bytes)) ||
			n.role != "" && !validAXRole(n.role) {
			return platform.ErrReadLimit
		}
		seen[n] = true
		if !n.boundedValue {
			bytes += n.declaredBytes
		}
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

func cloneExclusions(e Exclusions) Exclusions {
	e.Apps = append([]string(nil), e.Apps...)
	e.Domains = append([]string(nil), e.Domains...)
	e.IncludedApps = append([]string(nil), e.IncludedApps...)
	e.IncludedDomains = append([]string(nil), e.IncludedDomains...)
	e.RequiredApps = append([]string(nil), e.RequiredApps...)
	e.RequiredDomains = append([]string(nil), e.RequiredDomains...)
	return e
}

// MaxVisualDuration is separate from the 750ms Accessibility read budget.
const MaxVisualDuration = 3 * time.Second

func visualFrameSafe(frame rawFrame, nodes []*rawNode) bool {
	if frame.truncated || frame.validate == nil || len(nodes) == 0 {
		return false
	}
	for _, node := range nodes {
		if node.sensitive || node.role == "" || node.role == "AXUnknown" {
			return false
		}
	}
	return true
}
