package assistant

import (
	"fmt"
	"testing"
	"time"
)

func timelineID(n int) string { return fmt.Sprintf("%064x", n) }

func TestDayBalancedEvidenceGivesEveryDayOneItemBeforeAnyDayGetsTwo(t *testing.T) {
	zone := time.UTC
	var items []Evidence
	// Sunday is crowded with 12 hour recaps; Monday to Saturday hold one capture each.
	for day := 9; day <= 14; day++ {
		items = append(items, Evidence{SourceID: timelineID(day), CapturedAt: time.Date(2026, 11, day, 14, 0, 0, 0, zone)})
	}
	for hour := 0; hour < 12; hour++ {
		items = append(items, Evidence{SourceID: timelineID(100 + hour), Layer: LayerHour, CapturedAt: time.Date(2026, 11, 15, 8+hour, 0, 0, 0, zone)})
	}
	got, limited := DayBalancedEvidence(items, 8, zone)
	if !limited || len(got) != 8 {
		t.Fatalf("got %d items limited=%t, want 8 limited", len(got), limited)
	}
	perDay := map[int]int{}
	for _, item := range got {
		perDay[item.CapturedAt.Day()]++
	}
	for day := 9; day <= 14; day++ {
		if perDay[day] != 1 {
			t.Fatalf("day %d has %d items, want exactly one before Sunday takes a second: %v", day, perDay[day], perDay)
		}
	}
	if perDay[15] != 2 {
		t.Fatalf("Sunday got %d items, want the two remaining slots", perDay[15])
	}
}

func TestDayBalancedEvidenceIsChronologicalAndStable(t *testing.T) {
	zone := time.UTC
	items := []Evidence{
		{SourceID: timelineID(3), CapturedAt: time.Date(2026, 11, 11, 9, 0, 0, 0, zone)},
		{SourceID: timelineID(1), CapturedAt: time.Date(2026, 11, 9, 9, 0, 0, 0, zone)},
		{SourceID: timelineID(2), CapturedAt: time.Date(2026, 11, 10, 9, 0, 0, 0, zone)},
	}
	got, limited := DayBalancedEvidence(items, 12, zone)
	if limited || len(got) != 3 || got[0].SourceID != timelineID(1) || got[2].SourceID != timelineID(3) {
		t.Fatalf("got %v limited=%t, want oldest first and nothing limited", got, limited)
	}
}

func TestDayBalancedEvidenceUsesLocalDays(t *testing.T) {
	zone := time.FixedZone("CET", 3600)
	// 23:30 UTC on the 9th is already the 10th in CET.
	items := []Evidence{
		{SourceID: timelineID(1), CapturedAt: time.Date(2026, 11, 9, 23, 30, 0, 0, time.UTC)},
		{SourceID: timelineID(2), CapturedAt: time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)},
		{SourceID: timelineID(3), CapturedAt: time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)},
	}
	got, _ := DayBalancedEvidence(items, 2, zone)
	// Items 1 and 2 share a local day, so the second slot goes to day three.
	if len(got) != 2 || got[1].SourceID != timelineID(3) {
		t.Fatalf("got %v, want one item for the 10th and one for the 11th", got)
	}
}

func TestDayBalancedEvidenceMoreDaysThanSlotsStillSpansThePeriod(t *testing.T) {
	zone := time.UTC
	var items []Evidence
	for day := 1; day <= 20; day++ {
		items = append(items, Evidence{SourceID: timelineID(day), CapturedAt: time.Date(2026, 10, day, 12, 0, 0, 0, zone)})
	}
	got, limited := DayBalancedEvidence(items, 5, zone)
	if !limited || len(got) != 5 || got[0].SourceID != timelineID(1) || got[4].SourceID != timelineID(20) {
		t.Fatalf("got %v limited=%t, want both ends of the period kept", got, limited)
	}
}

func TestChronologicalPackingListsOldestFirstWithRollupsAhead(t *testing.T) {
	zone := time.UTC
	evidence := []Evidence{
		{SourceID: timelineID(1), Text: "raw monday", CapturedAt: time.Date(2026, 11, 9, 14, 0, 0, 0, zone)},
		{SourceID: timelineID(2), Text: "hour sunday", Layer: LayerHour, CapturedAt: time.Date(2026, 11, 15, 14, 0, 0, 0, zone), EndAt: time.Date(2026, 11, 15, 15, 0, 0, 0, zone)},
		{SourceID: timelineID(3), Text: "day tuesday", Layer: LayerDay, CapturedAt: time.Date(2026, 11, 10, 0, 0, 0, 0, zone), EndAt: time.Date(2026, 11, 11, 0, 0, 0, 0, zone)},
		{SourceID: timelineID(4), Text: "day monday", Layer: LayerDay, CapturedAt: time.Date(2026, 11, 9, 0, 0, 0, 0, zone), EndAt: time.Date(2026, 11, 10, 0, 0, 0, 0, zone)},
		{SourceID: timelineID(5), Text: "raw wednesday", CapturedAt: time.Date(2026, 11, 11, 10, 0, 0, 0, zone)},
	}
	packed, err := packContextWithOptions("what did I do last week", evidence, nil, 1<<20, contextOptions{balanced: true, chronological: true})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{timelineID(4), timelineID(3), timelineID(1), timelineID(5), timelineID(2)}
	if fmt.Sprint(packed.SourceIDs) != fmt.Sprint(want) {
		t.Fatalf("packed %v, want rollups first then oldest to newest %v", packed.SourceIDs, want)
	}
	// Without the flag the established layer order is unchanged: hours before raw captures.
	layered, err := packContextWithOptions("what did I do last week", evidence, nil, 1<<20, contextOptions{balanced: true})
	if err != nil {
		t.Fatal(err)
	}
	if layered.SourceIDs[2] != timelineID(2) {
		t.Fatalf("short-window packing changed: %v", layered.SourceIDs)
	}
}
