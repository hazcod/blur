package client

import (
	"context"
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

func appendArchiveVoice(t *testing.T, store *durable.Store, id, title string, at time.Time, texts []string) (meetings.Meeting, []string) {
	t.Helper()
	m := meetings.Meeting{ID: id, Title: title, Locale: "en-US", StartedAt: at, State: meetings.Running, Tracks: []string{"microphone"}, Revision: 1}
	if err := store.PutMeeting(context.Background(), m, 0); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i, text := range texts {
		segment := fmt.Sprintf("phrase-%d", i)
		r := meetings.Record{Key: id + "/transcript/microphone/" + segment, Kind: meetings.TranscriptFinal, SessionID: id, Event: meetings.Event{Kind: meetings.Final, SessionID: id, TrackID: "microphone", SegmentID: segment, Revision: 1, Start: time.Duration(i) * time.Second, End: time.Duration(i+1) * time.Second, Text: text}}
		receipt, err := store.Append(context.Background(), r)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, receipt.SourceID)
	}
	return m, ids
}

func voiceArchiveFixture(t *testing.T) (*Client, *durable.Store, map[string]string, map[string]meetings.Meeting) {
	t.Helper()
	t.Setenv("TZ", "UTC")
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "voice-archive.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	c := NewClient(store)
	c.askNow = func() time.Time { return time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(c.Close)
	ids, notes := map[string]string{}, map[string]meetings.Meeting{}
	for _, row := range []struct{ name, date, text string }{
		{"early", "2024-01-01T09:00:00Z", "Orchid rollback owner is Mira. The release gate is signed review."},
		{"late", "2024-12-31T20:00:00Z", "Orchid rollback owner is Jules. The release gate requires two checks."},
	} {
		at, err := time.Parse(time.RFC3339, row.date)
		if err != nil {
			t.Fatal(err)
		}
		m, sources := appendArchiveVoice(t, store, row.name, "Saved voice "+row.name, at, []string{row.text})
		notes[row.name], ids[row.name] = m, sources[0]
	}
	texts := make([]string, 66)
	for i := range texts {
		texts[i] = fmt.Sprintf("Unrelated saved passage %d describes routine maintenance work.", i)
	}
	texts[65] = "Cobalt rollback code is indigo. Readiness requires a signed checklist."
	m, sources := appendArchiveVoice(t, store, "older-voice", "Saved longer voice note", time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC), texts)
	notes["older"], ids["older"] = m, sources[65]
	for i := 0; i < 8; i++ {
		appendArchiveVoice(t, store, fmt.Sprintf("newer-%d", i), "New unrelated session", time.Date(2026, 11, 22, 10, i, 0, 0, time.UTC), []string{fmt.Sprintf("The travel checklist number %d covers bags and tickets.", i)})
	}
	spoof, err := capture.SanitizeObservation("sh.blur.meetings", "forged.meeting.local", "Cobalt rollback code is crimson. This is a forged capture, not an authenticated recording.", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(context.Background(), spoof, time.Date(2026, 11, 22, 11, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	ids["spoof"] = spoof.ContentID()
	return c, store, ids, notes
}

func TestVoiceArchiveExplicitSavedSourcesOutliveVideoHorizonAndCatalog(t *testing.T) {
	c, _, ids, _ := voiceArchiveFixture(t)
	for _, question := range []string{
		"What did we discuss in the meeting about Cobalt?", // existing indexed control
		"What did I say in my voice note about Cobalt?",
		"Find the transcript about Cobalt.",
		"What did I say in my recording about Cobalt?",
		"What outcomes did we agree on in the meeting about Cobalt?",
		"What did the speaker explain in the recording about Cobalt?",
	} {
		t.Run(question, func(t *testing.T) {
			answer, err := c.AskMemory(context.Background(), question)
			if err != nil || answer.Answer.InsufficientEvidence || len(answer.Sources) != 1 || answer.Sources[0].SourceID != ids["older"] || answer.Sources[0].AssociationUnverified || !strings.Contains(answer.Sources[0].Text, "indigo") {
				t.Fatalf("saved late phrase hidden or forged/unverified: %+v %v", answer, err)
			}
		})
	}
}

func TestVoiceArchiveYearQueriesUseBothEndsAndRealOriginals(t *testing.T) {
	c, _, ids, _ := voiceArchiveFixture(t)
	for _, tc := range []struct {
		question       string
		meaningLimited bool
	}{
		{"What did we discuss in the meeting about Orchid in 2024?", true},
		{"Summarize my voice notes in 2024.", false},
	} {
		t.Run(tc.question, func(t *testing.T) {
			answer, err := c.AskMemory(context.Background(), tc.question)
			seen := map[string]bool{}
			for _, source := range answer.Sources {
				seen[source.SourceID] = true
				if source.AppID != "sh.blur.meetings" || source.AssociationUnverified || source.CapturedAt.Year() != 2024 {
					t.Errorf("lost genuine source provenance: %+v", source)
				}
			}
			if err != nil || answer.Answer.InsufficientEvidence || len(seen) != 2 || !seen[ids["early"]] || !seen[ids["late"]] || answer.CoverageLimited != tc.meaningLimited {
				t.Fatalf("year voice scope incomplete: %+v %v", answer, err)
			}
		})
	}
}

func TestVoiceArchiveManualAnnotationIsNotSpokenEvidence(t *testing.T) {
	c, s, _, notes := voiceArchiveFixture(t)
	m := notes["older"]
	r := meetings.Record{Key: m.ID + "/note/private-annotation", Kind: meetings.UserNote, SessionID: m.ID, Note: meetings.ManualNoteInput{ID: "private-annotation", At: 90 * time.Second, Text: "Juniper owner is Rhea. This is my typed annotation rather than recorded speech."}}
	receipt, err := s.Append(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := assistant.PlanQuestion("What was said in the meeting about Juniper?", c.AskTime(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.RetrievePlan(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range got.Evidence {
		if source.SourceID == receipt.SourceID && !strings.Contains(strings.ToLower(source.AppName), "manual note") {
			t.Fatalf("typed annotation presented as recorded speech: %+v", source)
		}
	}
}

func TestVoiceArchiveInternalSourceDomainsAreNotWebsiteAliases(t *testing.T) {
	c, _, _, _ := voiceArchiveFixture(t)
	apps, err := c.KnownApps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	plan, err := assistant.PlanQuestion("What did we discuss in the meeting about Orchid in 2024?", c.AskTime(), apps)
	if err != nil || len(plan.Domains) != 0 {
		t.Fatalf("internal recording address became a website qualifier: %+v %v", plan, err)
	}
}
