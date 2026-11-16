package client

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/tasks"
)

func TestTopicalTaskQueriesKeepStateReminderAndScheduleConstraints(t *testing.T) {
	c, store, now := todayTaskClient(t)
	ctx := context.Background()
	fixtures := []struct {
		title string
		state tasks.State
		due   time.Time
	}{
		{"Drycleaners collect suit", tasks.Open, now.Add(3 * time.Hour)},
		{"Drycleaners collect coat", tasks.Completed, time.Time{}},
		{"Drycleaners return trousers", tasks.Cancelled, time.Time{}},
		{"Drycleaners drop shirt", tasks.Open, time.Time{}},
		{"Drycleaners collect bedding", tasks.Open, now.Add(27 * time.Hour)},
		{"Compiler review", tasks.Open, now.Add(2 * time.Hour)},
		{"Drycleaners overdue scarf", tasks.Open, now.Add(-21 * time.Hour)},
	}
	for i, fixture := range fixtures {
		item := tasks.Item{ID: fmt.Sprintf("%032x", i+1), Revision: 1, Title: fixture.title, State: fixture.state, CreatedAt: now.Add(-48 * time.Hour), UpdatedAt: now.Add(-24 * time.Hour), UserAuthored: true}
		if !fixture.due.IsZero() {
			item.Reminder = &tasks.Reminder{DueAt: fixture.due, Timezone: "UTC"}
			item.Delivery = tasks.Delivery{State: tasks.NeedsSync, At: item.UpdatedAt}
		}
		if err := store.PutTask(ctx, item, 0); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		question string
		ids      []int
		prefix   string
	}{
		{"What are my completed drycleaners tasks?", []int{2}, "1 completed task."},
		{"What are my cancelled drycleaners tasks?", []int{3}, "1 cancelled task."},
		{"What are my open drycleaners tasks?", []int{1, 4, 5, 7}, "4 open tasks."},
		{"What are my drycleaners reminders today?", []int{1}, "1 open reminder."},
		{"What's my next drycleaners reminder?", []int{1}, "Your next reminder:"},
		{"How many open drycleaners tasks do I have?", nil, "4 open tasks."},
		{"How many completed drycleaners tasks?", nil, "1 completed task."},
		{"Show all drycleaners tasks", []int{1, 2, 3, 4, 5, 7}, "6 tasks."},
		{"Show all completed drycleaners tasks", []int{2}, "1 completed task."},
		{"Show all open drycleaners tasks", []int{1, 4, 5, 7}, "4 open tasks."},
		{"Which drycleaners tasks did I complete yesterday?", []int{2}, "1 task completed in that period."},
	} {
		t.Run(test.question, func(t *testing.T) {
			result, err := c.AskMemory(ctx, test.question)
			if err != nil || !result.Direct || !strings.HasPrefix(result.Answer.Text, test.prefix) {
				t.Fatalf("filtered task answer = %+v, err=%v; want prefix %q", result, err, test.prefix)
			}
			if len(result.Targets) != len(test.ids) {
				t.Fatalf("targets=%+v, want task IDs %v", result.Targets, test.ids)
			}
			for _, id := range test.ids {
				found := false
				for _, target := range result.Targets {
					found = found || target.ID == fmt.Sprintf("%032x", id) && target.Revision == 1
				}
				if !found {
					t.Fatalf("missing current task %d in %+v", id, result.Targets)
				}
			}
		})
	}
}

func TestTopicalSemanticTaskQueriesFilterStateBeforeRanking(t *testing.T) {
	c, store, _ := todayTaskClient(t)
	ctx := context.Background()
	for _, state := range []tasks.State{tasks.Open, tasks.Completed} {
		created, err := c.CreateTask(ctx, "Drycleanres on monday", nil)
		if err != nil {
			t.Fatal(err)
		}
		if state == tasks.Completed {
			item := created.Item
			item.State = state
			if _, err := c.UpdateTask(ctx, item); err != nil {
				t.Fatal(err)
			}
		}
	}
	withTaskSemanticFixture(t, c, store, &taskRecallEmbedder{score: .8})
	result, err := c.AskMemory(ctx, "What are my completed clothes tasks?")
	if err != nil || !result.Direct || len(result.Sources) != 1 || len(result.Targets) != 1 || !strings.Contains(result.Sources[0].Text, "Current state: completed") || result.Targets[0].Revision != 2 {
		t.Fatalf("semantic query ignored task state: %+v, err=%v", result, err)
	}
}

func TestTopicalNextReminderDoesNotClaimAbsenceWhenMeaningSearchFails(t *testing.T) {
	c, store, now := todayTaskClient(t)
	if _, err := c.CreateTask(context.Background(), "Drycleanres on monday", &tasks.Reminder{DueAt: now.Add(3 * time.Hour), Timezone: "UTC"}); err != nil {
		t.Fatal(err)
	}
	withTaskSemanticFixture(t, c, store, &taskRecallEmbedder{score: .8, failDocuments: true})
	result, err := c.AskMemory(context.Background(), "What's my next clothes reminder?")
	if err != nil || !strings.Contains(result.Answer.Text, "Search by meaning is unavailable") || strings.Contains(result.Answer.Text, "No upcoming reminders are saved") {
		t.Fatalf("unavailable matching claimed no saved reminder: %+v, err=%v", result, err)
	}
}

func TestSearchMergeRetainsSourceAlongsideFullTaskPage(t *testing.T) {
	page := taskSearchPage{}
	for i := 0; i < MaxSearchResults; i++ {
		page.matches = append(page.matches, taskSearchMatch{item: tasks.Item{ID: fmt.Sprintf("%032x", i+1)}, evidence: assistant.Evidence{SourceID: fmt.Sprintf("%064x", i+1), AppID: "blur.tasks", Text: "Drycleaning task"}})
	}
	source := SearchHit{SourceID: strings.Repeat("f", 64), AppID: "com.example.notes", Text: "Exact drycleaning instructions"}
	hits, more := mergeTaskSearchHits([]SearchHit{source}, page, MaxSearchResults)
	if len(hits) != MaxSearchResults || !more {
		t.Fatalf("bounded merge has %d hits, more=%t", len(hits), more)
	}
	for _, hit := range hits {
		if hit.SourceID == source.SourceID && hit.Text == source.Text && hit.Task == nil {
			return
		}
	}
	t.Fatal("task matches displaced the exact saved-source result")
}
