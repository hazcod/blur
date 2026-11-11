package activity

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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

func TestNewSightingsReplacePriorSourceAttribution(t *testing.T) {
	start := time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)
	e := New(Config{MaxGap: time.Minute, RingSize: 128})
	for i := 0; i < 100; i++ {
		_, err := e.Apply(Event{
			Kind: Seen, Monotonic: int64(i) * int64(time.Minute),
			Wall:     start.Add(time.Duration(i) * time.Minute),
			SourceID: fmt.Sprintf("source-%03d", i), AppID: "slack",
		})
		if err != nil {
			t.Fatalf("sighting %d: %v", i, err)
		}
	}
	segments := e.Segments()
	if len(segments) != 99 {
		t.Fatalf("segments=%d, want 99", len(segments))
	}
	for i, segment := range segments {
		want := fmt.Sprintf("source-%03d", i)
		if len(segment.SourceIDs) != 1 || segment.SourceIDs[0] != want {
			t.Fatalf("segment %d sources=%v, want only most recent evidence %q", i, segment.SourceIDs, want)
		}
	}
}

func TestSegmentIDsDifferAcrossFreshProcesses(t *testing.T) {
	start := time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)
	segmentFromFreshEngine := func(wall time.Time, source string) Segment {
		engine := New(Config{MaxGap: time.Minute, RingSize: 2})
		if _, err := engine.Apply(Event{Kind: Seen, Monotonic: 0, Wall: wall, SourceID: source, AppID: "editor"}); err != nil {
			t.Fatal(err)
		}
		segments, err := engine.Apply(Event{Kind: Seen, Monotonic: int64(time.Second), Wall: wall.Add(time.Second), SourceID: source, AppID: "editor"})
		if err != nil || len(segments) != 1 {
			t.Fatalf("fresh engine emitted segments=%d err=%v", len(segments), err)
		}
		return segments[0]
	}

	first := segmentFromFreshEngine(start, "source-a")
	second := segmentFromFreshEngine(start.Add(time.Second), "source-b")
	if first.ID == second.ID {
		t.Fatalf("different process sessions reused durable segment ID %q", first.ID)
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

func TestReportConservesYearAcrossThousandsOfSegments(t *testing.T) {
	segments := syntheticYearSegments()
	report, err := Report(segments, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	want := int64(len(segments)) * int64(time.Minute)
	if report.Eligible != want || report.Unclassified != want {
		t.Fatalf("year totals eligible=%s unclassified=%s, want both %s", time.Duration(report.Eligible), time.Duration(report.Unclassified), time.Duration(want))
	}
	if len(report.Days) != 365 {
		t.Fatalf("report days = %d, want 365", len(report.Days))
	}
	var days int64
	for _, day := range report.Days {
		days += day.Duration
	}
	if days != want || len(report.Apps) != 1 || report.Apps[0].Duration != want {
		t.Fatalf("report buckets did not conserve year duration: days=%s apps=%+v", time.Duration(days), report.Apps)
	}
}

func syntheticYearSegments() []Segment {
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	segments := make([]Segment, 0, 365*11)
	for day := 0; day < 365; day++ {
		dayStart := start.AddDate(0, 0, day)
		for block := 0; block < 11; block++ {
			segmentStart := dayStart.Add(time.Duration(block*2) * time.Hour)
			segments = append(segments, Segment{
				ID:        fmt.Sprintf("year-segment-%04d-%02d", day, block),
				WallStart: segmentStart,
				WallEnd:   segmentStart.Add(time.Minute),
				Duration:  int64(time.Minute),
				AppID:     "editor",
			})
		}
	}
	return segments
}

func TestEngineForgetSourceRemovesRingAndActiveReferences(t *testing.T) {
	e := New(Config{MaxGap: time.Hour, RingSize: 8})
	start := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	for i, id := range []string{"source-a", "source-b", "source-c"} {
		_, err := e.Apply(Event{Kind: Seen, Monotonic: int64(i) * int64(time.Minute), Wall: start.Add(time.Duration(i) * time.Minute), SourceID: id, AppID: "editor"})
		if err != nil {
			t.Fatal(err)
		}
	}
	removed, err := e.ForgetSource("source-a")
	if err != nil || removed != 1 {
		t.Fatalf("ForgetSource() = (%d, %v), want one segment directly supported by source-a", removed, err)
	}
	for _, segment := range e.Segments() {
		if contains(segment.SourceIDs, "source-a") {
			t.Fatalf("forgotten source remains in ring: %#v", segment)
		}
	}
	segments, err := e.Apply(Event{Kind: Seen, Monotonic: 3 * int64(time.Minute), Wall: start.Add(3 * time.Minute), SourceID: "source-d", AppID: "editor"})
	if err != nil {
		t.Fatal(err)
	}
	for _, segment := range segments {
		if contains(segment.SourceIDs, "source-a") {
			t.Fatalf("forgotten source was re-emitted: %#v", segment)
		}
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

func TestApplyNeverEmitsInvalidSegmentsForRepeatedAndAlternatingSources(t *testing.T) {
	engine := New(Config{MaxGap: 10 * time.Minute, RingSize: 16})
	wall := time.Unix(1700000000, 0).UTC()
	var mono int64
	for i := 0; i < 500; i++ {
		id := "src-" + string(rune('a'+i%3))
		segments, err := engine.Apply(Event{Kind: Seen, Monotonic: mono, Wall: wall, SourceID: id, AppID: "org.example.app", Domain: "example.com"})
		if err != nil {
			t.Fatal(err)
		}
		for _, segment := range segments {
			if err := segment.Validate(); err != nil || len(segment.SourceIDs) > 32 {
				t.Fatalf("invalid segment %+v: %v", segment, err)
			}
		}
		mono += int64(7 * time.Second)
		wall = wall.Add(7 * time.Second)
	}
}

func TestApplyRejectsOversizedLabelsAtIntakeSoSegmentsStayValid(t *testing.T) {
	engine := New(Config{})
	wall := time.Unix(1700000000, 0).UTC()
	for _, event := range []Event{
		{Kind: Seen, Wall: wall, SourceID: "s", AppID: strings.Repeat("a", 129)},
		{Kind: Seen, Wall: wall, SourceID: "s", AppID: "app", Domain: strings.Repeat("d", 254)},
		{Kind: Seen, Wall: wall, SourceID: strings.Repeat("s", 129), AppID: "app"},
	} {
		if _, err := engine.Apply(event); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Apply(%d-byte fields) error = %v, want ErrInvalid", len(event.AppID)+len(event.Domain), err)
		}
	}
}

func TestTenMinuteSessionOfDistinctSourcesEmitsOnlyValidBoundedSegments(t *testing.T) {
	engine := New(Config{MaxGap: 10 * time.Minute, RingSize: 256})
	wall := time.Unix(1700000000, 0).UTC()
	var all []Segment
	for i := 0; i < 100; i++ {
		at := time.Duration(i) * 6 * time.Second
		segments, err := engine.Apply(Event{Kind: Seen, Monotonic: int64(at), Wall: wall.Add(at), SourceID: fmt.Sprintf("%064x", i), AppID: "org.example.app"})
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, segments...)
	}
	end := 10 * time.Minute
	segments, err := engine.Apply(Event{Kind: Idle, Monotonic: int64(end), Wall: wall.Add(end)})
	if err != nil {
		t.Fatal(err)
	}
	all = append(all, segments...)
	var total int64
	for _, segment := range all {
		if err := segment.Validate(); err != nil {
			t.Fatalf("segment invalid: %v", err)
		}
		if encoded, _ := json.Marshal(segment); len(encoded) > 16<<10 {
			t.Fatalf("segment encodes to %d bytes", len(encoded))
		}
		total += segment.Duration
	}
	if total != int64(end) {
		t.Fatalf("total duration %d, want %d", total, int64(end))
	}
}
