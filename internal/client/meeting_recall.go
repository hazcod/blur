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
			"discuss", "discussed", "discussing", "talk", "talked", "talking", "say", "said", "saying", "content", "about",
			"recap", "recapped", "recapping", "action", "item", "items", "key", "point", "points", "takeaway", "takeaways", "outcome", "outcomes",
			"commitment", "commitments", "promise", "promises", "promised", "settle", "settled", "assigned", "assignment", "assignments", "owner", "owners",
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
	for _, word := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if topics[word] {
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
	return media && content || strings.Contains(strings.ToLower(plan.Question), "voice note")
}

func asksAboutNamedSession(plan assistant.Plan) bool {
	for _, word := range strings.Fields(strings.ToLower(plan.Question)) {
		if recordedSessionWord(strings.Trim(word, "!?.,:;()[]{}\"'")) {
			return true
		}
	}
	return false
}

// A website filter describes captured browser pages, not the provenance of
// separately recorded audio. For explicit video/recording recall, inspect a
// bounded recent transcript page instead of treating browser chrome as speech.
// The website association remains explicitly unverified in the model payload.
func (c *Client) recordedContentEvidence(ctx context.Context, plan assistant.Plan) ([]assistant.Evidence, bool, error) {
	if !asksRecordedContent(plan) || !c.CanUseMeetings() {
		return nil, false, nil
	}
	start, end := plan.Scope.Start, plan.Scope.End
	now := c.askClock()
	// An explicit meeting reference identifies saved meeting content. The short
	// horizon is only a heuristic for associating a recording with nearby video
	// activity when the user asks about a video without naming a meeting.
	if start.IsZero() && !asksAboutNamedSession(plan) {
		start = now.Add(-voiceRecallHorizon)
	}
	if end.IsZero() || end.After(now) {
		end = now.Add(time.Nanosecond)
	}
	if !end.After(start) {
		return nil, false, nil
	}
	topics := voiceRecallTopics(plan)
	if asksAboutNamedSession(plan) && len(topics) > 0 {
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
				item.AppName = "Voice transcript (" + row.Record.Event.TrackID + ")"
				// A named meeting directly identifies its transcript. Video recall
				// still carries an uncertain association because the recording may
				// not correspond to the visible video.
				item.AssociationUnverified = !asksAboutNamedSession(plan)
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
	if len(terms) == 0 || c.data == nil {
		return nil, false, nil
	}
	if start.IsZero() {
		// The index treats a half-open time range as a complete pair. An
		// unconstrained meeting query should search the full encrypted index.
		end = time.Time{}
	}
	exact, err := c.data.SearchExact(ctx, search.ExactQuery{
		Terms: terms, AppID: "sh.blur.meetings", CapturedStart: start, CapturedEnd: end,
		TopK: planEvidenceItems, MaxRows: min(search.MaxIndexRows, planEvidenceItems*16), MaxBytes: search.MaxIndexBytes,
	})
	if err != nil {
		return nil, false, err
	}
	page, err := c.hybridAskPage(ctx, plan.Question, exact, semantic.Query{
		Limit: planEvidenceItems, AppIDs: []string{"sh.blur.meetings"}, Start: start, End: end,
	}, planEvidenceItems)
	if err != nil {
		return nil, false, err
	}
	page = boundSearchHydration(page)
	hits, err := c.hydrateSearchPage(ctx, page)
	if err != nil {
		return nil, false, err
	}
	reader, ok := c.data.(meetingSourceReader)
	if !ok {
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
		item := newEvidence(hit.SourceID, hit.AppID, hit.Domain, hit.Text)
		item.AppName = "Meeting: " + meeting.Title
		if strings.TrimSpace(meeting.Title) == "" {
			item.AppName = "Voice note"
		}
		item.CapturedAt, item.TimeUnknown = hit.CapturedAt, hit.TimeUnknown
		evidence = append(evidence, item)
		if len(evidence) == assistant.MaxEvidenceItems {
			break
		}
	}
	return evidence, page.HasMore || page.CoverageLimited || page.IndexingPending, nil
}
