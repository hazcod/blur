package tasks

import (
	"testing"
	"time"
)

func TestResolveWallTimeRejectsDSTGapAndAsksForFoldChoice(t *testing.T) {
	if _, err := ResolveWallTime("2027-03-28", "02:30", "Europe/Brussels", ""); err != ErrNonexistentTime {
		t.Fatalf("DST gap silently shifted: %v", err)
	}
	if _, err := ResolveWallTime("2027-10-31", "02:30", "Europe/Brussels", ""); err != ErrAmbiguousTime {
		t.Fatalf("DST fold chosen without consent: %v", err)
	}
	first, err := ResolveWallTime("2027-10-31", "02:30", "Europe/Brussels", "first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := ResolveWallTime("2027-10-31", "02:30", "Europe/Brussels", "second")
	if err != nil || second.Sub(first) != time.Hour {
		t.Fatal(first, second, err)
	}
}

func TestNextOccurrenceUsesCivilDaysAcrossDST(t *testing.T) {
	due, err := ResolveWallTime("2027-03-27", "09:00", "Europe/Brussels", "")
	if err != nil {
		t.Fatal(err)
	}
	r := Reminder{DueAt: due, Timezone: "Europe/Brussels", Repeat: Daily, Hour: 9}
	next, err := NextOccurrence(r, due)
	if err != nil || next.Sub(due) != 23*time.Hour {
		t.Fatal("daily reminder used 24h instead of local calendar", next, err)
	}
	if got := next.In(mustLocation(t, "Europe/Brussels")).Format("2006-01-02 15:04"); got != "2027-03-28 09:00" {
		t.Fatal(got)
	}
}
func mustLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	zone, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return zone
}
