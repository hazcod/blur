package client

import (
	"context"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/chronicle"
)

type recoveryReader struct {
	calls        []time.Time
	withEvidence bool
}

func (r *recoveryReader) CaptureHourEvidence(_ context.Context, start time.Time) (chronicle.Hour, error) {
	r.calls = append(r.calls, start)
	hour := chronicle.Hour{Start: start, End: start.Add(time.Hour)}
	if r.withEvidence {
		hour.Evidence = []chronicle.Evidence{workerEvidence(start)}
	}
	return hour, nil
}

type recoveryScheduler struct {
	enabled bool
	hours   []chronicle.Hour
}

func workerEvidence(at time.Time) chronicle.Evidence {
	o, _ := capture.SanitizeObservation("editor.app", "", "recovery fixture", 1024)
	return chronicle.Evidence{Observation: o, At: at.Add(time.Minute), Salience: 1}
}
func (s *recoveryScheduler) BackgroundSummariesEnabled() bool { return s.enabled }
func (s *recoveryScheduler) ScheduleClosedHour(_ context.Context, h chronicle.Hour) error {
	s.hours = append(s.hours, h)
	return nil
}

func TestChronicleRecoveryIsConsentGatedAndReadsOnlyPrevious24ClosedHours(t *testing.T) {
	now := time.Date(2026, 11, 6, 14, 34, 0, 0, time.UTC)
	reader := &recoveryReader{withEvidence: true}
	scheduler := &recoveryScheduler{}
	if err := recoverClosedChronicles(context.Background(), now, reader, scheduler); err != nil {
		t.Fatal(err)
	}
	if len(reader.calls) != 0 {
		t.Fatal("read capture history while background summaries were disabled")
	}
	scheduler.enabled = true
	if err := recoverClosedChronicles(context.Background(), now, reader, scheduler); err != nil {
		t.Fatal(err)
	}
	if len(reader.calls) != maxChronicleRecoveryHours || len(scheduler.hours) != maxChronicleRecoveryHours {
		t.Fatalf("recovery scanned %d/scheduled %d, want %d", len(reader.calls), len(scheduler.hours), maxChronicleRecoveryHours)
	}
	if !reader.calls[0].Equal(now.Truncate(time.Hour).Add(-24*time.Hour)) || !reader.calls[len(reader.calls)-1].Equal(now.Truncate(time.Hour).Add(-time.Hour)) {
		t.Fatalf("recovery hours not closed and chronological: %v .. %v", reader.calls[0], reader.calls[len(reader.calls)-1])
	}
}
