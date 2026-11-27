package client

import (
	"context"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReviewLongRecapEmptySampleRemainsLimited(t *testing.T) {
	t.Setenv("TZ", "UTC")
	ctx := context.Background()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "calendar.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	c := NewClient(store)
	defer c.Close()
	c.askNow = func() time.Time { return time.Date(2026, 11, 21, 15, 0, 0, 0, time.UTC) }
	o, err := capture.SanitizeObservation("com.apple.mail", "", "Reviewed the saved February release evidence.", capture.MaxObservationTextBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(ctx, o, time.Date(2024, 2, 10, 10, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	result, err := c.AskMemory(ctx, "Summarize my activity in 2024")
	if err != nil {
		t.Fatal(err)
	}
	if !result.CoverageLimited || strings.Contains(result.Answer.Text, "Nothing captured") {
		t.Fatalf("known saved source omitted by bounded sampling was reported as an empty year: limited=%v text=%q", result.CoverageLimited, result.Answer.Text)
	}
}

func TestReviewNativeCalendarScopePreservesSharedTopicNumber(t *testing.T) {
	t.Setenv("TZ", "UTC")
	ctx := context.Background()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "topic.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	c := NewClient(store)
	defer c.Close()
	c.askNow = func() time.Time { return time.Date(2026, 11, 21, 15, 0, 0, 0, time.UTC) }
	var target string
	for i, text := range []string{"Project 2026: approved the Aurora supplier review.", "Project 42: approved an unrelated supplier review."} {
		o, err := capture.SanitizeObservation("com.apple.mail", "", text, capture.MaxObservationTextBytes)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.PutObservationAt(ctx, o, time.Date(2026, 11, 18+i, 10, 0, 0, 0, time.UTC)); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			target = o.ContentID()
		}
	}
	result, err := c.AskMemory(ctx, "Find project 2026 in November 2026")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Sources) == 0 || result.Sources[0].SourceID != target {
		t.Fatalf("calendar date dropped the subject number and promoted another project: %+v", result)
	}
}
