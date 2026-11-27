package client

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

func TestCalendarYearNativeRecallKeepsBothEndsAndAppScope(t *testing.T) {
	t.Setenv("TZ", "UTC")
	ctx := context.Background()
	s, err := durable.OpenDurable(filepath.Join(t.TempDir(), "calendar.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c := NewClient(s)
	defer c.Close()
	c.askNow = func() time.Time { return time.Date(2026, 11, 21, 15, 0, 0, 0, time.UTC) }
	var wanted []string
	for _, tc := range []struct {
		app, text string
		at        time.Time
	}{
		{"com.apple.mail", "January activity: reviewed the Aurora launch evidence.", time.Date(2024, 1, 10, 10, 0, 0, 0, time.UTC)},
		{"com.apple.mail", "December activity: closed the Orion release review.", time.Date(2024, 12, 10, 10, 0, 0, 0, time.UTC)},
		{"com.tinyspeck.slackmacgap", "Unrelated Slack activity: planned the winter party.", time.Date(2024, 1, 10, 10, 0, 0, 0, time.UTC)},
		{"com.apple.mail", "Outside the requested year: discussed next spring.", time.Date(2025, 1, 10, 10, 0, 0, 0, time.UTC)},
	} {
		o, err := capture.SanitizeObservation(tc.app, "", tc.text, capture.MaxObservationTextBytes)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.PutObservationAt(ctx, o, tc.at); err != nil {
			t.Fatal(err)
		}
		if len(wanted) < 2 {
			wanted = append(wanted, o.ContentID())
		}
	}
	result, err := c.AskMemory(ctx, "Summarize my activity in Mail in 2024")
	if err != nil || result.Answer.InsufficientEvidence || !slices.Contains(result.Answer.SourceIDs, wanted[0]) || !slices.Contains(result.Answer.SourceIDs, wanted[1]) {
		t.Fatalf("native year recap lost the beginning or end: %+v %v", result, err)
	}
	for _, source := range result.Sources {
		if !slices.Contains(wanted, source.SourceID) {
			t.Fatalf("year/app scope admitted another source: %+v", source)
		}
	}
	plan := result.Plan
	retrieved, err := c.RetrievePlan(ctx, plan)
	if err != nil || !retrieved.Diagnosis.RecapSampled || len(retrieved.Evidence) > assistant.MaxEvidenceItems {
		t.Fatalf("long recap hid bounded sampling: %+v %v", retrieved, err)
	}
	for _, id := range wanted {
		if err := s.DeleteObservationRecord(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	after, err := c.AskMemoryWithHistory(ctx, "Make it shorter", []assistant.Turn{{Question: plan.Question, Plan: plan, Answer: result.Answer.Text, SourceIDs: result.Answer.SourceIDs}})
	if err != nil || !after.Answer.InsufficientEvidence || len(after.Answer.SourceIDs) != 0 {
		t.Fatalf("deleted historical sources remained in the answer: %+v %v", after, err)
	}
}

func TestLongActivityTotalsRejectAnUnfencedCompatibilityStore(t *testing.T) {
	f := newAskFixture(t)
	defer f.client.Close()
	p := assistant.Plan{Kind: assistant.KindTime, Scope: assistant.Scope{Start: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), End: f.now, Label: "this year", Explicit: true}}
	if _, _, err := f.client.AnswerDirect(context.Background(), p); !errors.Is(err, ErrMemoryChanging) {
		t.Fatalf("long time request silently returned a tail total: %v", err)
	}
}
