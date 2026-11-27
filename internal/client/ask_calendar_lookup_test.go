package client

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/capture"
)

func TestAskTopicLookupSpansYearWithAppAndDomain(t *testing.T) {
	c, store, ids := longSearchFixture(t)
	ctx := context.Background()
	c.askNow = func() time.Time { return time.Date(2024, 12, 31, 23, 59, 30, 0, time.UTC) }
	wrongDomain, err := capture.SanitizeObservation("com.apple.mail", "other.example", "Orchid invoice read from another website.", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(ctx, wrongDomain, time.Date(2024, 9, 1, 9, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.IndexPendingExact(ctx, 100); err != nil {
		t.Fatal(err)
	}

	got, err := c.AskMemory(ctx, "What did I read about orchid invoice in Mail on finance.example this year?")
	if err != nil {
		t.Fatal("year-scoped Ask topic lookup failed:", err)
	}
	var sourceIDs []string
	for _, source := range got.Sources {
		sourceIDs = append(sourceIDs, source.SourceID)
		if source.SourceID == ids["legacy"] && !source.CapturedAt.Equal(time.Date(2024, 6, 16, 10, 0, 0, 0, time.UTC)) {
			t.Fatalf("legacy activity visit lost its in-year timestamp: %v", source.CapturedAt)
		}
	}
	want := []string{ids["early"], ids["middle"], ids["late"], ids["legacy"]}
	slices.Sort(want)
	slices.Sort(sourceIDs)
	if got.Plan.Kind != assistant.KindLookup || !slices.Equal(sourceIDs, want) {
		t.Fatalf("year topic lookup lost scoped sources: plan=%+v sources=%v want=%v", got.Plan, sourceIDs, want)
	}
	for _, query := range store.queries {
		if query.CapturedEnd.Sub(query.CapturedStart) > 31*24*time.Hour {
			t.Fatalf("Ask sent an unsupported physical index window: %+v", query)
		}
	}
	missing, err := c.AskMemory(ctx, "What did I read about nonexistentkeyword in Mail on finance.example this year?")
	if err != nil || !missing.Answer.InsufficientEvidence || len(missing.Sources) != 0 {
		t.Fatalf("missing year topic fell back to unrelated captures or failed: answer=%+v err=%v", missing, err)
	}
}

func TestAskLiteralYearLookupPreservesMatchCoverage(t *testing.T) {
	for _, count := range []int{8, 9} {
		t.Run(fmt.Sprintf("%d_matches", count), func(t *testing.T) {
			c, store, _ := todayTaskClient(t)
			ctx := context.Background()
			for i := 0; i < count; i++ {
				o, err := capture.SanitizeObservation("editor.app", "", fmt.Sprintf("launch_ready evidence %d", i), 1024)
				if err != nil {
					t.Fatal(err)
				}
				if err := store.PutObservationAt(ctx, o, time.Date(2024, time.Month(i+1), 1, 12, 0, 0, 0, time.UTC)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := store.IndexPendingExact(ctx, 100); err != nil {
				t.Fatal(err)
			}
			got, err := c.AskMemory(ctx, "Find launch_ready in 2024")
			if err != nil {
				t.Fatal(err)
			}
			if !got.Plan.Literal || len(got.Sources) != 8 || got.CoverageLimited != (count > 8) {
				t.Fatalf("year exact lookup lost match coverage: count=%d literal=%v sources=%d limited=%v", count, got.Plan.Literal, len(got.Sources), got.CoverageLimited)
			}
		})
	}
}
