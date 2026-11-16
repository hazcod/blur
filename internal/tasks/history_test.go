package tasks

import (
	"testing"
	"time"
)

func TestTaskHistorySurvivesLaterEditsAndReopen(t *testing.T) {
	at := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	item := Item{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Revision: 1, Title: "Review PR", State: Open, UserAuthored: true, CreatedAt: at, UpdatedAt: at}
	RecordTransition(&item, "")
	item.Revision++
	item.State = Completed
	item.UpdatedAt = at.Add(time.Hour)
	RecordTransition(&item, Open)
	item.Revision++
	item.Title = "Review deployment PR"
	item.UpdatedAt = at.Add(24 * time.Hour)
	RecordTransition(&item, Completed)
	item.Revision++
	item.State = Open
	item.UpdatedAt = at.Add(25 * time.Hour)
	RecordTransition(&item, Completed)
	if len(item.History) != 3 || item.History[1].State != Completed || !item.History[1].At.Equal(at.Add(time.Hour)) || item.Validate() != nil {
		t.Fatal(item)
	}
}

func TestTaskHistoryBoundReportsOlderOmissions(t *testing.T) {
	at := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	item := Item{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Revision: 1, Title: "Review PR", State: Open, UserAuthored: true, CreatedAt: at, UpdatedAt: at}
	RecordTransition(&item, "")
	for n := 0; n < 30; n++ {
		previous := item.State
		item.State = Completed
		if previous == Completed {
			item.State = Open
		}
		item.Revision++
		item.UpdatedAt = item.UpdatedAt.Add(time.Minute)
		RecordTransition(&item, previous)
	}
	if len(item.History) != 24 || item.HistoryOmitted != 7 || item.Validate() != nil {
		t.Fatal(item)
	}
}
