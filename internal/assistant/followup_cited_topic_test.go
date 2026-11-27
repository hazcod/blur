package assistant

import (
	"slices"
	"testing"
	"time"
)

func TestNewTopicDropsEarlierContentReference(t *testing.T) {
	now := time.Now()
	initial, _ := PlanQuestion("What did I read about Cedar?", now, nil)
	date, _ := PlanQuestion("And yesterday?", now, nil)
	previous := inheritContentContinuation(date.Inherit(initial), initial)
	if previous.ReferenceQuestion != initial.Question {
		t.Fatal("scope followup lost content reference", previous)
	}
	current, _ := PlanQuestion("And Maple?", now, nil)
	got := followUpPlan(current, previous)
	if got.ReferenceQuestion != "" || !slices.Equal(got.Terms, []string{"maple"}) {
		t.Fatal("new topic reused old content reference", got)
	}
}
