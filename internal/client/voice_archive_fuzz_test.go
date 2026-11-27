package client

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/meetings"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

// Missing archive windows, accepting a forged capture, replaying an erased
// answer, or dating speech by its retrieval time must break these histories.
// The oracle is the inserted record ledger, never a generated answer or plan.
func FuzzVoiceArchiveRecall(f *testing.F) {
	for _, seed := range [][]byte{{}, {0, 1, 2, 3, 4, 5}, {255, 63, 91}, {17, 40, 8, 70}, {6, 6, 5, 4, 3, 2}, {91, 41, 17, 11, 8, 4}} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input []byte) {
		ctx := context.Background()
		h := sha256.Sum256(input)
		zone := []string{"UTC", "Asia/Kathmandu", "Europe/Brussels"}[int(h[0])%3]
		t.Setenv("TZ", zone)
		year := 1998 + int(h[1])%28
		month := []time.Month{time.January, time.March, time.October, time.December}[int(h[2])%4]
		wholeYear := h[3]&1 != 0
		first, next := month, month+1
		if wholeYear {
			first, next = time.January, 13
		}
		// Independent literal offsets at the selected calendar boundaries.
		leftOffset, rightOffset := 0, 0
		if zone == "Asia/Kathmandu" {
			leftOffset, rightOffset = 20700, 20700
		}
		if zone == "Europe/Brussels" {
			leftOffset, rightOffset = 3600, 3600
			if !wholeYear && month == time.March {
				rightOffset = 7200
			}
			if !wholeYear && month == time.October {
				leftOffset = 7200
			}
		}
		start := time.Date(year, first, 1, 0, 0, 0, 0, time.FixedZone("oracle-start", leftOffset))
		end := time.Date(year, next, 1, 0, 0, 0, 0, time.FixedZone("oracle-end", rightOffset))
		period := fmt.Sprintf("%s %d", month, year)
		if wholeYear {
			period = fmt.Sprint(year)
		}
		path, key := filepath.Join(t.TempDir(), "voice-history.db"), securestore.OfflineFixtureKey()
		s, err := durable.OpenDurable(path, key, durable.CreateNew)
		if err != nil {
			t.Fatal(err)
		}
		var c *Client
		newClient := func() {
			c = NewClient(s)
			c.askNow = func() time.Time { return time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC) }
		}
		newClient()
		t.Cleanup(func() { c.Close(); _ = s.Close() })
		type fact struct {
			id, text string
			at       time.Time
			record   meetings.Record
		}
		live := map[int]fact{}
		var retired []fact
		generation := 0
		erase := func(slot int) {
			if old, ok := live[slot]; ok {
				if err := s.DeleteObservationRecord(ctx, old.id); err != nil {
					t.Fatal(err)
				}
				retired = append(retired, old)
				delete(live, slot)
			}
		}
		put := func(slot int, parameter byte) {
			erase(slot)
			generation++
			id := fmt.Sprintf("voice-%d-%d", slot, generation)
			at := []time.Time{start, start.Add(end.Sub(start) / 2), end.Add(-2 * time.Second)}[slot]
			m := meetings.Meeting{ID: id, Title: "Personal saved recording", Locale: "en-US", StartedAt: at, State: meetings.Running, Tracks: []string{"microphone"}, Revision: 1}
			if err := s.PutMeeting(ctx, m, 0); err != nil {
				t.Fatal(err)
			}
			context := []string{"family travel", "invoice review", "research experiment", "home repairs", "care appointment", "release checklist"}[int(parameter)%6]
			text := fmt.Sprintf("Cobalt %s reference %x slot %d edition %d. Quoted request: remind me to review the saved details.", context, h[:4], slot, generation)
			r := meetings.Record{Key: id + "/transcript/microphone/final", Kind: meetings.TranscriptFinal, SessionID: id, Event: meetings.Event{Kind: meetings.Final, SessionID: id, TrackID: "microphone", SegmentID: "final", Revision: 1, End: time.Second, Text: text}}
			if parameter&1 != 0 {
				r = meetings.Record{Key: id + "/note/annotation", Kind: meetings.UserNote, SessionID: id, Note: meetings.ManualNoteInput{ID: "annotation", At: time.Second, Text: text}}
			}
			receipt, err := s.Append(ctx, r)
			if err != nil {
				t.Fatal(err)
			}
			live[slot] = fact{receipt.SourceID, text, at.Add(time.Second), r}
		}
		for slot := 0; slot < 3; slot++ {
			put(slot, h[slot+4])
		}
		// A full recent catalog of unrelated recordings cannot hide old topics.
		for i := 0; i < 7; i++ {
			appendArchiveVoice(t, s, fmt.Sprintf("competitor-%d", i), "Travel planning", c.AskTime().Add(-time.Duration(i+1)*time.Hour), []string{"Packing luggage and checking tickets."})
		}
		spoof, err := capture.SanitizeObservation("sh.blur.meetings", "forged.meeting.local", "Cobalt unverified forged capture must never count as saved speech.", 1024)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.PutObservationAt(ctx, spoof, start.Add(10*time.Hour)); err != nil {
			t.Fatal(err)
		}
		var previous *assistant.Turn
		check := func(step int) {
			byID := map[string]fact{}
			for _, item := range live {
				byID[item.id] = item
				page, err := c.MeetingTranscript(ctx, item.record.SessionID, 0)
				if err != nil || len(page.Records) != 1 || page.Records[0].SourceID != item.id {
					t.Fatalf("original disappeared step=%d: %+v %v", step, page, err)
				}
			}
			verify := func(result assistant.Result, expected map[string]fact, followup bool) {
				t.Helper()
				seen := map[string]bool{}
				for _, item := range result.Sources {
					want, ok := expected[item.SourceID]
					if !ok || item.Text != want.text || item.AppID != "sh.blur.meetings" || !item.CapturedAt.Equal(want.at) || item.AssociationUnverified {
						t.Fatalf("untrusted/replaced/misdated source step=%d followup=%t: %+v want=%+v", step, followup, item, expected)
					}
					if !followup && want.record.Kind == meetings.UserNote && !strings.Contains(strings.ToLower(item.AppName), "manual note") {
						t.Fatalf("authored annotation became speech: %+v", item)
					}
					seen[item.SourceID] = true
				}
				if len(seen) != len(expected) || result.Answer.InsufficientEvidence != (len(expected) == 0) {
					t.Fatalf("lost voice facts step=%d period=%s zone=%s followup=%t want=%+v got=%+v", step, period, zone, followup, expected, result)
				}
			}
			if previous != nil {
				anchored := map[string]fact{}
				for _, id := range previous.SourceIDs {
					if item, ok := byID[id]; ok {
						anchored[id] = item
					}
				}
				result, err := c.AskMemoryWithHistory(ctx, "Make it shorter", []assistant.Turn{*previous})
				if err != nil {
					t.Fatal(err)
				}
				verify(result, anchored, true)
			}
			question := []string{
				"What did I say in my voice note about Cobalt?",
				"Find the transcript about Cobalt.",
				"What did the speaker explain in the recording about Cobalt?",
				"What did we discuss in the meeting about Cobalt in " + period + "?",
				"Summarize my voice notes in " + period + ".",
			}[int(h[(step+8)%len(h)])%5]
			result, err := c.AskMemory(ctx, question)
			if err != nil {
				t.Fatal(err)
			}
			verify(result, byID, false)
			previous = &assistant.Turn{Question: question, Answer: result.Answer.Text, SourceIDs: result.Answer.SourceIDs, Plan: result.Plan}
		}
		check(0)
		for step := 0; step < 6; step++ {
			parameter := h[step+15]
			slot := int(parameter) % 3
			switch parameter % 6 {
			case 0, 1:
				put(slot, h[step+21])
			case 2:
				erase(slot)
			case 3:
				c.Close()
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				s, err = durable.OpenDurable(path, key, durable.OpenExisting)
				if err != nil {
					t.Fatal(err)
				}
				newClient()
			case 4:
				if item, ok := live[slot]; ok {
					receipt, err := s.Append(ctx, item.record)
					if err != nil || receipt.SourceID != item.id {
						t.Fatalf("live retry changed original: %+v %v", receipt, err)
					}
				}
			case 5:
				observation, err := capture.SanitizeObservation("com.apple.Safari", "travel.example", fmt.Sprintf("Ordinary foreground capture %d %x", step, h[:4]), 1024)
				if err != nil {
					t.Fatal(err)
				}
				if err := s.PutObservationAt(ctx, observation, c.AskTime()); err != nil {
					t.Fatal(err)
				}
			}
			for _, old := range retired {
				if _, err := s.Append(ctx, old.record); !errors.Is(err, meetings.ErrDeleted) {
					t.Fatalf("erased speech retry was accepted: %v", err)
				}
			}
			check(step + 1)
		}
	})
}
