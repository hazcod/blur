package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/meetings"
)

// Opt-in native checks use only synthetic transcripts in a temporary encrypted
// store. The canonical signed helper must be beside the Go test executable.
func TestMeetingSummaryNativeSyntheticFragmentsAndLongRecording(t *testing.T) {
	if os.Getenv("BLUR_TEST_APPLE_HELPER") == "" {
		t.Skip("requires the canonical helper beside the test binary")
	}
	fragmented := strings.Fields("Welcome everyone and thank you for joining this recorded explanation today. The subject is TCP congestion control. Slow start grows the congestion window quickly. Packet loss signals congestion, so the sender reduces its sending rate. Congestion avoidance grows the window cautiously to let the network recover.")
	long := make([]string, 96)
	for i := range long {
		switch {
		case i < 24:
			long[i] = "TCP congestion control balances throughput with network capacity. Slow start grows the congestion window quickly."
		case i < 48:
			long[i] = "Packet loss signals network congestion. The TCP sender reduces its congestion window and sending rate after a loss."
		case i < 72:
			long[i] = "Congestion avoidance increases the window cautiously. Explicit congestion notification can signal congestion before packets are lost."
		default:
			long[i] = "The main comparison is rapid growth in slow start and cautious growth in congestion avoidance. Both adapt the sender to network capacity."
		}
	}
	for _, scenario := range []struct {
		name  string
		text  []string
		span  time.Duration
		words []string
	}{
		{"split-video-fragments", fragmented, time.Second, []string{"congestion", "tcp"}},
		{"eight-minute-video", long, 5 * time.Second, []string{"congestion", "tcp", "window", "packet"}},
		{"personal-voice-note", []string{"I am thinking about balcony gardening. Herbs need enough sunlight, drainage holes, and regular watering. Basil and mint should go in separate pots because mint spreads quickly."}, 20 * time.Second, []string{"herb", "gardening", "basil", "mint"}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			c, store, _, m := summaryFixture(t, 0)
			g, err := assistant.NewAppleGenerator()
			if err != nil {
				t.Fatal(err)
			}
			defer g.CloseIdleConnections()
			c.localAI.askModel = g
			for i, text := range scenario.text {
				r := meetings.Record{Key: fmt.Sprintf("%s/transcript/microphone/native-%d", m.ID, i), Kind: meetings.TranscriptFinal, SessionID: m.ID, Event: meetings.Event{Kind: meetings.Final, SessionID: m.ID, TrackID: "microphone", SegmentID: fmt.Sprintf("native-%d", i), Revision: 1, Start: time.Duration(i) * scenario.span, End: time.Duration(i+1) * scenario.span, Text: text}}
				if _, err := store.Append(t.Context(), r); err != nil {
					t.Fatal(err)
				}
			}
			if scenario.name == "split-video-fragments" {
				page, err := store.MeetingRecords(t.Context(), m.ID, meetings.PageRequest{Limit: assistant.MaxEvidenceItems})
				if err != nil {
					t.Fatal(err)
				}
				legacy := &meetingSummaryRetriever{}
				for _, row := range page.Records {
					source, err := c.meetingSummaryEvidence(t.Context(), m, row)
					if err != nil {
						t.Fatal(err)
					}
					legacy.evidence = append(legacy.evidence, source)
				}
				old, oldErr := assistant.NewService(legacy, g, g, assistant.ContextTokenLimit).AskWithHistoryMode(t.Context(), "Summarize this page of my saved meeting transcript and manual notes. Cover decisions, open questions, and mentioned action items, with sources.", nil, "memory")
				t.Logf("legacy twelve-fragment Ask path: insufficient=%t generated_issue=%s sources=%d error=%v", old.Answer.InsufficientEvidence, old.GenerationIssue, len(old.Sources), oldErr)
			}
			var after uint64
			processed, pages := 0, 0
			for pages < 10 {
				result, page, err := c.SummarizeLocalMeetingPage(context.Background(), m.ID, after)
				if err != nil || result.Answer.InsufficientEvidence || result.Direct || result.General || result.GenerationIssue != "" || len(result.Sources) == 0 || result.SentItems != len(page.Records) || result.CoverageLimited || page.Next <= after {
					t.Fatalf("native recap unusable: sent=%d rows=%d sources=%d insufficient=%t limited=%t error=%v", result.SentItems, len(page.Records), len(result.Sources), result.Answer.InsufficientEvidence, result.CoverageLimited, err)
				}
				useful := false
				for _, word := range scenario.words {
					useful = useful || strings.Contains(strings.ToLower(result.Answer.Text), word)
				}
				if !useful {
					t.Fatal("native summary omitted the actual synthetic subject")
				}
				for _, source := range result.Sources {
					saved, found, err := store.GetObservation(t.Context(), source.SourceID)
					if err != nil || !found || saved.Text() != source.Text {
						t.Fatal("native summary lost genuine encrypted-source authority", err)
					}
				}
				processed += len(page.Records)
				pages++
				t.Logf("native recap page=%d rows=%d input_tokens=%d answer_bytes=%d citations=%d more=%t", pages, len(page.Records), result.InputTokens, len(result.Answer.Text), len(result.Sources), page.More)
				if !page.More {
					break
				}
				after = page.Next
			}
			if processed != len(scenario.text) || (len(scenario.text) > assistant.MaxTranscriptItems && pages < 2) {
				t.Fatal("native recap skipped saved transcript fragments", processed, len(scenario.text), pages)
			}
			// The explicit manual page uses the same source transform and keeps
			// its displayed 20-row range, without requiring meeting decisions.
			manual, err := c.SummarizeMeetingPage(t.Context(), m.ID, 0)
			if err != nil || manual.Answer.InsufficientEvidence || len(manual.Sources) == 0 {
				t.Fatal("manual recap did not use the transcript summary path", err)
			}
		})
	}
}

func TestMeetingSummaryNativeSyntheticFillerPrefix(t *testing.T) {
	if os.Getenv("BLUR_TEST_APPLE_HELPER") == "" {
		t.Skip("requires the canonical helper beside the test binary")
	}
	c, store, _, m := summaryFixture(t, 0)
	g, err := assistant.NewAppleGenerator()
	if err != nil {
		t.Fatal(err)
	}
	defer g.CloseIdleConnections()
	c.localAI.askModel = g
	for i := 0; i < 51; i++ {
		text := "Um."
		if i == 50 {
			text = "The video explains TCP congestion control. Slow start increases the congestion window quickly; packet loss causes the sender to reduce its sending rate, while congestion avoidance grows the window cautiously."
		}
		r := meetings.Record{Key: fmt.Sprintf("%s/transcript/microphone/filler-%d", m.ID, i), Kind: meetings.TranscriptFinal, SessionID: m.ID, Event: meetings.Event{Kind: meetings.Final, SessionID: m.ID, TrackID: "microphone", SegmentID: fmt.Sprintf("filler-%d", i), Revision: 1, Start: time.Duration(i) * time.Second, End: time.Duration(i+1) * time.Second, Text: text}}
		if _, err := store.Append(t.Context(), r); err != nil {
			t.Fatal(err)
		}
	}
	first, page, err := c.SummarizeLocalMeetingPage(t.Context(), m.ID, 0)
	gap := errors.Is(err, assistant.ErrSummaryInsufficientEvidence) || errors.Is(err, assistant.ErrSummaryInvalidResponse)
	if !gap || first.Answer.Text != "" || len(first.Sources) != 0 || first.SentItems != 50 || len(page.Records) != 50 || page.Next != 50 || !page.More {
		t.Fatalf("native filler did not return only an honest admitted prefix: sent=%d rows=%d next=%d citations=%d err=%v", first.SentItems, len(page.Records), page.Next, len(first.Sources), err)
	}
	next, page, err := c.SummarizeLocalMeetingPage(t.Context(), m.ID, page.Next)
	if err != nil || page.More || len(page.Records) != 1 || next.SentItems != 1 || len(next.Sources) != 1 || next.Answer.InsufficientEvidence || !strings.Contains(strings.ToLower(next.Answer.Text), "congestion") {
		t.Fatal("native filler hid the useful later transcript", next.SentItems, len(next.Sources), err)
	}
	t.Logf("native filler prefix: skipped_rows=%d invalid_output=%t useful_later_rows=%d input_tokens=%d citations=%d", first.SentItems, first.GenerationIssue == assistant.GenerationInvalidResponse, next.SentItems, next.InputTokens, len(next.Sources))
}
