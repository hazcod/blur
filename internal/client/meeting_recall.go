package client

import (
	"context"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/meetings"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/semantic"
)

const voiceRecallHorizon = 2 * time.Hour

func recordedAnswerNeedsPublication(result assistant.Result) bool {
	plan := result.Plan
	if asksRecordedContent(plan) {
		return true
	}
	if plan.ReferenceQuestion != "" {
		plan.Question = plan.ReferenceQuestion
		if asksRecordedContent(plan) {
			return true
		}
	}
	for _, source := range result.Sources {
		if source.AppID == "sh.blur.meetings" {
			return true
		}
	}
	return false
}

func (c *Client) recordedAnswerVoiceOnly(ctx context.Context, result assistant.Result) (bool, error) {
	reader, ok := c.data.(meetingSourceReader)
	kinds, knowsKind := c.data.(meetingRecordKindReader)
	if !ok || !knowsKind {
		return false, nil
	}
	for _, source := range result.Sources {
		if source.AppID != "sh.blur.meetings" || !strings.HasSuffix(source.Domain, ".meeting.local") {
			return false, nil
		}
		meeting, found, err := reader.MeetingForSource(ctx, source.SourceID)
		if err != nil {
			return false, err
		}
		if !found || meeting.Validate() != nil {
			return false, nil
		}
		kind, live, err := kinds.MeetingRecordKindForSource(ctx, source.SourceID)
		if err != nil {
			return false, err
		}
		if !live || kind != meetings.TranscriptFinal && kind != meetings.UserNote {
			return false, nil
		}
	}
	return true, nil
}

func recordedSessionWord(word string) bool {
	switch word {
	case "meeting", "meetings", "call", "calls", "standup", "sync", "session", "sessions",
		"interview", "interviews", "webinar", "webinars", "lecture", "lectures", "conference", "conferences", "podcast", "podcasts", "keynote", "keynotes":
		return true
	default:
		return false
	}
}

func voiceRecallTopics(plan assistant.Plan) map[string]bool {
	topics := make(map[string]bool)
	for _, term := range plan.Terms {
		switch term {
		case "video", "videos", "youtube", "recording", "recordings", "transcript", "transcripts", "voice", "note", "notes",
			"spraaknotitie", "spraaknotities", "voicenotitie", "voicenotities", "opname", "opnames",
			"discuss", "discussed", "discussing", "talk", "talked", "talking", "say", "said", "saying", "content", "about",
			"recap", "recapped", "recapping", "action", "item", "items", "key", "point", "points", "takeaway", "takeaways", "outcome", "outcomes",
			"commitment", "commitments", "promise", "promises", "promised", "settle", "settled", "assigned", "assignment", "assignments", "owner", "owners",
			"agree", "agreed", "decide", "decided", "decision", "decisions", "cover", "covered", "explain", "explained", "speaker", "presenter",
			"watch", "watched", "watching", "listen", "listened", "listening":
			continue
		}
		if recordedSessionWord(term) {
			continue
		}
		topics[term] = true
	}
	return topics
}

func voiceRecallTopicMatch(text string, topics map[string]bool) bool {
	if len(topics) == 0 {
		return true
	}
	// Plan terms are in the index's folded space, so fold each word of the
	// meeting text the same way ("Réunion" matches "reunion"). Combining marks
	// stay inside a word so decomposed text folds like composed text.
	for _, word := range strings.FieldsFunc(text, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && !search.IsCombiningAccent(r) }) {
		if topics[search.FoldWord(word)] {
			return true
		}
	}
	return false
}

func asksRecordedContent(plan assistant.Plan) bool {
	if plan.Kind != assistant.KindLookup && plan.Kind != assistant.KindRecap {
		return false
	}
	media, content := false, false
	for _, word := range strings.Fields(strings.ToLower(plan.Question)) {
		switch strings.Trim(word, "!?.,:;()[]{}\"'") {
		case "url", "link", "links", "file", "filename":
			return false
		case "video", "videos":
			media = true
		case "meeting", "meetings", "call", "calls", "standup", "sync", "session", "sessions",
			"interview", "interviews", "webinar", "webinars", "lecture", "lectures", "conference", "conferences", "podcast", "podcasts", "keynote", "keynotes":
			media = true
		case "recording", "recordings", "transcript", "transcripts":
			media, content = true, true
		case "about", "said", "say", "saying", "content", "explained", "summary", "summarize", "summarise", "recap", "recapped", "recapping",
			"discuss", "discussed", "discussing", "talk", "talked", "talking", "covered", "agreed", "decide", "decided", "decisions", "mentioned", "happen", "happened",
			"action", "item", "items", "point", "points", "takeaway", "takeaways", "outcome", "outcomes", "commitment", "commitments", "promise", "promises", "promised", "settle", "settled", "assigned", "assignment", "assignments", "owner", "owners":
			content = true
		}
	}
	return media && content || savedVoiceNoteReference(plan.Question)
}

func asksAboutSavedRecording(plan assistant.Plan) bool {
	question := strings.ToLower(plan.Question)
	if savedVoiceNoteReference(question) {
		return true
	}
	for _, word := range strings.Fields(strings.ToLower(plan.Question)) {
		word = strings.Trim(word, "!?.,:;()[]{}\"'")
		if recordedSessionWord(word) || word == "recording" || word == "recordings" || word == "transcript" || word == "transcripts" {
			return true
		}
	}
	return false
}

func savedVoiceNoteReference(question string) bool {
	if strings.Contains(strings.ToLower(question), "voice note") {
		return true
	}
	for _, word := range strings.Fields(strings.ToLower(question)) {
		switch strings.Trim(word, "!?.,:;()[]{}\"'") {
		case "spraaknotitie", "spraaknotities", "voicenotitie", "voicenotities", "opname", "opnames":
			return true
		}
	}
	return false
}

// A website filter describes captured browser pages, not the provenance of
// separately recorded audio. Explicit saved-source requests use the archive;
// ambiguous video recall uses a bounded recent transcript page, with unverified
// website association. Browser chrome never becomes spoken content.
func (c *Client) recordedContentEvidence(ctx context.Context, plan assistant.Plan) ([]assistant.Evidence, bool, error) {
	if !asksRecordedContent(plan) || !c.CanUseMeetings() {
		return nil, false, nil
	}
	read, stable := c.beginVoicePublication()
	if !stable || read.fence == nil && plan.Scope.End.Sub(plan.Scope.Start) > searchWindowSpan {
		return nil, false, ErrMemoryChanging
	}
	evidence, limited, err := c.readRecordedContentEvidence(ctx, plan)
	if err != nil {
		return nil, false, err
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if !read.publish(c, func() {}) {
		return nil, false, ErrMemoryChanging
	}
	return evidence, limited, nil
}

func (c *Client) readRecordedContentEvidence(ctx context.Context, plan assistant.Plan) ([]assistant.Evidence, bool, error) {
	start, end := plan.Scope.Start, plan.Scope.End
	now := c.askClock()
	// An explicit meeting reference identifies saved meeting content. The short
	// horizon is only a heuristic for associating a recording with nearby video
	// activity when the user asks about a video without naming a meeting.
	if start.IsZero() && !asksAboutSavedRecording(plan) {
		start = now.Add(-voiceRecallHorizon)
	}
	if end.IsZero() || end.After(now) {
		end = now.Add(time.Nanosecond)
	}
	if !end.After(start) {
		return nil, false, nil
	}
	topics := voiceRecallTopics(plan)
	if asksAboutSavedRecording(plan) && (len(topics) > 0 || !plan.Scope.IsZero()) {
		indexed, limited, err := c.indexedMeetingTopicEvidence(ctx, plan, topics, start, end)
		if err != nil {
			return nil, false, err
		}
		if len(indexed) > 0 {
			return indexed, limited, nil
		}
	}
	page, err := c.meetings.repo.ListMeetings(ctx, meetings.PageRequest{Limit: 6})
	if err != nil {
		return nil, false, err
	}
	if len(page.Meetings) > 6 {
		return nil, false, ErrInvalidRequest
	}
	for _, meeting := range page.Meetings {
		if meeting.Validate() != nil {
			return nil, false, ErrInvalidRequest
		}
	}
	anchor := func(m meetings.Meeting) time.Time {
		if !m.RecordingStartedAt.IsZero() {
			return m.RecordingStartedAt
		}
		return m.StartedAt
	}
	sort.SliceStable(page.Meetings, func(i, j int) bool { return anchor(page.Meetings[i]).After(anchor(page.Meetings[j])) })
	reads := 0
	for _, meeting := range page.Meetings {
		if !anchor(meeting).Before(end) || !meeting.EndedAt.IsZero() && meeting.EndedAt.Before(start) || meeting.RecordCount == 0 {
			continue
		}
		var evidence []assistant.Evidence
		matched := voiceRecallTopicMatch(meeting.Title, topics)
		after := uint64(0)
		for {
			if reads == 3 {
				if matched {
					return evidence, true, nil
				}
				return nil, true, nil
			}
			reads++
			rows, err := c.meetings.repo.MeetingRecords(ctx, meeting.ID, meetings.PageRequest{After: after, Limit: 20, MaxBytes: 128 << 10})
			if err != nil {
				return nil, false, err
			}
			if len(rows.Records) > 20 || rows.More && rows.Next <= after {
				return nil, false, ErrInvalidRequest
			}
			for _, row := range rows.Records {
				if row.Record.Kind != meetings.TranscriptFinal {
					continue
				}
				item, err := c.meetingSummaryEvidence(ctx, meeting, row)
				if err != nil {
					return nil, false, err
				}
				if item.CapturedAt.Before(start) || !item.CapturedAt.Before(end) {
					continue
				}
				if !technicalSubjectSupport(plan.TechnicalSubjects, item.Text, meeting.Title) {
					continue
				}
				item.AppName = "Voice transcript (" + row.Record.Event.TrackID + ")"
				// A named meeting directly identifies its transcript. Video recall
				// still carries an uncertain association because the recording may
				// not correspond to the visible video.
				item.AssociationUnverified = !asksAboutSavedRecording(plan)
				matched = matched || voiceRecallTopicMatch(item.Text, topics)
				if len(evidence) == assistant.MaxEvidenceItems {
					copy(evidence, evidence[1:])
					evidence[len(evidence)-1] = item
				} else {
					evidence = append(evidence, item)
				}
				if matched && len(evidence) == assistant.MaxEvidenceItems {
					return evidence, rows.More || row.Sequence != rows.Next || page.More, nil
				}
			}
			if !rows.More {
				break
			}
			after = rows.Next
		}
		if len(evidence) > 0 && matched {
			return evidence, page.More, nil
		}
	}
	return nil, page.More, nil
}

// Topic-specific meeting recall uses the encrypted source index, so a relevant
// older transcript remains findable after it falls off the recent-meetings
// page. Both lexical and meaning search stay constrained to Blur meeting
// sources, and every returned source is checked against the authenticated
// meeting reverse edge before it can become Ask evidence.
func (c *Client) indexedMeetingTopicEvidence(ctx context.Context, plan assistant.Plan, topics map[string]bool, start, end time.Time) ([]assistant.Evidence, bool, error) {
	terms := make([]string, 0, len(topics))
	for term := range topics {
		terms = append(terms, term)
	}
	sort.Strings(terms)
	if len(plan.TechnicalSubjects) > 0 {
		terms = plan.LiteralTerms
	}
	if c.data == nil {
		return nil, false, nil
	}
	var page search.ExactPage
	var err error
	if len(terms) == 0 {
		// A dated archive request is a browse of saved voice sources, not the
		// newest six sessions. Reuse the bounded shared source-window reader.
		voicePlan := assistant.Plan{AppIDs: []string{"sh.blur.meetings"}, Scope: assistant.Scope{Start: start, End: end}}
		page, err = c.browseSearchScope(ctx, voicePlan, planEvidenceItems)
	} else {
		windows := singleSearchWindow(searchScopeWindow{})
		if !start.IsZero() {
			windows, err = splitSearchWindows(start, end)
		}
		if err != nil {
			return nil, false, err
		}
		// Voice sources already have immutable recording times; no foreground
		// activity membership scan is needed to query their source index.
		pool := planEvidenceItems
		if len(plan.TechnicalSubjects) > 0 {
			pool = planLookupPool
		}
		page, err = c.searchExactWindows(ctx, search.ExactQuery{
			Terms: terms, AppID: "sh.blur.meetings", TopK: pool,
			MaxRows: min(search.MaxIndexRows, pool*16), MaxBytes: search.MaxIndexBytes,
		}, windows)
		if err == nil && !plan.Literal {
			scope := semantic.Query{Limit: planEvidenceItems, AppIDs: []string{"sh.blur.meetings"}, Start: start, End: end}
			if start.IsZero() {
				scope.End = time.Time{}
			}
			page, err = c.hybridAskPage(ctx, plan.Question, page, scope, planEvidenceItems)
		}
	}
	if err != nil {
		return nil, false, err
	}
	page = boundSearchHydration(page)
	hits, err := c.hydrateSearchPage(ctx, page)
	if err != nil {
		return nil, false, err
	}
	reader, ok := c.data.(meetingSourceReader)
	kinds, knowsKind := c.data.(meetingRecordKindReader)
	if !ok || !knowsKind {
		return nil, page.HasMore || page.CoverageLimited || page.IndexingPending, nil
	}
	evidence := make([]assistant.Evidence, 0, len(hits))
	for _, hit := range hits {
		if hit.AppID != "sh.blur.meetings" || !strings.HasSuffix(hit.Domain, ".meeting.local") {
			continue
		}
		meeting, found, err := reader.MeetingForSource(ctx, hit.SourceID)
		if err != nil {
			return nil, false, err
		}
		if !found || meeting.Validate() != nil {
			continue
		}
		if !technicalSubjectSupport(plan.TechnicalSubjects, hit.Text, meeting.Title) {
			continue
		}
		kind, live, err := kinds.MeetingRecordKindForSource(ctx, hit.SourceID)
		if err != nil {
			return nil, false, err
		}
		if !live {
			continue
		}
		item := newEvidence(hit.SourceID, hit.AppID, hit.Domain, hit.Text)
		switch kind {
		case meetings.TranscriptFinal:
			item.AppName = "Meeting transcript"
		case meetings.UserNote:
			item.AppName = "Meeting manual note"
		default:
			continue
		}
		if strings.TrimSpace(meeting.Title) != "" {
			item.AppName += ": " + meeting.Title
		}
		item.CapturedAt, item.TimeUnknown = hit.CapturedAt, hit.TimeUnknown
		evidence = append(evidence, item)
		if len(evidence) == assistant.MaxEvidenceItems {
			break
		}
	}
	return evidence, page.HasMore || page.CoverageLimited || page.IndexingPending, nil
}
