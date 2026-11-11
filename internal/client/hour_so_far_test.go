package client

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/chronicle"
	"github.com/hazcod/blur/internal/power"
)

func newTestWorker() *chronicle.Worker {
	repo := &feedRepo{rows: map[string]chronicle.Summary{}, notify: make(chan struct{}, 1)}
	return chronicle.NewWorker(repo, feedPolicy{}, feedModel{}, chronicle.WorkerConfig{Enabled: true, Provider: power.LocalProvider})
}

func TestChronicleFeedCurrentHourIsDeepCopyAndPurged(t *testing.T) {
	feed := newChronicleFeed()
	defer feed.Close()
	if _, ok := feed.CurrentHour(); ok {
		t.Fatal("empty feed reported an hour")
	}
	o, _ := capture.SanitizeObservation("editor.app", "", "design review notes", 1024)
	start := time.Now().UTC().Truncate(time.Hour)
	feed.mu.Lock()
	feed.worker = newTestWorker()
	feed.mu.Unlock()
	feed.accept(capturedEvidence{observation: o, at: start.Add(time.Minute)})
	hour, ok := feed.CurrentHour()
	if !ok || len(hour.Evidence) != 1 {
		t.Fatalf("hour=%+v ok=%v", hour, ok)
	}
	hour.Evidence[0].Salience = 99
	if again, _ := feed.CurrentHour(); again.Evidence[0].Salience == 99 {
		t.Fatal("CurrentHour exposed internal evidence")
	}
	feed.Purge()
	if _, ok := feed.CurrentHour(); ok {
		t.Fatal("hour survived Purge")
	}
}

func TestClientHourSoFarIsExtractiveAndHonoursMemoryFence(t *testing.T) {
	now := time.Date(2026, 11, 6, 10, 30, 0, 0, time.UTC)
	segment := activity.Segment{ID: "s1", WallStart: now.Add(-45 * time.Minute), WallEnd: now.Add(-10 * time.Minute), Duration: int64(35 * time.Minute), AppID: "com.example.editor"}
	client := NewClient(&testData{activity: []activity.Segment{segment}})
	draft, err := client.HourSoFar(context.Background(), now)
	if err != nil || len(draft.Lines) != 1 || !strings.HasSuffix(draft.Lines[0], ", 20 min") {
		t.Fatalf("draft=%+v err=%v", draft, err)
	}
	client.memoryMutationDepth = 1
	draft, err = client.HourSoFar(context.Background(), now)
	if err != nil || len(draft.Lines) != 0 {
		t.Fatalf("changing memory draft=%+v err=%v", draft, err)
	}
}

func TestHourSoFarCadence(t *testing.T) {
	now := time.Now()
	if !hourSoFarStale(time.Time{}, now) || hourSoFarStale(now.Add(-time.Minute), now) || !hourSoFarStale(now.Add(-2*time.Minute), now) {
		t.Fatal("unexpected staleness")
	}
	if got := hourSoFarUpdatedLabel(now.Add(-2*time.Minute), now); got != "Updated 2 min ago" {
		t.Fatal(got)
	}
}
