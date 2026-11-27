package client

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/meetings"
)

// SummarizeMeetingPage uses a bounded consecutive saved transcript page.
// This is an explicit, transient model action; saving raw
// transcript text never depends on its availability or success.
func (c *Client) SummarizeMeetingPage(ctx context.Context, id string, after uint64) (assistant.Result, error) {
	result, _, err := c.summarizeMeetingPage(ctx, id, after, false)
	return result, err
}

// SummarizeLocalMeetingPage is for an automatic finished-note recap. The model
// is captured under the policy lock only when it is on-device. CoverageLimited
// describes packing within this page; Page.More describes remaining saved text.
func (c *Client) SummarizeLocalMeetingPage(ctx context.Context, id string, after uint64) (assistant.Result, meetings.RecordPage, error) {
	return c.summarizeMeetingPage(ctx, id, after, true)
}

func (c *Client) summarizeMeetingPage(ctx context.Context, id string, after uint64, localOnly bool) (assistant.Result, meetings.RecordPage, error) {
	if c == nil || ctx == nil || c.data == nil || !c.CanUseMeetings() {
		return assistant.Result{}, meetings.RecordPage{}, ErrNotConfigured
	}
	op, done, err := c.beginOperation(ctx, noteOperationTimeout)
	if err != nil {
		return assistant.Result{}, meetings.RecordPage{}, err
	}
	defer done()
	read, stable := c.beginVoicePublication()
	if !stable {
		return assistant.Result{}, meetings.RecordPage{}, ErrMemoryChanging
	}
	epoch := read.memoryEpoch
	retriever := &meetingSummaryRetriever{}
	service, askCtx, finish, err := c.beginMeetingSummaryAsk(op, epoch, retriever, localOnly)
	if err != nil {
		return assistant.Result{}, meetings.RecordPage{}, err
	}
	defer finish()
	metadata, found, err := c.meetings.repo.GetMeeting(askCtx, id)
	if err != nil {
		return assistant.Result{}, meetings.RecordPage{}, err
	}
	if !found {
		return assistant.Result{}, meetings.RecordPage{}, meetings.ErrNotFound
	}
	if metadata.ID != id || metadata.Validate() != nil {
		return assistant.Result{}, meetings.RecordPage{}, ErrInvalidRequest
	}
	limit := 20 // Match the manually displayed transcript page.
	if localOnly {
		limit = assistant.MaxTranscriptItems
	}
	page, err := c.meetings.repo.MeetingRecords(askCtx, id, meetings.PageRequest{After: after, Limit: limit, MaxBytes: 128 << 10})
	if err != nil {
		return assistant.Result{}, meetings.RecordPage{}, err
	}
	if len(page.Records) == 0 {
		return assistant.Result{}, meetings.RecordPage{}, assistant.ErrSummaryInsufficientEvidence
	}
	if len(page.Records) > limit || page.Next < after {
		return assistant.Result{}, meetings.RecordPage{}, ErrInvalidRequest
	}
	previous := after
	for _, row := range page.Records {
		if row.Sequence <= previous || row.Sequence > page.Next {
			return assistant.Result{}, meetings.RecordPage{}, ErrInvalidRequest
		}
		previous = row.Sequence
		evidence, err := c.meetingSummaryEvidence(askCtx, metadata, row)
		if err != nil {
			return assistant.Result{}, meetings.RecordPage{}, err
		}
		retriever.evidence = append(retriever.evidence, evidence)
	}
	// The fixed retriever cannot fall back to a general question, search other
	// meetings, execute a quoted action item, or introduce unrelated profile text.
	result, err := service.SummarizeTranscript(askCtx, retriever.evidence)
	summaryErr := err
	unusable := errors.Is(err, assistant.ErrSummaryInsufficientEvidence) || errors.Is(err, assistant.ErrSummaryInvalidResponse)
	if err != nil && !unusable {
		return assistant.Result{}, meetings.RecordPage{}, err
	}
	if result.General || result.Direct || result.Answer.InsufficientEvidence || (!unusable && result.GenerationIssue != "") || result.SentItems == 0 || result.SentItems > len(page.Records) {
		return assistant.Result{}, meetings.RecordPage{}, assistant.ErrAnswer
	}
	if result.SentItems < len(page.Records) {
		// Packing admits a consecutive prefix. Continue with the first unread
		// row next, rather than counting omitted fragments as summarized.
		page.Records = page.Records[:result.SentItems]
		page.Next = page.Records[len(page.Records)-1].Sequence
		page.More = true
		result.Omitted = 0
		result.CoverageLimited = result.Truncated > 0
	}
	result.CoverageLimited = result.CoverageLimited || result.Omitted > 0 || result.Truncated > 0
	if !localOnly {
		result.CoverageLimited = result.CoverageLimited || after > 0 || page.More
	}
	// Recheck every admitted source before publication. A deletion through the
	// Client also cancels this ask and advances the memory epoch.
	for _, source := range retriever.evidence {
		saved, found, err := c.data.GetObservation(askCtx, source.SourceID)
		if err != nil {
			return assistant.Result{}, meetings.RecordPage{}, err
		}
		if !found || saved.Validate() != nil || saved.ContentID() != source.SourceID || saved.AppID() != source.AppID || saved.Domain() != source.Domain || saved.Text() != source.Text {
			return assistant.Result{}, meetings.RecordPage{}, ErrMemoryChanging
		}
	}
	c.aiPolicyMu.RLock()
	defer c.aiPolicyMu.RUnlock()
	if err := askCtx.Err(); err != nil {
		return assistant.Result{}, meetings.RecordPage{}, err
	}
	if !read.publish(c, func() {}) {
		return assistant.Result{}, meetings.RecordPage{}, ErrMemoryChanging
	}
	result.Publication = &memoryAnswerPublication{client: c, read: read}
	return result, page, summaryErr
}

type meetingSummaryRetriever struct{ evidence []assistant.Evidence }

func (r *meetingSummaryRetriever) Retrieve(ctx context.Context, _ string) ([]assistant.Evidence, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return append([]assistant.Evidence(nil), r.evidence...), nil
}

// Keep selected-model snapshot, saved-memory permission and cancellation
// registration in one policy read window, just like the normal Ask path.
func (c *Client) beginMeetingSummaryAsk(ctx context.Context, epoch uint64, retriever *meetingSummaryRetriever, localOnly bool) (*assistant.Service, context.Context, func(), error) {
	c.aiPolicyMu.RLock()
	defer c.aiPolicyMu.RUnlock()
	if c.localAI == nil {
		return nil, nil, nil, assistant.ErrAskUnavailable
	}
	c.localAI.mu.RLock()
	settings := c.localAI.settings
	c.localAI.mu.RUnlock()
	provider := effectiveProvider(settings)
	if localOnly && !isLocalCompletionProvider(provider) {
		return nil, nil, nil, assistant.ErrAskUnavailable
	}
	if !isLocalCompletionProvider(provider) && (!isRemoteCompletionProvider(provider) || !settings.RemoteAskEnabled) {
		return nil, nil, nil, assistant.ErrAskUnavailable
	}
	service, ok := c.localAI.askService(retriever)
	if !ok {
		return nil, nil, nil, assistant.ErrAskUnavailable
	}
	askCtx, cancel := context.WithCancel(ctx)
	request, registered := c.registerAskEpoch(cancel, &epoch)
	if !registered {
		cancel()
		return nil, nil, nil, ErrMemoryChanging
	}
	return service, askCtx, func() { cancel(); c.unregisterAsk(request) }, nil
}
func (c *Client) meetingSummaryEvidence(ctx context.Context, m meetings.Meeting, row meetings.StoredRecord) (assistant.Evidence, error) {
	rawID, err := hex.DecodeString(row.SourceID)
	clear(rawID)
	if err != nil || len(row.SourceID) != 64 || strings.ToLower(row.SourceID) != row.SourceID || row.Record.SessionID != m.ID {
		return assistant.Evidence{}, ErrInvalidRequest
	}
	saved, found, err := c.data.GetObservation(ctx, row.SourceID)
	if err != nil {
		return assistant.Evidence{}, err
	}
	if !found || saved.Validate() != nil || saved.ContentID() != row.SourceID || saved.AppID() != "sh.blur.meetings" || !strings.HasSuffix(saved.Domain(), ".meeting.local") || saved.Truncated() {
		return assistant.Evidence{}, ErrInvalidRequest
	}
	source := assistant.Evidence{SourceID: row.SourceID, AppID: saved.AppID(), Domain: saved.Domain(), Text: saved.Text()}
	limits := meetings.DefaultLimits()
	switch row.Record.Kind {
	case meetings.TranscriptFinal:
		e := row.Record.Event
		track := false
		for _, id := range m.Tracks {
			track = track || e.TrackID == id
		}
		if e.Kind != meetings.Final || e.SessionID != m.ID || !track || !summaryRecordID(e.SegmentID) || e.Revision == 0 || e.Start < 0 || e.End < e.Start || e.End > limits.MaxDuration || e.Text != source.Text || len(e.Text) > limits.MaxEventBytes || row.Record.Note != (meetings.ManualNoteInput{}) || row.Record.Key != m.ID+"/transcript/"+e.TrackID+"/"+e.SegmentID {
			return assistant.Evidence{}, ErrInvalidRequest
		}
		source.AppName = "Meeting transcript (" + e.TrackID + ")"

	case meetings.UserNote:
		n := row.Record.Note
		if !summaryRecordID(n.ID) || n.At < 0 || n.Text != source.Text || len(n.Text) > limits.MaxNoteBytes || row.Record.Event != (meetings.Event{}) || row.Record.Key != m.ID+"/note/"+n.ID {
			return assistant.Evidence{}, ErrInvalidRequest
		}
		source.AppName = "Meeting manual note"

	default:
		return assistant.Evidence{}, ErrInvalidRequest
	}
	source.CapturedAt, source.EndAt, err = meetings.RecordTimes(m, row.Record)
	if err != nil {
		return assistant.Evidence{}, ErrInvalidRequest
	}
	return source, nil
}
func summaryRecordID(id string) bool {
	if len(id) == 0 || len(id) > 64 || id == "." || id == ".." {
		return false
	}
	for _, r := range id {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return true
}
