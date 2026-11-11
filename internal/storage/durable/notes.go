package durable

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/today"
)

var ErrInvalidNote = errors.New("durable store: invalid note")

var _ notes.Repository = (*Store)(nil)

// SaveNote atomically encrypts the user-authored source and updates its local
// Today projection. Exact duplicate notes reuse the existing source record.
func (s *Store) SaveNote(ctx context.Context, observation capture.SanitizedObservation, at time.Time) error {
	if s == nil || s.state == nil || ctx == nil || ctx.Err() != nil || observation.Validate() != nil || observation.AppID() != "sh.blur.notes" || observation.Domain() != "local" || len(observation.Text()) == 0 || len(observation.Text()) > notes.MaxNoteBytes || at.IsZero() {
		return ErrInvalidNote
	}
	state := s.state
	state.noteMu.Lock()
	defer state.noteMu.Unlock()
	date, zone := localDate(at)
	snapshot, err := s.noteSnapshot(ctx, date, zone, at, observation.ContentID(), observation.Text())
	if err != nil {
		return err
	}
	_, exists, err := s.GetObservation(ctx, observation.ContentID())
	if err != nil {
		return err
	}
	return s.Update(ctx, func(tx *Tx) error {
		if !exists {
			if err := tx.PutObservationAt(observation, at); err != nil {
				return err
			}
		}
		return tx.putTodaySnapshot(snapshot)
	})
}

// SaveProposals stores only validated, source-linked, unconfirmed results in
// the encrypted Today projection. It never creates a notification or fires an
// external reminder.
func (s *Store) SaveProposals(ctx context.Context, sourceID string, proposals []notes.Proposal) error {
	if s == nil || s.state == nil || ctx == nil || ctx.Err() != nil || len(proposals) == 0 || len(proposals) > notes.MaxProposals || len(sourceID) != 64 {
		return ErrInvalidNote
	}
	for i := range sourceID {
		c := sourceID[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return ErrInvalidNote
		}
	}
	state := s.state
	state.noteMu.Lock()
	defer state.noteMu.Unlock()
	observation, found, err := s.GetObservation(ctx, sourceID)
	if err != nil || !found || observation.AppID() != "sh.blur.notes" {
		return ErrInvalidNote
	}
	var at time.Time
	for _, proposal := range proposals {
		if proposal.SourceID != sourceID || proposal.Confirmed || !validOpaqueID(proposal.ID) || !validNoteProposal(proposal) {
			return ErrInvalidNote
		}
		if at.IsZero() || proposal.CreatedAt.Before(at) {
			at = proposal.CreatedAt
		}
	}
	if at.IsZero() {
		return ErrInvalidNote
	}
	date, _ := localDate(at)
	snapshot, found, err := s.GetTodaySnapshot(ctx, date)
	if err != nil {
		return err
	}
	if !found || !hasSource(snapshot.SourceIDs, sourceID) {
		return ErrInvalidNote
	}
	snapshot.GeneratedAt = at
	snapshot.GenerationID = "note-" + sourceID
	for _, proposal := range proposals {
		text := proposal.Text
		switch proposal.Kind {
		case notes.Question:
			if !hasQuestion(snapshot.Questions, proposal.ID) && len(snapshot.Questions) < today.MaxQuestions {
				snapshot.Questions = append(snapshot.Questions, today.Question{ID: proposal.ID, Text: proposal.Text, Reason: "Possible missing follow-up after this meeting.", SourceIDs: []string{sourceID}, CreatedAt: proposal.CreatedAt})
			}
			continue
		case notes.Reminder:
			text = "Reminder: " + text
		case notes.FollowUp:
			text = "Follow up: " + text
		}
		if proposal.Due != "" {
			text += " · " + proposal.Due
		}
		if len(text) > today.MaxTextBytes {
			text = truncateNoteText(text, today.MaxTextBytes)
		}
		if !hasTodo(snapshot.TodoCandidates, proposal.ID) && len(snapshot.TodoCandidates) < today.MaxTodos {
			snapshot.TodoCandidates = append(snapshot.TodoCandidates, today.Todo{ID: proposal.ID, Text: text, SourceIDs: []string{sourceID}})
		}
	}
	if !found || snapshot.Validate() != nil {
		return ErrInvalidNote
	}
	return s.PutTodaySnapshot(ctx, snapshot)
}

// DismissQuestion records an explicit user dismissal; it never deletes the
// source note or changes a task/reminder.
func (s *Store) DismissQuestion(ctx context.Context, date, questionID string) error {
	return s.resolveQuestion(ctx, date, questionID, "", time.Time{}, true)
}

// AnswerQuestion stores the answer as a new encrypted user note and links its
// source ID to the original question. The question is resolved only after the
// answer has been durably saved.
func (s *Store) AnswerQuestion(ctx context.Context, date, questionID, answer string, at time.Time) error {
	if s == nil || s.state == nil || ctx == nil || ctx.Err() != nil || !validTodayDate(date) || !validOpaqueID(questionID) || len(answer) == 0 || len(answer) > notes.MaxNoteBytes || !utf8.ValidString(answer) || strings.TrimSpace(answer) == "" || at.IsZero() {
		return ErrInvalidNote
	}
	questionSnapshot, found, err := s.GetTodaySnapshot(ctx, date)
	if err != nil || !found {
		return ErrInvalidNote
	}
	observation, err := capture.SanitizeObservation("sh.blur.notes", "local", answer, notes.MaxNoteBytes)
	if err != nil || !hasSource(questionSnapshot.SourceIDs, observation.ContentID()) && len(questionSnapshot.SourceIDs) >= today.MaxSources {
		return ErrInvalidNote
	}
	questionFound := false
	for _, question := range questionSnapshot.Questions {
		if question.ID == questionID && !question.Resolved {
			questionFound = true
			break
		}
	}
	if !questionFound {
		return ErrInvalidNote
	}
	if err := s.SaveNote(ctx, observation, at); err != nil {
		return err
	}
	return s.resolveQuestion(ctx, date, questionID, observation.ContentID(), at, false)
}

func (s *Store) resolveQuestion(ctx context.Context, date, questionID, answerSourceID string, at time.Time, dismissed bool) error {
	if s == nil || s.state == nil || ctx == nil || ctx.Err() != nil || !validTodayDate(date) || !validOpaqueID(questionID) || (!dismissed && (!validDigestID(answerSourceID) || at.IsZero())) {
		return ErrInvalidNote
	}
	s.state.noteMu.Lock()
	defer s.state.noteMu.Unlock()
	snapshot, found, err := s.GetTodaySnapshot(ctx, date)
	if err != nil || !found {
		return ErrInvalidNote
	}
	for i := range snapshot.Questions {
		question := &snapshot.Questions[i]
		if question.ID != questionID {
			continue
		}
		if question.Resolved {
			return ErrInvalidNote
		}
		question.Resolved, question.Dismissed, question.AnswerSourceID = true, dismissed, answerSourceID
		if !dismissed && !hasSource(snapshot.SourceIDs, answerSourceID) {
			if len(snapshot.SourceIDs) >= today.MaxSources {
				return ErrInvalidNote
			}
			snapshot.SourceIDs = append(snapshot.SourceIDs, answerSourceID)
			sort.Strings(snapshot.SourceIDs)
		}
		return s.PutTodaySnapshot(ctx, snapshot)
	}
	return ErrInvalidNote
}

// ConfirmProposal promotes one visible suggestion into the user's active Today
// list. It does not schedule notifications or mutate any source application.
func (s *Store) ConfirmProposal(ctx context.Context, date, proposalID string) error {
	if s == nil || s.state == nil || ctx == nil || ctx.Err() != nil || !validTodayDate(date) || !validOpaqueID(proposalID) {
		return ErrInvalidNote
	}
	s.state.noteMu.Lock()
	defer s.state.noteMu.Unlock()
	snapshot, found, err := s.GetTodaySnapshot(ctx, date)
	if err != nil || !found {
		return ErrInvalidNote
	}
	for i, candidate := range snapshot.TodoCandidates {
		if candidate.ID != proposalID {
			continue
		}
		if len(snapshot.ConfirmedTodos) >= today.MaxTodos {
			return ErrInvalidNote
		}
		candidate.Confirmed = true
		snapshot.TodoCandidates = append(snapshot.TodoCandidates[:i], snapshot.TodoCandidates[i+1:]...)
		snapshot.ConfirmedTodos = append(snapshot.ConfirmedTodos, candidate)
		return s.PutTodaySnapshot(ctx, snapshot)
	}
	return ErrInvalidNote
}

func validNoteProposal(p notes.Proposal) bool {
	if len(p.Text) == 0 || len(p.Text) > notes.MaxProposalBytes || !utf8.ValidString(p.Text) || strings.TrimSpace(p.Text) != p.Text || strings.ContainsRune(p.Text, 0) || len(p.Due) > 64 || strings.ContainsRune(p.Due, 0) || p.CreatedAt.IsZero() {
		return false
	}
	switch p.Kind {
	case notes.Task, notes.Reminder, notes.FollowUp:
		return true
	case notes.Question:
		return strings.HasSuffix(p.Text, "?") && p.Due == ""
	default:
		return false
	}
}

func (s *Store) noteSnapshot(ctx context.Context, date, zone string, at time.Time, sourceID, text string) (today.Snapshot, error) {
	snapshot, found, err := s.GetTodaySnapshot(ctx, date)
	if err != nil {
		return today.Snapshot{}, err
	}
	if !found {
		preview := truncateNoteText(strings.ReplaceAll(strings.ReplaceAll(text, "\n", " "), "\r", " "), today.MaxTextBytes)
		return today.Build(today.Input{Date: date, Zone: zone, GeneratedAt: at, GenerationID: "note-" + sourceID, Evidence: []today.Evidence{{SourceID: sourceID, At: at, Change: preview, ChangeOrigin: today.ChangeOriginUser}}})
	}
	if snapshot.Zone != zone || snapshot.Date != date {
		return today.Snapshot{}, ErrInvalidNote
	}
	snapshot.GeneratedAt = at
	snapshot.GenerationID = "note-" + sourceID
	if !hasSource(snapshot.SourceIDs, sourceID) {
		if len(snapshot.SourceIDs) >= today.MaxSources {
			snapshot.Completeness.SourcesOmitted++
			snapshot.Completeness.ChangesOmitted++
			snapshot.Completeness.EvidenceOmitted++
			if snapshot.Validate() != nil {
				return today.Snapshot{}, ErrInvalidNote
			}
			return snapshot, nil
		}
		snapshot.SourceIDs = append(snapshot.SourceIDs, sourceID)
		sort.Strings(snapshot.SourceIDs)
	}
	preview := truncateNoteText(strings.ReplaceAll(strings.ReplaceAll(text, "\n", " "), "\r", " "), today.MaxTextBytes)
	change := today.Change{At: at, Text: preview, SourceIDs: []string{sourceID}, Origin: today.ChangeOriginUser}
	snapshot.RecentChanges = append(snapshot.RecentChanges, change)
	sort.Slice(snapshot.RecentChanges, func(i, j int) bool {
		if snapshot.RecentChanges[i].At.Equal(snapshot.RecentChanges[j].At) {
			return snapshot.RecentChanges[i].SourceIDs[0] < snapshot.RecentChanges[j].SourceIDs[0]
		}
		return snapshot.RecentChanges[i].At.After(snapshot.RecentChanges[j].At)
	})
	if len(snapshot.RecentChanges) > today.MaxChanges {
		snapshot.Completeness.ChangesOmitted += len(snapshot.RecentChanges) - today.MaxChanges
		snapshot.RecentChanges = snapshot.RecentChanges[:today.MaxChanges]
	}
	snapshot.Completeness.EvidenceSeen++
	if snapshot.Validate() != nil {
		return today.Snapshot{}, ErrInvalidNote
	}
	return snapshot, nil
}

func localDate(at time.Time) (string, string) {
	zone := time.Local.String()
	location, err := time.LoadLocation(zone)
	if err != nil {
		location = time.UTC
		zone = "UTC"
	}
	return at.In(location).Format("2006-01-02"), zone
}

func truncateNoteText(value string, max int) string {
	if len(value) <= max {
		return value
	}
	value = value[:max]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return strings.TrimSpace(value)
}

func hasSource(ids []string, id string) bool {
	i := sort.SearchStrings(ids, id)
	return i < len(ids) && ids[i] == id
}

func hasTodo(todos []today.Todo, id string) bool {
	for _, item := range todos {
		if item.ID == id {
			return true
		}
	}
	return false
}

func hasQuestion(questions []today.Question, id string) bool {
	for _, question := range questions {
		if question.ID == id {
			return true
		}
	}
	return false
}

func validDigestID(value string) bool {
	if len(value) != 64 {
		return false
	}
	for i := range value {
		c := value[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
