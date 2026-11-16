package capture

import (
	"encoding/hex"
	"errors"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/privacy"
)

const MaxCapturePartTextBytes = 32 << 10
const MaxCapturePartOrigins = 256
const MaxAuthenticatedCapturePartBytes = 512 << 10
const MaxAuthenticatedCaptureHeaderBytes = 4096
const MaxAuthenticatedCaptureSummaryBytes = 8192

// CaptureCoverage describes how much of the source was read for a capture.
type CaptureCoverage uint8

const (
	CaptureComplete CaptureCoverage = iota + 1
	CaptureStructureLimited
)

var ErrCaptureInvalid = errors.New("capture: invalid capture")
var ErrCaptureClosed = errors.New("capture: capture closed")
var ErrCaptureBusy = errors.New("capture: capture operation in progress")
var ErrCaptureOutput = errors.New("capture: capture output not accepted")

// CaptureHeader identifies saved content from a page, note, or app activity.
type CaptureHeader struct {
	appID, domain, sourceKey, familyID, captureID string
	at                                            time.Time
	admission                                     bool
	redactionVersion                              int
}

func (h CaptureHeader) AppID() string         { return h.appID }
func (h CaptureHeader) Domain() string        { return h.domain }
func (h CaptureHeader) SourceKey() string     { return h.sourceKey }
func (h CaptureHeader) FamilyID() string      { return h.familyID }
func (h CaptureHeader) CaptureID() string     { return h.captureID }
func (h CaptureHeader) CapturedAt() time.Time { return h.at }
func (h CaptureHeader) RedactionVersion() int { return h.redactionVersion }
func (h CaptureHeader) InitialRoot() string {
	if h.Validate() != nil {
		return ""
	}
	return captureHash("blur.document.chain.v1", struct {
		FamilyID         string
		RedactionVersion int
	}{h.familyID, h.redactionVersion})
}
func (h CaptureHeader) Validate() error {
	if len(h.appID) > 128 || len(h.domain) > 253 || h.redactionVersion != privacy.StreamVersion || !captureHashID(h.sourceKey) || !captureHashID(h.captureID) || !captureHashID(h.familyID) || h.at.IsZero() || !time.Unix(0, h.at.UnixNano()).UTC().Equal(h.at) {
		return ErrCaptureInvalid
	}
	app, appOK := normalizeAppID(h.appID)
	domain, domainOK := normalizeDomain(h.domain)
	if !appOK || !domainOK || app != h.appID || domain != h.domain || h.familyID != captureFamilyID(app, domain, h.sourceKey) {
		return ErrCaptureInvalid
	}
	return nil
}
func (h CaptureHeader) ValidateForAdmission() error {
	if !h.admission {
		return ErrCaptureInvalid
	}
	return h.Validate()
}

type CapturePart struct {
	captureID, digest, text string
	index, offset           int64
	origins                 []privacy.OriginSpan
	admission               bool
}

func (p CapturePart) CaptureID() string { return p.captureID }
func (p CapturePart) Digest() string    { return p.digest }
func (p CapturePart) Text() string      { return p.text }
func (p CapturePart) Index() int64      { return p.index }
func (p CapturePart) Offset() int64     { return p.offset }
func (p CapturePart) Origins() []privacy.OriginSpan {
	return append([]privacy.OriginSpan(nil), p.origins...)
}
func (p CapturePart) Validate() error {
	if !captureHashID(p.captureID) || !captureHashID(p.digest) || p.index < 0 || p.offset < 0 || len(p.text) == 0 || len(p.text) > MaxCapturePartTextBytes || p.offset > math.MaxInt64-int64(len(p.text)) || len(p.origins) == 0 || len(p.origins) > MaxCapturePartOrigins || !utf8.ValidString(p.text) || strings.ContainsRune(p.text, 0) {
		return ErrCaptureInvalid
	}
	position := p.offset
	for _, span := range p.origins {
		if span.OutputStart != position || span.OutputEnd <= position || span.OutputEnd > p.offset+int64(len(p.text)) || span.InputStart < 0 || span.InputEnd <= span.InputStart || span.Exact && (span.OpenEnded || span.InputEnd-span.InputStart != span.OutputEnd-span.OutputStart) || !utf8.ValidString(p.text[:span.OutputEnd-p.offset]) {
			return ErrCaptureInvalid
		}
		position = span.OutputEnd
	}
	if position != p.offset+int64(len(p.text)) || p.digest != capturePartDigest(p) {
		return ErrCaptureInvalid
	}
	return nil
}
func (p CapturePart) ValidateForAdmission() error {
	if !p.admission {
		return ErrCaptureInvalid
	}
	return p.Validate()
}
func (p CapturePart) String() string   { return "capture: sanitized capture part" }
func (p CapturePart) GoString() string { return p.String() }

type CaptureSummary struct {
	header                 CaptureHeader
	contentID, root        string
	parts, bytes, rawBytes int64
	coverage               CaptureCoverage
	counts                 []privacy.Count
	admission              bool
}

func (s CaptureSummary) Header() CaptureHeader     { return s.header }
func (s CaptureSummary) ContentID() string         { return s.contentID }
func (s CaptureSummary) Root() string              { return s.root }
func (s CaptureSummary) PartCount() int64          { return s.parts }
func (s CaptureSummary) Bytes() int64              { return s.bytes }
func (s CaptureSummary) RawBytes() int64           { return s.rawBytes }
func (s CaptureSummary) Coverage() CaptureCoverage { return s.coverage }
func (s CaptureSummary) Counts() []privacy.Count   { return copyCounts(s.counts) }
func (s CaptureSummary) Validate() error {
	if s.header.Validate() != nil || !captureHashID(s.contentID) || !captureHashID(s.root) || s.parts <= 0 || s.bytes <= 0 || s.rawBytes <= 0 || s.parts > s.bytes || (s.bytes-1)/MaxCapturePartTextBytes+1 > s.parts || s.coverage != CaptureComplete && s.coverage != CaptureStructureLimited || len(s.counts) > 32 {
		return ErrCaptureInvalid
	}
	last := ""
	for _, count := range s.counts {
		if !validRedactionRule(count.Rule) || count.Rule <= last || count.Count <= 0 {
			return ErrCaptureInvalid
		}
		last = count.Rule
	}
	if s.contentID != captureSummaryID(s) {
		return ErrCaptureInvalid
	}
	return nil
}
func (s CaptureSummary) ValidateForAdmission() error {
	if !s.admission || !s.header.admission {
		return ErrCaptureInvalid
	}
	return s.Validate()
}
func AdvanceCaptureRoot(root string, p CapturePart) (string, error) {
	if !captureHashID(root) || p.Validate() != nil {
		return "", ErrCaptureInvalid
	}
	return captureHash("blur.document.link.v1", [2]string{root, p.digest}), nil
}
func captureHashID(s string) bool {
	if len(s) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(s)
	return err == nil && hex.EncodeToString(decoded) == s
}
