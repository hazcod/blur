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
}

// Evidence carries only a sanitized observation plus deterministic
// caller-computed salience. Observation text is always data; this package
// never interprets instructions.
type Evidence struct {
	Observation capture.SanitizedObservation
	At          time.Time
	Salience    uint8
}

// Packed is a deterministic, byte-bounded model input. SourceIDs are sorted
// for stable provenance; Evidence remains chronological for readable context.
type Packed struct {
	HourID     string
	Start      time.Time
	End        time.Time
	Evidence   []Evidence
	SourceIDs  []string
	InputBytes int
	Omitted    int
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

// PackHour selects the highest-salience complete evidence items under fixed
// item, count, and aggregate byte caps. It never truncates source text.
func PackHour(hour Hour) (Packed, error) {
	id, err := hour.ID()
	if err != nil || len(hour.Evidence) == 0 || len(hour.Evidence) > MaxEvidence {
		return Packed{}, ErrInvalid
	}
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
		return selected[i].Observation.ContentID() < selected[j].Observation.ContentID()
	})
	sourceIDs := make([]string, len(selected))
	for i := range selected {
		sourceIDs[i] = selected[i].Observation.ContentID()
	}
	sort.Strings(sourceIDs)
	return Packed{HourID: id, Start: hour.Start, End: hour.End, Evidence: selected, SourceIDs: sourceIDs, InputBytes: bytesUsed, Omitted: omitted}, nil
}

// ValidateSummary verifies structural bounds and that every attribution is
// drawn from the exact input evidence made available to the model.
func ValidateSummary(input Packed, summary Summary) error {
	if !validPacked(input) || ValidatePersistedSummary(summary) != nil || summary.HourID != input.HourID || !summary.Start.Equal(input.Start) || !summary.End.Equal(input.End) {
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
		if item.Observation.ContentID() != sourceID {
			continue
		}
		return SupportsFollowUpEvidence(item.Observation)
	}
	return false
}

// SupportsFollowUpEvidence is a conservative lexical guard shared by storage
// and model-output validation; it is not itself a meeting classifier.
func SupportsFollowUpEvidence(observation capture.SanitizedObservation) bool {
	if observation.Validate() != nil {
		return false
	}
	text := strings.ToLower(observation.Text())
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
		if item.Observation.Validate() != nil || !validSourceID(item.Observation.ContentID()) || !item.At.After(hour.Start.Add(-time.Nanosecond)) || !item.At.Before(hour.End) || len(item.Observation.Text()) > MaxEvidenceItemBytes {
			return ErrInvalid
		}
		sourceID := item.Observation.ContentID()
		if _, ok := seen[sourceID]; ok {
			return ErrInvalid
		}
		seen[sourceID] = struct{}{}
		if i > 0 && (item.At.Before(previous.At) || item.At.Equal(previous.At) && sourceID <= previous.Observation.ContentID()) {
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
		if item.Observation.Validate() != nil || !validSourceID(item.Observation.ContentID()) || !item.At.After(input.Start.Add(-time.Nanosecond)) || !item.At.Before(input.End) || len(item.Observation.Text()) > MaxEvidenceItemBytes || i > 0 && (item.At.Before(previous.At) || item.At.Equal(previous.At) && item.Observation.ContentID() <= previous.Observation.ContentID()) {
			return false
		}
		previous = item
		evidenceIDs[i] = item.Observation.ContentID()
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
		if !(id[i] >= '0' && id[i] <= '9' || id[i] >= 'a' && id[i] <= 'f') {
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
	return len(item.Observation.ContentID()) + len(item.Observation.Text()) + evidenceFramingBytes
}

func cloneEvidence(item Evidence) Evidence {
	item.Observation = item.Observation.Clone()
	return item
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
		return a.Observation.ContentID() < b.Observation.ContentID()
	})
	return order
}
