package client

import (
	"context"
	"encoding/hex"
	"slices"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/storage"
)

func (c *Client) CitedSourcesMatch(ctx context.Context, ids, terms []string) (bool, error) {
	if c == nil || c.data == nil || ctx == nil || len(ids) > assistant.MaxEvidenceItems || len(terms) < 1 || len(terms) > search.MaxTerms {
		return false, ErrInvalidRequest
	}
	epoch, stable := c.BeginMemoryRead()
	if !stable {
		return false, ErrMemoryChanging
	}
	matched := false
	anchoredRecording := false
	for _, id := range ids {
		decoded, err := hex.DecodeString(id)
		if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != id {
			return false, ErrInvalidRequest
		}
		source, found, err := storage.ReadSourceEvidence(ctx, c.data, id)
		if err != nil {
			return false, err
		}
		if !found {
			continue
		}
		if source.AppID() == "sh.blur.meetings" {
			if reader, ok := c.data.(meetingSourceReader); ok {
				meeting, live, err := reader.MeetingForSource(ctx, id)
				if err != nil {
					return false, err
				}
				if live && meeting.Validate() == nil {
					anchoredRecording = true
					continue
				}
			}
		}
		all, err := citedTextHasTerms(source.Text(), terms)
		if err != nil {
			return false, err
		}
		matched = matched || all
	}
	if anchoredRecording {
		related, _, err := c.citedRecordingContext(ctx, assistant.Plan{Terms: terms}, ids)
		if err != nil {
			return false, err
		}
		matched = matched || len(related.Evidence) > 0 || related.Diagnosis.VoiceRecallLimited
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !c.MemoryReadCurrent(epoch) {
		return false, ErrMemoryChanging
	}
	return matched, nil
}

func citedTextHasTerms(text string, terms []string) (bool, error) {
	tokens, err := search.NormalizeTokens(text, max(1, len(text)))
	if err != nil {
		return false, err
	}
	for _, term := range terms {
		if _, found := slices.BinarySearch(tokens, term); !found {
			return false, nil
		}
	}
	return len(terms) > 0, nil
}

var _ assistant.CitedTopicMatcher = (*Client)(nil)
