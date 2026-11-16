package backgroundrepair

import (
	"testing"
	"time"
)

func TestRepairDeadlineSurvivesNotificationsRetriesAndRefusals(t *testing.T) {
	start := time.Unix(1, 0)
	schedule := New(start, 10*time.Minute)
	for elapsed := 6 * time.Second; elapsed < 10*time.Minute; elapsed += 6 * time.Second {
		now := start.Add(elapsed)
		if schedule.Due(now) {
			t.Fatal("early repair", elapsed)
		}
		if delay := schedule.Delay(now, time.Hour); delay != 10*time.Minute-elapsed {
			t.Fatal("notification postponed deadline", delay, elapsed)
		}
	}
	due := start.Add(10 * time.Minute)
	if !schedule.Due(due) {
		t.Fatal("repair not due")
	}
	if delay := schedule.Delay(due, time.Minute); delay != time.Minute {
		t.Fatal("refused repair busy-looped", delay)
	}
	if !schedule.Due(due.Add(time.Minute)) {
		t.Fatal("refused repair advanced deadline")
	}
	schedule.Admitted(due.Add(time.Minute))
	if schedule.Due(due.Add(2 * time.Minute)) {
		t.Fatal("admitted repair not advanced")
	}
	if delay := schedule.Delay(due.Add(2*time.Minute), time.Second); delay != time.Second {
		t.Fatal("continuation retry delayed", delay)
	}
}
