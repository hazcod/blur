package assistant

import (
	"slices"
	"testing"
	"time"
)

func TestNumberedSubjectCuesPreserveQueryRelationships(t *testing.T) {
	now := time.Date(2026, 11, 21, 15, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		question string
		want     [][2]string
	}{
		{"What time is my route 23 bus on Tuesday 23 February 2027?", [][2]string{{"route", "23"}}},
		{"Find invoice #23 approval on 23 November 2026", [][2]string{{"invoice", "23"}}},
		{"Find project 2026 plan", [][2]string{{"project", "2026"}}},
		{"Find Café 23 notes", [][2]string{{"cafe", "23"}}},
		{"When is my appointment on 2026-11-23?", nil},
		{"What time is my appointment at 14:30 UTC?", nil},
		{"Find version 2.3 release", nil},
	} {
		p, err := PlanQuestion(tc.question, now, nil)
		if err != nil || !slices.Equal(p.NumberedSubjects, tc.want) {
			t.Errorf("numbered cues for %q = %v, want %v: %v", tc.question, p.NumberedSubjects, tc.want, err)
		}
	}
}

func TestEventDateContinuationRetainsNumberedSubjectCue(t *testing.T) {
	now := time.Date(2026, 11, 21, 15, 0, 0, 0, time.UTC)
	previous, _ := PlanQuestion("What time is my route 23 bus on Monday 23 November 2026?", now, nil)
	current, _ := PlanQuestion("And on Tuesday 23 February 2027?", now, nil)
	got := current.Inherit(previous)
	if !slices.Equal(got.NumberedSubjects, [][2]string{{"route", "23"}}) {
		t.Fatalf("date continuation replaced its numbered topic: %+v", got)
	}
}
