package client

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/meetings"
)

// Follow recording membership rather than per-passage opaque domains. The
// streaming reader has a work/byte budget, not a first-N-records cutoff; an
// incomplete read stays limited and can never authorize a global fallback.
func (c *Client) citedRecordingContext(ctx context.Context, plan assistant.Plan, ids []string) (assistant.Retrieval, bool, error) {
	result := assistant.Retrieval{}
	reader, ok := c.data.(meetingSourceReader)
	if !ok {
		return result, false, nil
	}
	var recordings []meetings.Meeting
	seen := map[string]bool{}
	for _, id := range ids {
		meeting, found, err := reader.MeetingForSource(ctx, id)
		if err != nil {
			return result, false, err
		}
		if found && meeting.Validate() == nil && !seen[meeting.ID] {
			recordings = append(recordings, meeting)
			seen[meeting.ID] = true
		}
	}
	if len(recordings) == 0 {
		return result, false, nil
	}
	result.Diagnosis.VoiceRequested = true
	if c.meetings == nil || c.meetings.repo == nil {
		result.Diagnosis.VoiceRecallLimited = true
		return result, true, nil
	}
	work, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	used := 0
	for _, meeting := range recordings {
		after := uint64(0)
		for {
			if MaxSearchBytes-used < 256 || work.Err() != nil && ctx.Err() == nil {
				result.Diagnosis.VoiceRecallLimited = true
				return result, true, nil
			}
			page, err := c.meetings.repo.MeetingRecords(work, meeting.ID, meetings.PageRequest{After: after, Limit: 20, MaxBytes: min(128<<10, MaxSearchBytes-used)})
			if errors.Is(err, meetings.ErrLimit) || errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
				result.Diagnosis.VoiceRecallLimited = true
				return result, true, nil
			}
			if err != nil {
				return result, true, err
			}
			if len(page.Records) > 20 || page.More && page.Next <= after {
				return result, true, ErrInvalidRequest
			}
			for _, row := range page.Records {
				encoded, err := json.Marshal(row)
				if err != nil {
					return result, true, err
				}
				used += len(encoded)
				if used > MaxSearchBytes {
					result.Diagnosis.VoiceRecallLimited = true
					return result, true, nil
				}
				if row.Record.Kind != meetings.TranscriptFinal && row.Record.Kind != meetings.UserNote {
					continue
				}
				item, err := c.meetingSummaryEvidence(work, meeting, row)
				if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
					result.Diagnosis.VoiceRecallLimited = true
					return result, true, nil
				}
				if err != nil {
					return result, true, err
				}
				if !plan.Scope.Start.IsZero() && item.CapturedAt.Before(plan.Scope.Start) || !plan.Scope.End.IsZero() && !item.CapturedAt.Before(plan.Scope.End) {
					continue
				}
				matched, err := citedTextHasTerms(item.Text, plan.Terms)
				if err != nil {
					return result, true, err
				}
				if !matched || !technicalSubjectSupport(plan.TechnicalSubjects, item.Text, meeting.Title) {
					continue
				}
				result.Evidence = append(result.Evidence, item)
				if len(result.Evidence) == assistant.MaxEvidenceItems {
					result.Diagnosis.VoiceRecallLimited = true
					return result, true, nil
				}
			}
			if !page.More {
				break
			}
			after = page.Next
		}
	}
	return result, true, nil
}
