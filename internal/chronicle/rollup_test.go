package chronicle

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func rollupSourceID(n int) string { return fmt.Sprintf("%064x", n+1) }

func TestRollupPeriodsFollowLocalDaysAndISOWeeks(t *testing.T) {
	ams, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Fatal(err)
	}
	level, key, start, end, ok := RollupPeriod("day:2026-11-15", ams)
	if !ok || level != "day" || key != "2026-11-15" || !start.Equal(time.Date(2026, 11, 14, 23, 0, 0, 0, time.UTC)) || !end.Equal(time.Date(2026, 11, 15, 23, 0, 0, 0, time.UTC)) {
		t.Fatalf("day period = %v %v %v %v %v", level, key, start, end, ok)
	}
	// 25 October 2026 is the autumn DST change: the local day is 25 hours.
	_, _, start, end, _ = RollupPeriod("day:2026-10-25", ams)
	if end.Sub(start) != 25*time.Hour {
		t.Fatalf("DST day length = %v", end.Sub(start))
	}
	level, key, start, end, ok = RollupPeriod("week:2026-W46", ams)
	if !ok || level != "week" || key != "2026-W46" || !start.Equal(time.Date(2026, 11, 8, 23, 0, 0, 0, time.UTC)) || !end.Equal(time.Date(2026, 11, 15, 23, 0, 0, 0, time.UTC)) {
		t.Fatalf("week period = %v %v %v %v %v", level, key, start, end, ok)
	}
	if week, ok := WeekKeyForDate("2026-11-15"); !ok || week != "2026-W46" {
		t.Fatalf("week of Sunday 15 November = %q %v", week, ok)
	}
	if week, _ := WeekKeyForDate("2027-01-01"); week != "2026-W53" {
		t.Fatalf("ISO year boundary = %q", week)
	}
	for _, bad := range []string{"day:2026-02-30", "day:2026-1-1", "week:2026-W54", "week:2026-W00", "week:2027-W53", "hour:20261115T10Z", "day:", ""} {
		if _, _, _, _, ok := RollupPeriod(bad, ams); ok {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := NewRollupPlaceholder("day:2026-11-15", "Local"); err == nil {
		t.Error("placeholder accepted the non-IANA zone name Local")
	}
}

func TestValidateRollupBoundsAndProvenance(t *testing.T) {
	good, err := NewRollupPlaceholder("day:2026-11-15", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateRollup(good); err != nil {
		t.Fatalf("placeholder rejected: %v", err)
	}
	generated := good
	generated.Text = "- A bullet."
	generated.ChildIDs = []string{"hour:20261115T09Z"}
	generated.SourceIDs = []string{rollupSourceID(1)}
	generated.InputHash = rollupSourceID(9)
	generated.GeneratedAt = time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	generated.Dirty = false
	if err := ValidateRollup(generated); err != nil {
		t.Fatalf("generated rollup rejected: %v", err)
	}
	for name, mutate := range map[string]func(*Rollup){
		"text without sources": func(r *Rollup) { r.SourceIDs = nil },
		"text without hash":    func(r *Rollup) { r.InputHash = "" },
		"text over 2048":       func(r *Rollup) { r.Text = strings.Repeat("x", MaxRollupTextBytes+1) },
		"control character":    func(r *Rollup) { r.Text = "a\x07b" },
		"unsorted children":    func(r *Rollup) { r.ChildIDs = []string{"hour:20261115T10Z", "hour:20261115T09Z"} },
		"duplicate sources":    func(r *Rollup) { r.SourceIDs = []string{rollupSourceID(1), rollupSourceID(1)} },
		"week child in a day":  func(r *Rollup) { r.ChildIDs = []string{"day:2026-11-15"} },
		"model with newline":   func(r *Rollup) { r.Model = "a\nb" },
		"sources over 256": func(r *Rollup) {
			r.SourceIDs = nil
			for i := 0; i <= MaxRollupSources; i++ {
				r.SourceIDs = append(r.SourceIDs, rollupSourceID(i))
			}
		},
	} {
		candidate := generated
		candidate.ChildIDs = append([]string(nil), generated.ChildIDs...)
		candidate.SourceIDs = append([]string(nil), generated.SourceIDs...)
		mutate(&candidate)
		if ValidateRollup(candidate) == nil {
			t.Errorf("%s accepted", name)
		}
	}
	text := good
	text.GeneratedAt = generated.GeneratedAt
	if ValidateRollup(text) == nil {
		t.Error("placeholder with generation metadata accepted")
	}
}

func TestValidateRollupTextRejectsIdentifiersAndOversize(t *testing.T) {
	for _, ok := range []string{"- Reviewed the plan.\n- Fixed the build.", "  trimmed  "} {
		if _, err := ValidateRollupText(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "   ", "- see src:abc", "- note " + rollupSourceID(3), "- hour:20261115T09Z was busy", "- day:2026-11-15 summary", "- week:2026-W46 summary", strings.Repeat("a", MaxRollupTextBytes+1), "a\x00b", "bad\xff"} {
		if _, err := ValidateRollupText(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestRollupInputDropsOldestFirstAndKeepsOrder(t *testing.T) {
	var children []RollupChild
	for i := 0; i < 20; i++ {
		children = append(children, RollupChild{ID: fmt.Sprintf("hour:202611%02dT%02dZ", 15, i), Label: fmt.Sprintf("%02d:00-%02d:00", i, i+1), Text: strings.Repeat(string(rune('a'+i)), 500), SourceIDs: []string{rollupSourceID(i)}})
	}
	input := NewRollupInput(RollupLevelDay, children)
	if !input.Omitted || len(input.Children) >= len(children) {
		t.Fatalf("oversized input was not trimmed: %d of %d omitted=%t", len(input.Children), len(children), input.Omitted)
	}
	last := children[len(children)-1]
	if input.Children[len(input.Children)-1].ID != last.ID {
		t.Fatal("newest child was dropped before older ones")
	}
	prompt, err := BuildRollupPrompt(input, "Day 2026-11-15")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt.User, "(earlier hours omitted)") || len(prompt.User) > MaxRollupInputBytes+200 {
		t.Fatalf("prompt marker/size wrong: %d bytes", len(prompt.User))
	}
	first := input.Children[0]
	if !strings.Contains(prompt.User, first.Label+": "+first.Text) || strings.Index(prompt.User, first.Label) > strings.Index(prompt.User, last.Label) {
		t.Fatal("children are not labelled and ordered oldest first")
	}
	week := NewRollupInput(RollupLevelWeek, []RollupChild{{ID: "day:2026-11-09", Label: "Mon 2026-11-09", Text: "- one", SourceIDs: []string{rollupSourceID(1)}}})
	weekPrompt, _ := BuildRollupPrompt(week, "Week 2026-W46")
	if strings.Contains(weekPrompt.User, "omitted") || !strings.Contains(weekPrompt.System, "3 to 5 bullet") || !strings.Contains(weekPrompt.System, "120 words") {
		t.Fatalf("week prompt = %+v", weekPrompt)
	}
	dayPrompt, _ := BuildRollupPrompt(input, "x")
	if !strings.Contains(dayPrompt.System, "4 to 7 bullet") || !strings.Contains(dayPrompt.System, "180 words") || !strings.Contains(dayPrompt.System, "at most 4 bullet") {
		t.Fatalf("day prompt = %q", dayPrompt.System)
	}
	if _, err := BuildRollupPrompt(RollupInput{Level: RollupLevelDay}, "x"); err == nil {
		t.Error("empty input accepted")
	}
}

func TestRollupInputHashAndSourceUnion(t *testing.T) {
	a := NewRollupInput(RollupLevelDay, []RollupChild{{ID: "hour:20261115T09Z", Text: "one", SourceIDs: []string{rollupSourceID(1), rollupSourceID(2)}}, {ID: "hour:20261115T10Z", Text: "two", SourceIDs: []string{rollupSourceID(2), rollupSourceID(3)}}})
	b := NewRollupInput(RollupLevelDay, []RollupChild{{ID: "hour:20261115T09Z", Text: "one"}, {ID: "hour:20261115T10Z", Text: "three"}})
	if a.Hash() == b.Hash() || len(a.Hash()) != 64 || a.Hash() != a.Hash() {
		t.Fatal("hash does not track child text")
	}
	if got := a.SourceIDs(); len(got) != 3 || !sortedUniqueIDs(got) {
		t.Fatalf("source union = %v", got)
	}
	var many []RollupChild
	n := 0
	for h := 0; h < 10; h++ {
		child := RollupChild{ID: fmt.Sprintf("hour:20261115T%02dZ", h), Text: "t"}
		for i := 0; i < 64; i++ {
			child.SourceIDs = append(child.SourceIDs, rollupSourceID(n))
			n++
		}
		many = append(many, child)
	}
	bounded := NewRollupInput(RollupLevelDay, many).SourceIDs()
	if len(bounded) != MaxRollupSources || !sortedUniqueIDs(bounded) {
		t.Fatalf("bounded union = %d", len(bounded))
	}
	for h := 0; h < 10; h++ {
		represented := false
		for _, id := range many[h].SourceIDs {
			if i := searchID(bounded, id); i {
				represented = true
				break
			}
		}
		if !represented {
			t.Fatalf("child %d lost all provenance in the bounded union", h)
		}
	}
}

func searchID(ids []string, id string) bool {
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}
	return false
}
