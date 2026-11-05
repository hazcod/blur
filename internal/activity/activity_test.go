package activity

import (
	"testing"
	"time"
)

func TestRepeatedSightingsConserveElapsedTime(t *testing.T) {
	start := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	engine := New(Config{MaxGap: 10 * time.Minute, RingSize: 8})
	if _, err := engine.Apply(Event{Kind: Seen, Monotonic: 0, Wall: start, SourceID: "source-1", AppID: "slack"}); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Apply(Event{Kind: Seen, Monotonic: int64(time.Minute), Wall: start.Add(time.Minute), SourceID: "source-1", AppID: "slack"}); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Apply(Event{Kind: Seen, Monotonic: int64(3 * time.Minute), Wall: start.Add(3 * time.Minute), SourceID: "source-2", AppID: "slack"}); err != nil {
		t.Fatal(err)
	}
	segments := engine.Segments()
	if got := sumDuration(segments); got != int64(3*time.Minute) {
		t.Fatalf("conserved duration = %s, want 3m", time.Duration(got))
	}
	if len(segments) != 2 || segments[0].SourceIDs[0] != "source-1" || segments[1].SourceIDs[0] != "source-1" {
		t.Fatalf("segments lost prior evidence: %#v", segments)
	}
}

func TestIdleStopsAccountingAndClockJumpDoesNotChangeDuration(t *testing.T) {
	start := time.Date(2026, 11, 5, 23, 59, 0, 0, time.UTC)
	engine := New(Config{MaxGap: time.Hour, RingSize: 8})
	_, _ = engine.Apply(Event{Kind: Seen, Monotonic: 0, Wall: start, SourceID: "s1", AppID: "editor"})
	_, err := engine.Apply(Event{Kind: Idle, Monotonic: int64(2 * time.Minute), Wall: start.Add(-24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = engine.Apply(Event{Kind: Seen, Monotonic: int64(12 * time.Minute), Wall: start.Add(12 * time.Minute), SourceID: "s2", AppID: "editor"})
	if err != nil {
		t.Fatal(err)
	}
	got := engine.Segments()
	if sumDuration(got) != int64(2*time.Minute) {
		t.Fatalf("eligible duration = %s, want 2m", time.Duration(sumDuration(got)))
	}
	if got[0].WallEnd.Sub(got[0].WallStart) != 2*time.Minute {
		t.Fatalf("wall metadata followed clock jump: %#v", got[0])
	}
}

func TestAppSwitchAndOperatingSystemStateEventsCloseSessions(t *testing.T) {
	start := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	e := New(Config{MaxGap: time.Hour, RingSize: 8})
	_, _ = e.Apply(Event{Kind: Seen, Monotonic: 0, Wall: start, SourceID: "s1", AppID: "slack"})
	_, err := e.Apply(Event{Kind: Seen, Monotonic: int64(time.Minute), Wall: start.Add(time.Minute), SourceID: "s2", AppID: "editor"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.Apply(Event{Kind: Locked, Monotonic: int64(2 * time.Minute), Wall: start.Add(2 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.Apply(Event{Kind: Seen, Monotonic: int64(20 * time.Minute), Wall: start.Add(20 * time.Minute), SourceID: "s3", AppID: "browser"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.Apply(Event{Kind: Sleeping, Monotonic: int64(21 * time.Minute), Wall: start.Add(21 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	segments := e.Segments()
	if len(segments) != 3 || segments[0].AppID != "slack" || segments[0].SourceIDs[0] != "s1" || segments[1].AppID != "editor" || segments[1].SourceIDs[0] != "s2" || segments[2].AppID != "browser" || segments[2].SourceIDs[0] != "s3" {
		t.Fatalf("app/state session boundaries: %#v", segments)
	}
	if sumDuration(segments) != int64(3*time.Minute) {
		t.Fatalf("eligible total = %s", time.Duration(sumDuration(segments)))
	}
}

func TestLongMissingEventGapIsUnclassifiedAndMonotonicRegressionRejected(t *testing.T) {
	e := New(Config{MaxGap: time.Minute, RingSize: 8})
	_, _ = e.Apply(Event{Kind: Seen, Monotonic: 5, Wall: time.Unix(0, 0), SourceID: "s1", AppID: "browser"})
	_, err := e.Apply(Event{Kind: Seen, Monotonic: int64(3 * time.Minute), Wall: time.Unix(0, int64(3*time.Minute)), SourceID: "s2", AppID: "browser"})
	if err != nil {
		t.Fatal(err)
	}
	segments := e.Segments()
	want := int64(3*time.Minute) - 5
	if sumDuration(segments) != want || segments[0].AppID != "browser" || segments[1].AppID != "" {
		t.Fatalf("gap accounting not conserved/classified: %#v", segments)
	}
	if _, err := e.Apply(Event{Kind: Idle, Monotonic: 10, Wall: time.Now()}); err == nil {
		t.Fatal("accepted monotonic regression")
	}
}

func TestExtremeGapIsSplitAndAccountingWorkIsBounded(t *testing.T) {
	e := New(Config{MaxGap: 48 * time.Hour, RingSize: 16})
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	_, _ = e.Apply(Event{Kind: Seen, Monotonic: 1, Wall: start, SourceID: "s1", AppID: "editor"})
	segments, err := e.Apply(Event{Kind: Seen, Monotonic: int64(30 * 24 * time.Hour), Wall: start.Add(30 * 24 * time.Hour), SourceID: "s2", AppID: "editor"})
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, s := range segments {
		if s.Duration > int64(24*time.Hour) {
			t.Fatalf("oversized segment: %s", time.Duration(s.Duration))
		}
		total += s.Duration
	}
	if total != int64(7*24*time.Hour) || len(segments) > 8 {
		t.Fatalf("unbounded or lost capped accounting: %s across %d segments", time.Duration(total), len(segments))
	}
	if got := e.OmittedDuration(); got != 23*24*time.Hour-1 {
		t.Fatalf("omitted=%s", got)
	}
}

func TestRulesPrecedenceAndRetroactiveClassification(t *testing.T) {
	rules := []Rule{
		{ID: "app", Version: 1, Priority: 1, AppID: "slack", Project: "work"},
		{ID: "domain", Version: 2, Priority: 2, Domain: "example.com", Project: "research"},
	}
	start := time.Unix(0, 0)
	segments := []Segment{{ID: "segment-1", WallStart: start, WallEnd: start.Add(time.Minute), Duration: int64(time.Minute), AppID: "slack", Domain: "example.com", SourceIDs: []string{"source-1"}}}
	classified, err := Classify(segments, rules)
	if err != nil {
		t.Fatal(err)
	}
	if classified[0].Project != "research" || classified[0].RuleID != "domain" || classified[0].RuleVersion != 2 {
		t.Fatalf("precedence/provenance: %#v", classified[0])
	}
	if segments[0].Project != "" {
		t.Fatal("classification mutated immutable input")
	}
}

func TestReportConservesDurationAcrossLocalMidnight(t *testing.T) {
	zone := time.FixedZone("test", 60*60)
	start := time.Date(2026, 11, 5, 23, 59, 0, 0, zone)
	segments := []Segment{{ID: "seg", WallStart: start, WallEnd: start.Add(3 * time.Minute), Duration: int64(3 * time.Minute), AppID: "editor", Project: "work", SourceIDs: []string{"src"}}}
	report, err := Report(segments, zone)
	if err != nil {
		t.Fatal(err)
	}
	if report.Eligible != int64(3*time.Minute) || report.Unclassified != 0 {
		t.Fatalf("report total: %#v", report)
	}
	if len(report.Days) != 2 || report.Days[0].Duration+report.Days[1].Duration != report.Eligible {
		t.Fatalf("midnight split: %#v", report.Days)
	}
}

func TestReportUsesLocalDSTCalendarBoundary(t *testing.T) {
	zone, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("timezone database unavailable: %v", err)
	}
	start := time.Date(2026, 3, 8, 0, 0, 0, 0, zone)
	end := time.Date(2026, 3, 9, 0, 0, 0, 0, zone)
	segment := Segment{ID: "dst-day", WallStart: start, WallEnd: end, Duration: int64(end.Sub(start)), AppID: "editor"}
	report, err := Report([]Segment{segment}, zone)
	if err != nil {
		t.Fatal(err)
	}
	if segment.Duration != int64(23*time.Hour) || len(report.Days) != 1 || report.Days[0].Duration != segment.Duration {
		t.Fatalf("DST conservation: segment=%s report=%#v", time.Duration(segment.Duration), report)
	}
}

func sumDuration(segments []Segment) int64 {
	var n int64
	for _, s := range segments {
		n += s.Duration
	}
	return n
}
