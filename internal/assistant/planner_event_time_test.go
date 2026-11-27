package assistant

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/search"
)

func TestEventTimeQuestionsDoNotBecomeObservationWindows(t *testing.T) {
	now := time.Date(2026, 11, 21, 15, 0, 0, 0, time.UTC)
	for _, question := range []string{
		"What time is my physiotherapy appointment on Monday?",
		"When is my train on Monday?",
		"When was our supplier visit last week?",
		"When will the launch happen on Monday?",
		"When does my class start on Monday?",
		"What time will I need to arrive on Monday?",
		"When will I see my physiotherapist on Monday?",
		"What time is the book reading on Monday?",
		"When do I see José on Monday?",
		"What time am I seeing José on Monday?",
	} {
		for _, planner := range []func(string, time.Time, []KnownApp) (Plan, error){PlanQuestion, PlanSearch} {
			plan, err := planner(question, now, nil)
			if err != nil || !plan.Scope.IsZero() {
				t.Fatalf("event date became an observation filter: %q %+v %v", question, plan, err)
			}
			if question == "When is my train on Monday?" && !slices.Contains(plan.Terms, "monday") {
				t.Fatalf("event weekday lost as a content cue: %+v", plan)
			}
		}
	}
}

func TestEventDateContinuationPreservesTopicIdentifiers(t *testing.T) {
	now := time.Date(2026, 11, 21, 15, 0, 0, 0, time.UTC)
	for _, topic := range []string{"route 66 bus", "May project"} {
		previous, err := PlanQuestion("What time is my "+topic+" on Monday 23 November 2026?", now, nil)
		if err != nil {
			t.Fatal(err)
		}
		current, err := PlanQuestion("And on Tuesday 24 November 2026?", now, nil)
		got := current.Inherit(previous)
		want := "66"
		if topic == "May project" {
			want = "may"
		}
		if err != nil || !slices.Contains(got.Terms, want) || slices.Contains(got.Terms, "23") || slices.Contains(got.Terms, "monday") || !slices.Contains(got.Terms, "24") {
			t.Fatalf("event-date replacement changed the topic: %+v %v", got, err)
		}
	}
}

func TestEventDateContinuationSeparatesRepeatedTopicAndDateWords(t *testing.T) {
	now := time.Date(2026, 11, 21, 15, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		question, topic, next string
	}{
		{"What time is my route 23 bus on Monday 23 November 2026?", "23", "And on Tuesday 23 February 2027?"},
		{"What time is my May project meeting in May 2027?", "may", "And in May 2028?"},
	} {
		previous, err := PlanQuestion(tc.question, now, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, question := range []string{tc.next, "And on Wednesday?"} {
			current, err := PlanQuestion(question, now, nil)
			got := current.Inherit(previous)
			if err != nil || !slices.Contains(got.Terms, tc.topic) || !got.Scope.IsZero() {
				t.Fatalf("date replacement removed a repeated topic %q: %+v %v", tc.topic, got, err)
			}
			previous = got
		}
	}
}

func TestEventDateContinuationReplacesDateSpans(t *testing.T) {
	now := time.Date(2026, 11, 21, 15, 0, 0, 0, time.UTC)
	for _, date := range []string{"on Monday 23rd November 2026", "on November 23, 2026", "on 2026-11-23", "on 23/11/2026", "in November 2026", "in 2 days", "tomorrow morning", "next week"} {
		t.Run(date, func(t *testing.T) {
			previous, _ := PlanQuestion("What time is my appointment "+date+"?", now, nil)
			current, err := PlanQuestion("And on Tuesday?", now, nil)
			got := current.Inherit(previous)
			if err != nil || !slices.Equal(got.Terms, []string{"appointment", "tuesday"}) || !got.Scope.IsZero() {
				t.Fatalf("old date survived replacement: %+v %v", got, err)
			}
		})
	}
}

func TestEventDateContinuationReplacesRelativeDate(t *testing.T) {
	now := time.Date(2026, 11, 21, 15, 0, 0, 0, time.UTC)
	previous, _ := PlanQuestion("What time is my appointment tomorrow?", now, nil)
	current, err := PlanQuestion("And on Tuesday?", now, nil)
	got := current.Inherit(previous)
	if err != nil || slices.Contains(got.Terms, "tomorrow") || !slices.Contains(got.Terms, "appointment") || !slices.Contains(got.Terms, "tuesday") || !got.Scope.IsZero() {
		t.Fatalf("event-date replacement retained the previous relative date: %+v %v", got, err)
	}
}

func TestEventDateContinuationDoesNotTreatArbitraryNumberAsDate(t *testing.T) {
	now := time.Date(2026, 11, 21, 15, 0, 0, 0, time.UTC)
	previous, _ := PlanQuestion("What time is my route 23 bus on Monday?", now, nil)
	current, _ := PlanQuestion("And 66 on Tuesday?", now, nil)
	if got, ok := eventDateContinuation(current, previous); ok {
		t.Fatalf("new numeric subject became a date-only continuation: %+v", got)
	}
}

func TestEventDateContinuationKeepsTheQueryTermBound(t *testing.T) {
	var topics []string
	for i := 0; i < search.MaxTerms; i++ {
		topics = append(topics, fmt.Sprintf("topic%02d", i))
	}
	now := time.Date(2026, 11, 21, 15, 0, 0, 0, time.UTC)
	previous, err := PlanQuestion("What time is my "+strings.Join(topics, " ")+" event on Monday?", now, nil)
	if err != nil {
		t.Fatal(err)
	}
	current, err := PlanQuestion("And on Tuesday 24 November 2026?", now, nil)
	got := current.Inherit(previous)
	if err != nil || len(got.Terms) > search.MaxTerms || !slices.Contains(got.Terms, "tuesday") {
		t.Fatalf("event date expansion exceeded retrieval bounds or lost its new date: %+v %v", got, err)
	}
}

func TestEventDateContinuationChangesContentDateWithoutCaptureFiltering(t *testing.T) {
	now := time.Date(2026, 11, 21, 15, 0, 0, 0, time.UTC)
	previous, _ := PlanQuestion("What time is my appointment on Monday?", now, nil)
	for _, question := range []string{"And on Tuesday?", "What about Tuesday?", "Tuesday?"} {
		current, err := PlanQuestion(question, now, nil)
		got := current.Inherit(previous)
		if err != nil || got.Kind != KindLookup || !got.Scope.IsZero() || !slices.Contains(got.Terms, "tuesday") || slices.Contains(got.Terms, "monday") || !slices.Contains(got.Terms, "appointment") {
			t.Fatalf("event-date continuation became captured activity: %q %+v %v", question, got, err)
		}
	}
}

func TestActivityTimeQuestionsRetainObservationWindows(t *testing.T) {
	now := time.Date(2026, 11, 21, 15, 0, 0, 0, time.UTC)
	for _, question := range []string{
		"When did I read about physiotherapy on Monday?",
		"What time was I reading on Monday?",
		"When was I in Slack on Monday?",
		"What did I read about my Monday appointment on Thursday?",
		"What time is my appointment on Monday from the email I read on Thursday?",
	} {
		plan, err := PlanQuestion(question, now, nil)
		want := "on Monday"
		if question == "What did I read about my Monday appointment on Thursday?" || question == "What time is my appointment on Monday from the email I read on Thursday?" {
			want = "on Thursday"
		}
		if err != nil || plan.Scope.Label != want {
			t.Fatalf("activity date lost or confused with event date: %q %+v %v", question, plan, err)
		}
	}
}

func TestEventTimeQuestionDoesNotInheritAnActivityWindow(t *testing.T) {
	now := time.Date(2026, 11, 21, 15, 0, 0, 0, time.UTC)
	previous, _ := PlanQuestion("What did I read yesterday?", now, nil)
	current, err := PlanQuestion("When is my appointment on Monday?", now, nil)
	if err != nil || !current.Inherit(previous).Scope.IsZero() {
		t.Fatalf("event query inherited an unrelated observation date: %+v %v", current.Inherit(previous), err)
	}
}

func TestMixedTimeQuestionRetainsEventWeekdayAsContent(t *testing.T) {
	plan, err := PlanQuestion("What time is my appointment on Monday from the email I read on Thursday?", time.Date(2026, 11, 21, 15, 0, 0, 0, time.UTC), nil)
	if err != nil || !slices.Contains(plan.Terms, "monday") || slices.Contains(plan.Terms, "thursday") || plan.Scope.Label != "on Thursday" {
		t.Fatalf("event and observation weekdays collapsed: %+v %v", plan, err)
	}
}
