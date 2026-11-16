// Package chronicle defines bounded, source-linked hourly summaries. It has
// no storage, provider, clock, or operating-system dependencies.
package chronicle

import (
	"crypto/sha256"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/capture"
)

var ErrInvalid = errors.New("chronicle: invalid hour, evidence, or summary")

const (
	MaxEvidence          = 64
	MaxEvidenceItemBytes = 4 << 10
	MaxEvidenceBytes     = 16 << 10
	MaxSummaryBytes      = 4 << 10
	MaxFollowUpBytes     = 512
	evidenceFramingBytes = 16
)

// Hour is a closed, UTC, half-open one-hour interval. Evidence must be in
// chronological order and contain at most one entry per stable source ID.
type Hour struct {
	Start    time.Time
	End      time.Time
	Evidence []Evidence
	// SourceLimitReached records that the bounded source index/feed may have
	// excluded additional captures. It makes no claim about unobserved activity.
	SourceLimitReached bool
}

// Evidence carries only a sanitized observation plus deterministic
// caller-computed salience. Observation text is always data; this package
// never interprets instructions.
type Evidence struct {
	Observation capture.SanitizedObservation
	At          time.Time
	Salience    uint8
	excerpt     *sourceExcerpt
	source      *capture.Evidence
}

// Packed is a deterministic, byte-bounded model input. SourceIDs are sorted
// for stable provenance; Evidence remains chronological for readable context.
type Packed struct {
	HourID             string
	Start              time.Time
	End                time.Time
	Evidence           []Evidence
	SourceIDs          []string
	InputBytes         int
	Omitted            int
	SourceLimitReached bool
}

// Summary is an untrusted generated artifact. SourceIDs must be a non-empty,
// sorted subset of the exact Packed input used to generate Text.
type Summary struct {
	HourID    string
	Start     time.Time
	End       time.Time
	Text      string
	SourceIDs []string
	FollowUp  *FollowUp `json:"follow_up,omitempty"`
	// InputReceipt is assigned by the trusted worker after validating output.
	// Legacy summaries omit it and are reconsidered once on recovery.
	InputReceipt string `json:"input_receipt,omitempty"`
	// InputSourceIDs records every source supplied to generation, including
	// uncited context. Deletion must erase derived text for all dependencies.
	InputSourceIDs []string `json:"input_source_ids,omitempty"`
	// Coverage is generation-time metadata. The deterministic coverage note in
	// Text survives persistence without changing the schema-3 JSON contract.
	Coverage Coverage `json:"-"`
}

func (s Summary) DependencyIDs() []string {
	if len(s.InputSourceIDs) > 0 {
		return append([]string(nil), s.InputSourceIDs...)
	}
	return append([]string(nil), s.SourceIDs...)
}

// FollowUp is an optional, single clarification question about a possible
// missing meeting detail. It is never an action or task and must cite one of
// the exact source observations used to generate the hour summary.
type FollowUp struct {
	SourceID string `json:"source_id"`
	Text     string `json:"text"`
}

// ID returns a stable identity for one UTC wall-clock hour, independent of
// process-local state or the order in which evidence arrived.
func (h Hour) ID() (string, error) {
	if !validBounds(h.Start, h.End) {
		return "", ErrInvalid
	}
	return "hour:" + h.Start.Format("20060102T15Z"), nil
}

// PackHour selects the highest-salience evidence under fixed item, count and
// aggregate byte caps. Large saved sources use labelled head/tail excerpts;
// their original observations and identities are never rewritten.
func PackHour(hour Hour) (Packed, error) {
	id, err := hour.ID()
	if err != nil || len(hour.Evidence) == 0 || len(hour.Evidence) > MaxEvidence {
		return Packed{}, ErrInvalid
	}
	bounded := make([]Evidence, len(hour.Evidence))
	for i, item := range hour.Evidence {
		if item.excerpt == nil {
			if item.source != nil {
				item, err = NewSourceEvidence(*item.source, item.At, item.Salience)
			} else {
				item, err = NewEvidence(item.Observation, item.At, item.Salience)
			}
			if err != nil {
				return Packed{}, err
			}
		}
		bounded[i] = item
	}
	hour.Evidence = bounded
	if err := validateEvidence(hour); err != nil {
		return Packed{}, err
	}
	order := salienceOrder(hour.Evidence)
	selected := make([]Evidence, 0, len(hour.Evidence))
	bytesUsed := 0
	omitted := 0
	for _, index := range order {
		item := hour.Evidence[index]
		cost := evidenceCost(item)
		if bytesUsed+cost > MaxEvidenceBytes {
			omitted++
			continue
		}
		selected = append(selected, cloneEvidence(item))
		bytesUsed += cost
	}
	sort.Slice(selected, func(i, j int) bool {
		if !selected[i].At.Equal(selected[j].At) {
			return selected[i].At.Before(selected[j].At)
		}
		return selected[i].SourceID() < selected[j].SourceID()
	})
	sourceIDs := make([]string, len(selected))
	for i := range selected {
		sourceIDs[i] = selected[i].SourceID()
	}
	sort.Strings(sourceIDs)
	return Packed{HourID: id, Start: hour.Start, End: hour.End, Evidence: selected, SourceIDs: sourceIDs, InputBytes: bytesUsed, Omitted: omitted, SourceLimitReached: hour.SourceLimitReached}, nil
}

// ValidateSummary verifies structural bounds and that every attribution is
// drawn from the exact input evidence made available to the model.
func ValidateSummary(input Packed, summary Summary) error {
	if !validPacked(input) || ValidatePersistedSummary(summary) != nil || summary.HourID != input.HourID || !summary.Start.Equal(input.Start) || !summary.End.Equal(input.End) {
		return ErrInvalid
	}
	if summary.Coverage != input.Coverage() || summary.Coverage.hasGaps() && !strings.HasSuffix(summary.Text, summary.Coverage.note()) {
		return ErrInvalid
	}
	allowed := make(map[string]struct{}, len(input.SourceIDs))
	for _, id := range input.SourceIDs {
		allowed[id] = struct{}{}
	}
	for _, id := range summary.SourceIDs {
		if _, ok := allowed[id]; !ok {
			return ErrInvalid
		}
	}
	if len(summary.InputSourceIDs) > 0 {
		if len(summary.InputSourceIDs) != len(input.SourceIDs) {
			return ErrInvalid
		}
		for i, id := range summary.InputSourceIDs {
			if id != input.SourceIDs[i] {
				return ErrInvalid
			}
		}
	}
	if summary.FollowUp != nil && (!validFollowUp(*summary.FollowUp) || !containsID(input.SourceIDs, summary.FollowUp.SourceID)) {
		return ErrInvalid
	}
	if summary.FollowUp != nil {
		if !containsID(summary.SourceIDs, summary.FollowUp.SourceID) || !followUpSourceIsMeetingEvidence(input, summary.FollowUp.SourceID) {
			return ErrInvalid
		}
	}
	return nil
}

// ValidatePersistedSummary validates the bounded, source-linked artifact after
// the exact model input is no longer retained. Generation-time validation
// should use ValidateSummary to additionally enforce the input allow-list.
func ValidatePersistedSummary(summary Summary) error {
	if summary.InputReceipt != "" && !validSourceID(summary.InputReceipt) {
		return ErrInvalid
	}
	if len(summary.InputSourceIDs) > 0 {
		if len(summary.InputSourceIDs) > MaxEvidence || !sortedUniqueIDs(summary.InputSourceIDs) {
			return ErrInvalid
		}
		for _, id := range summary.SourceIDs {
			if !containsID(summary.InputSourceIDs, id) {
				return ErrInvalid
			}
		}
	}
	if !validBounds(summary.Start, summary.End) || summary.HourID != "hour:"+summary.Start.Format("20060102T15Z") || !validText(summary.Text, MaxSummaryBytes) || len(summary.SourceIDs) == 0 || !sortedUniqueIDs(summary.SourceIDs) {
		return ErrInvalid
	}
	if summary.FollowUp != nil && (!validFollowUp(*summary.FollowUp) || !containsID(summary.SourceIDs, summary.FollowUp.SourceID)) {
		return ErrInvalid
	}
	return nil
}

func validFollowUp(question FollowUp) bool {
	return validSourceID(question.SourceID) && validText(question.Text, MaxFollowUpBytes) && strings.TrimSpace(question.Text) == question.Text && strings.HasSuffix(question.Text, "?")
}

func containsID(ids []string, target string) bool {
	i := sort.SearchStrings(ids, target)
	return i < len(ids) && ids[i] == target
}

func followUpSourceIsMeetingEvidence(input Packed, sourceID string) bool {
	for _, item := range input.Evidence {
		if item.SourceID() != sourceID {
			continue
		}
		// Missing details in a sampled or partial source may be present in the
		// unavailable portion. Its absence cannot ground a clarification.
		if item.CapturedPartial() || item.Excerpted() {
			return false
		}
		return supportsMeetingText(item.Text())
	}
	return false
}

// SupportsFollowUpEvidence is a conservative lexical guard shared by storage
// and model-output validation; it is not itself a meeting classifier.
func SupportsFollowUpEvidence(observation capture.SanitizedObservation) bool {
	if observation.Validate() != nil || observation.Truncated() {
		return false
	}
	return supportsMeetingText(observation.Text())
}

func SupportsFollowUpSource(source capture.Evidence) bool {
	return source.Validate() == nil && !source.Truncated() && supportsMeetingText(source.Text())
}

func supportsMeetingText(value string) bool {
	text := strings.ToLower(value)
	for _, cue := range []string{"meeting", "standup", "stand-up", "retrospective", "workshop", "interview", "1:1", "one-on-one", "sync with", "call with", "met with", "meeting with", "discussed with"} {
		if strings.Contains(text, cue) {
			return true
		}
	}
	return false
}

func validateEvidence(hour Hour) error {
	seen := make(map[string]struct{}, len(hour.Evidence))
	var previous Evidence
	for i, item := range hour.Evidence {
		if !item.valid() || !item.At.After(hour.Start.Add(-time.Nanosecond)) || !item.At.Before(hour.End) {
			return ErrInvalid
		}
		sourceID := item.SourceID()
		if _, ok := seen[sourceID]; ok {
			return ErrInvalid
		}
		seen[sourceID] = struct{}{}
		if i > 0 && (item.At.Before(previous.At) || item.At.Equal(previous.At) && sourceID <= previous.SourceID()) {
			return ErrInvalid
		}
		previous = item
	}
	return nil
}

func validPacked(input Packed) bool {
	if !validBounds(input.Start, input.End) || input.HourID != "hour:"+input.Start.Format("20060102T15Z") || input.Omitted < 0 || len(input.Evidence)+input.Omitted > MaxEvidence || len(input.Evidence) != len(input.SourceIDs) || !sortedUniqueIDs(input.SourceIDs) {
		return false
	}
	var total int
	evidenceIDs := make([]string, len(input.Evidence))
	var previous Evidence
	for i, item := range input.Evidence {
		if !item.valid() || !item.At.After(input.Start.Add(-time.Nanosecond)) || !item.At.Before(input.End) || i > 0 && (item.At.Before(previous.At) || item.At.Equal(previous.At) && item.SourceID() <= previous.SourceID()) {
			return false
		}
		previous = item
		evidenceIDs[i] = item.SourceID()
		total += evidenceCost(item)
	}
	sort.Strings(evidenceIDs)
	for i := range evidenceIDs {
		if evidenceIDs[i] != input.SourceIDs[i] {
			return false
		}
	}
	return total == input.InputBytes && total <= MaxEvidenceBytes
}

func validBounds(start, end time.Time) bool {
	return !start.IsZero() && start.Location() == time.UTC && start.Minute() == 0 && start.Second() == 0 && start.Nanosecond() == 0 && end.Location() == time.UTC && end.Equal(start.Add(time.Hour))
}

func sortedUniqueIDs(ids []string) bool {
	for i, id := range ids {
		if !validSourceID(id) || i > 0 && ids[i-1] >= id {
			return false
		}
	}
	return true
}

func validSourceID(id string) bool {
	if len(id) != sha256.Size*2 {
		return false
	}
	for i := range id {
		if (id[i] < '0' || id[i] > '9') && (id[i] < 'a' || id[i] > 'f') {
			return false
		}
	}
	return true
}

func validText(text string, limit int) bool {
	if len(text) == 0 || len(text) > limit || !utf8.ValidString(text) || strings.TrimSpace(text) == "" {
		return false
	}
	for _, r := range text {
		if r == 0 || r < 0x20 && r != '\n' && r != '\t' || r == 0x7f {
			return false
		}
	}
	return true
}

func evidenceCost(item Evidence) int {
	return len(item.SourceID()) + len(item.Text()) + evidenceFramingBytes
}

func cloneEvidence(item Evidence) Evidence {
	return item.Clone()
}

// salienceOrder returns evidence indexes from most to least salient, breaking
// ties by time and then content ID. PackHour and DraftHour share it so both
// agree on which evidence matters most.
func salienceOrder(evidence []Evidence) []int {
	order := make([]int, len(evidence))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(i, j int) bool {
		a, b := evidence[order[i]], evidence[order[j]]
		if a.Salience != b.Salience {
			return a.Salience > b.Salience
		}
		if !a.At.Equal(b.At) {
			return a.At.Before(b.At)
		}
		return a.SourceID() < b.SourceID()
	})
	return order
}
