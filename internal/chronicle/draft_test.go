package chronicle

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
)

func draftEvidence(id int, at time.Time, salience uint8, text string) Evidence {
	o, err := capture.SanitizeObservation("draft-test", "local", text, capture.MaxObservationTextBytes)
	if err != nil {
		panic(err)
	}
	_ = id
	return Evidence{Observation: o, At: at, Salience: salience}
}

func TestDraftHour(t *testing.T) {
	start := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	now := start.Add(25 * time.Minute)
	hourOf := func(ev ...Evidence) Hour { return Hour{Start: start, End: start.Add(time.Hour), Evidence: ev} }
	min := func(n int) time.Duration { return time.Duration(n) * time.Minute }

	t.Run("empty", func(t *testing.T) {
		d := DraftHour(hourOf(), nil, now)
		if len(d.Lines) != 0 || d.Sources != 0 || d.Omitted {
			t.Fatalf("draft = %+v", d)
		}
		if !d.Start.Equal(start) || !d.End.Equal(now) {
			t.Fatalf("bounds = %v %v", d.Start, d.End)
		}
	})
	t.Run("sorts and limits activity", func(t *testing.T) {
		d := DraftHour(hourOf(), []ActivityLine{{"Xcode", min(9)}, {"Safari", min(22)}, {"Mail", min(9)}, {"Slack", min(2)}, {"Idle", 10 * time.Second}}, now)
		want := []string{"Safari, 22 min", "Mail, 9 min", "Xcode, 9 min"}
		if strings.Join(d.Lines, "|") != strings.Join(want, "|") || !d.Omitted {
			t.Fatalf("lines = %q omitted=%v", d.Lines, d.Omitted)
		}
	})
	t.Run("salience order and dedupe", func(t *testing.T) {
		d := DraftHour(hourOf(
			draftEvidence(1, start.Add(time.Minute), 1, "low one"),
			draftEvidence(2, start.Add(2*time.Minute), 5, "Quarterly Plan"),
			draftEvidence(3, start.Add(3*time.Minute), 3, "quarterly plan"),
		), nil, now)
		if strings.Join(d.Lines, "|") != "Quarterly Plan|low one" || d.Sources != 3 {
			t.Fatalf("draft = %+v", d)
		}
	})
	t.Run("urls paths and shouting", func(t *testing.T) {
		d := DraftHour(hourOf(
			draftEvidence(1, start, 9, "https://example.com/secret"),
			draftEvidence(2, start.Add(time.Second), 8, "Notes at ~/Documents/x.md today"),
			draftEvidence(3, start.Add(2*time.Second), 7, "URGENT WARNING DO NOT IGNORE"),
		), nil, now)
		if strings.Join(d.Lines, "|") != "Notes at today" {
			t.Fatalf("lines = %q", d.Lines)
		}
	})
	t.Run("truncates on word boundary", func(t *testing.T) {
		d := DraftHour(hourOf(draftEvidence(1, start, 1, strings.Repeat("alpha ", 30))), nil, now)
		if len(d.Lines) != 1 || len([]rune(d.Lines[0])) > 80 || !strings.HasSuffix(d.Lines[0], "alpha…") {
			t.Fatalf("lines = %q", d.Lines)
		}
	})
	t.Run("bounds", func(t *testing.T) {
		var ev []Evidence
		for i := 0; i < 10; i++ {
			ev = append(ev, draftEvidence(i, start.Add(time.Duration(i)*time.Second), uint8(i+1), fmt.Sprintf("title %d %s", i, strings.Repeat("word ", 20))))
		}
		acts := []ActivityLine{{"A", min(5)}, {"B", min(4)}, {"C", min(3)}, {"D", min(2)}}
		d := DraftHour(hourOf(ev...), acts, now)
		total := 0
		for _, l := range d.Lines {
			total += len(l)
		}
		if len(d.Lines) > MaxDraftLines || total > MaxDraftBytes || len(d.Lines) != 7 || !d.Omitted {
			t.Fatalf("lines=%d bytes=%d omitted=%v", len(d.Lines), total, d.Omitted)
		}
		again := DraftHour(hourOf(ev...), acts, now)
		if strings.Join(again.Lines, "|") != strings.Join(d.Lines, "|") {
			t.Fatal("not deterministic")
		}
		long := []ActivityLine{{strings.Repeat("é", 300), min(5)}, {strings.Repeat("ü", 300), min(4)}, {"C", min(3)}}
		d = DraftHour(hourOf(), long, now)
		total = 0
		for _, l := range d.Lines {
			total += len(l)
		}
		if total > MaxDraftBytes || len(d.Lines) != 1 && len(d.Lines) != 2 {
			t.Fatalf("byte bound: lines=%d bytes=%d", len(d.Lines), total)
		}
	})
}
