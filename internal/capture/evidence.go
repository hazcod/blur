package capture

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strings"

	"github.com/hazcod/blur/internal/privacy"
)

// Evidence is an immutable sanitized source value. Legacy observation IDs retain
// their meaning; multipart source IDs bind one part to one live publication.
// No constructor creates a SanitizedObservation with a multipart identity.
type Evidence struct {
	observation SanitizedObservation
	summary     CaptureSummary
	part        CapturePart
	publication string
	kind        uint8
}

// EvidenceProvenance links local text offsets to its complete saved capture.
// Start and End are UTF-8 byte offsets in that capture's sanitized text.
type EvidenceProvenance struct {
	EditionID             string
	FamilyID              string
	CaptureID             string
	Publication           string
	PartIndex             int64
	PartCount, TotalBytes int64
	Start, End            int64
	Coverage              CaptureCoverage
}

func ObservationEvidence(observation SanitizedObservation) (Evidence, error) {
	if err := observation.Validate(); err != nil {
		return Evidence{}, err
	}
	return Evidence{observation: observation.Clone(), kind: 1}, nil
}

// CapturePartEvidence preserves authenticated part identity and text. Storage
// additionally verifies membership against the live edition's ordered chain
// before returning this value; structurally valid detached parts are not reads.
func CapturePartEvidence(summary CaptureSummary, part CapturePart, publication string) (Evidence, error) {
	e := Evidence{summary: summary, part: part, publication: publication, kind: 2}
	if err := e.Validate(); err != nil {
		return Evidence{}, err
	}
	return e.Clone(), nil
}

// CapturePartSourceID is the existing publication-scoped multipart identity.
func CapturePartSourceID(edition, publication string, index int64) string {
	if !captureHashID(edition) || !captureHashID(publication) || index < 0 {
		return ""
	}
	var ordinal [8]byte
	binary.BigEndian.PutUint64(ordinal[:], uint64(index))
	h := sha256.New()
	h.Write([]byte("blur.capture.part-source.v1\x00"))
	h.Write([]byte(edition))
	h.Write([]byte(publication))
	h.Write(ordinal[:])
	return hex.EncodeToString(h.Sum(nil))
}
func (e Evidence) SourceID() string {
	if e.kind == 1 {
		return e.observation.ContentID()
	}
	if e.kind == 2 {
		return CapturePartSourceID(e.summary.ContentID(), e.publication, e.part.Index())
	}
	return ""
}
func (e Evidence) AppID() string {
	if e.kind == 1 {
		return e.observation.AppID()
	}
	return e.summary.Header().AppID()
}
func (e Evidence) Domain() string {
	if e.kind == 1 {
		return e.observation.Domain()
	}
	return e.summary.Header().Domain()
}
func (e Evidence) Text() string {
	if e.kind == 1 {
		return e.observation.Text()
	}
	return e.part.Text()
}
func (e Evidence) Truncated() bool {
	if e.kind == 1 {
		return e.observation.Truncated()
	}
	return e.summary.Coverage() != CaptureComplete
}
func (e Evidence) RedactionVersion() int {
	if e.kind == 1 {
		return e.observation.RedactionVersion()
	}
	return e.summary.Header().RedactionVersion()
}
func (e Evidence) RedactionCounts() []privacy.Count {
	if e.kind == 1 {
		return e.observation.RedactionCounts()
	}
	return e.summary.Counts()
}
func (e Evidence) Origins() []privacy.OriginSpan {
	if e.kind == 2 {
		return e.part.Origins()
	}
	return nil
}
func (e Evidence) Provenance() (EvidenceProvenance, bool) {
	if e.kind != 2 {
		return EvidenceProvenance{}, false
	}
	h := e.summary.Header()
	return EvidenceProvenance{EditionID: e.summary.ContentID(), FamilyID: h.FamilyID(), CaptureID: h.CaptureID(), Publication: e.publication, PartIndex: e.part.Index(), PartCount: e.summary.PartCount(), TotalBytes: e.summary.Bytes(), Start: e.part.Offset(), End: e.part.Offset() + int64(len(e.part.Text())), Coverage: e.summary.Coverage()}, true
}
func (e Evidence) Validate() error {
	switch e.kind {
	case 1:
		return e.observation.Validate()
	case 2:
		if e.summary.Validate() != nil || e.part.Validate() != nil || !captureHashID(e.publication) || e.part.CaptureID() != e.summary.Header().CaptureID() || e.part.Index() >= e.summary.PartCount() || e.part.Offset()+int64(len(e.part.Text())) > e.summary.Bytes() || e.part.Index() == 0 && e.part.Offset() != 0 || e.part.Index() == e.summary.PartCount()-1 && e.part.Offset()+int64(len(e.part.Text())) != e.summary.Bytes() {
			return ErrCaptureInvalid
		}
		return nil
	default:
		return ErrCaptureInvalid
	}
}
func (e Evidence) Clone() Evidence {
	e.observation = e.observation.Clone()
	e.summary.counts = copyCounts(e.summary.counts)
	e.part.origins = append([]privacy.OriginSpan(nil), e.part.origins...)
	e.publication = strings.Clone(e.publication)
	return e
}
func (Evidence) String() string     { return "capture: sanitized source evidence" }
func (e Evidence) GoString() string { return e.String() }
