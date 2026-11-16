package chronicle

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/capture"
)

const excerptGap = "\n[... middle of saved source not supplied ...]\n"

// sourceExcerpt is a derived model projection, never a SanitizedObservation.
// Its private construction keeps the original source ID attached to exactly
// the sanitized source from which its head and tail were copied.
type sourceExcerpt struct {
	sourceID, appID, domain, text     string
	originalBytes, headEnd, tailStart int
	capturedPartial                   bool
}

// NewEvidence projects a saved observation into a bounded in-memory value.
// Large originals remain inspectable in storage under their unchanged IDs.
func NewEvidence(observation capture.SanitizedObservation, at time.Time, salience uint8) (Evidence, error) {
	if observation.Validate() != nil || at.IsZero() {
		return Evidence{}, ErrInvalid
	}
	item := Evidence{At: at, Salience: salience}
	text := observation.Text()
	if len(text) <= MaxEvidenceItemBytes {
		item.Observation = observation.Clone()
		return item, nil
	}
	item.Observation = observation
	return projectEvidence(item, MaxEvidenceItemBytes), nil
}

// NewSourceEvidence retains a published part's actual identity. It never
// rewrites part text as an observation with an unrelated ContentID.
func NewSourceEvidence(source capture.Evidence, at time.Time, salience uint8) (Evidence, error) {
	if source.Validate() != nil || at.IsZero() {
		return Evidence{}, ErrInvalid
	}
	cloned := source.Clone()
	item := Evidence{At: at, Salience: salience, source: &cloned}
	if len(source.Text()) > MaxEvidenceItemBytes {
		return projectEvidence(item, MaxEvidenceItemBytes), nil
	}
	return item, nil
}

// projectEvidence only removes text already supplied by a trusted observation
// or an earlier projection. Repacking can never retrieve an excluded middle.
func projectEvidence(item Evidence, limit int) Evidence {
	text := item.Text()
	if len(text) <= limit {
		return item.Clone()
	}
	head, tail := text, text
	if item.excerpt != nil {
		head = text[:item.excerpt.headEnd]
		tail = text[len(text)-(item.excerpt.originalBytes-item.excerpt.tailStart):]
	}
	budget := limit - len(excerptGap)
	headEnd := min(budget/2, len(head))
	for headEnd > 0 && headEnd < len(head) && !utf8.RuneStart(head[headEnd]) {
		headEnd--
	}
	tailStart := max(0, len(tail)-(budget-budget/2))
	for tailStart < len(tail) && !utf8.RuneStart(tail[tailStart]) {
		tailStart++
	}
	originalBytes := item.OriginalBytes()
	item.excerpt = &sourceExcerpt{sourceID: item.SourceID(), appID: item.AppID(), domain: item.Domain(), text: head[:headEnd] + excerptGap + tail[tailStart:], originalBytes: originalBytes, headEnd: headEnd, tailStart: originalBytes - (len(tail) - tailStart), capturedPartial: item.CapturedPartial()}
	item.Observation = capture.SanitizedObservation{}
	item.source = nil
	return item
}

func (e Evidence) SourceID() string {
	if e.excerpt != nil {
		return strings.Clone(e.excerpt.sourceID)
	}
	if e.source != nil {
		return e.source.SourceID()
	}
	return e.Observation.ContentID()
}
func (e Evidence) Text() string {
	if e.excerpt != nil {
		return strings.Clone(e.excerpt.text)
	}
	if e.source != nil {
		return e.source.Text()
	}
	return e.Observation.Text()
}
func (e Evidence) AppID() string {
	if e.excerpt != nil {
		return strings.Clone(e.excerpt.appID)
	}
	if e.source != nil {
		return e.source.AppID()
	}
	return e.Observation.AppID()
}
func (e Evidence) Domain() string {
	if e.excerpt != nil {
		return strings.Clone(e.excerpt.domain)
	}
	if e.source != nil {
		return e.source.Domain()
	}
	return e.Observation.Domain()
}
func (e Evidence) CapturedPartial() bool {
	if e.excerpt != nil {
		return e.excerpt.capturedPartial
	}
	if e.source != nil {
		return e.source.Truncated()
	}
	return e.Observation.Truncated()
}
func (e Evidence) Excerpted() bool { return e.excerpt != nil }
func (e Evidence) OriginalBytes() int {
	if e.excerpt != nil {
		return e.excerpt.originalBytes
	}
	if e.source != nil {
		return len(e.source.Text())
	}
	return len(e.Observation.Text())
}

func (e Evidence) Clone() Evidence {
	if e.source != nil {
		source := e.source.Clone()
		e.source = &source
	}
	if e.excerpt == nil {
		e.Observation = e.Observation.Clone()
		return e
	}
	x := *e.excerpt
	x.sourceID = strings.Clone(x.sourceID)
	x.appID = strings.Clone(x.appID)
	x.domain = strings.Clone(x.domain)
	x.text = strings.Clone(x.text)
	e.excerpt = &x
	return e
}

func (e Evidence) valid() bool {
	if !validSourceID(e.SourceID()) {
		return false
	}
	if e.excerpt == nil {
		if e.source != nil {
			return e.source.Validate() == nil && len(e.source.Text()) <= MaxEvidenceItemBytes
		}
		return e.Observation.Validate() == nil && len(e.Observation.Text()) <= MaxEvidenceItemBytes
	}
	x := e.excerpt
	return x.originalBytes > len(x.text)-len(excerptGap) && x.originalBytes <= capture.MaxObservationTextBytes && x.headEnd > 0 && x.tailStart > x.headEnd && x.tailStart < x.originalBytes && x.headEnd+len(excerptGap)+(x.originalBytes-x.tailStart) == len(x.text) && len(x.text) <= MaxEvidenceItemBytes && utf8.ValidString(x.text)
}
