package durable

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/notes"
)

func TestShortNoteWhitespaceIsPreservedWhileTodayPreviewIsNormalized(t *testing.T) {
	ctx := context.Background()
	store, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xB3), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	at := time.Date(2026, 11, 13, 15, 0, 0, 0, time.UTC)
	for i, text := range []string{
		"\n\tMeeting: release planning\nDecision: defer rollout.\n  ",
		" \r\nMeeting: incident review\r\nDecision: retain evidence.\r\n\r\n",
	} {
		observation, err := capture.SanitizeObservation("sh.blur.notes", "local", text, notes.MaxNoteBytes)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SaveNote(ctx, observation, at.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatalf("valid short note with surrounding whitespace rejected: %v", err)
		}
		saved, found, err := store.GetObservation(ctx, observation.ContentID())
		if err != nil || !found || saved.Text() != text {
			t.Fatalf("source was trimmed: found=%t err=%v", found, err)
		}
	}
	date, _ := localDate(at)
	snapshot, found, err := store.GetTodaySnapshot(ctx, date)
	if err != nil || !found || len(snapshot.RecentChanges) != 2 || snapshot.Validate() != nil {
		t.Fatalf("normalized Today preview unavailable: found=%t err=%v", found, err)
	}
	for _, change := range snapshot.RecentChanges {
		if change.Text != strings.TrimSpace(change.Text) || strings.ContainsAny(change.Text, "\r\n") {
			t.Fatal("display preview contains surrounding whitespace or line breaks")
		}
	}
}
