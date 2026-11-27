package client

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/intent"
	"github.com/hazcod/blur/internal/tasks"
)

func TestDutchNativeRecallKeepsEventDatesCaptureProvenanceAndFollowups(t *testing.T) {
	c, s, _ := todayTaskClient(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 21, 15, 0, 0, 0, time.UTC)
	observed := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	c.askNow = func() time.Time { return now }
	var ids []string
	for _, body := range []string{
		"Bevestiging afspraak fysiotherapie: maandag 23 november 2026 om 14:30. Er is geen eindtijd opgegeven.",
		"Bevestiging afspraak fysiotherapie: dinsdag 24 november 2026 om 09:45. Er is geen eindtijd opgegeven.",
	} {
		o, err := capture.SanitizeObservation("com.apple.mail", "", body, capture.MaxObservationTextBytes)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.PutObservationAt(ctx, o, observed); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, o.ContentID())
	}
	for {
		more, err := s.IndexPendingExact(ctx, 32)
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
	}
	var history []assistant.Turn
	for _, tc := range []struct {
		question, clock string
		source          int
	}{
		{"Hoe laat is mijn afspraak fysiotherapie op maandag?", "14:30", 0},
		{"En in Mail?", "14:30", 0},
		{"En op dinsdag?", "09:45", 1},
	} {
		result, err := c.AskMemoryWithHistory(ctx, tc.question, history)
		if err != nil || result.Answer.InsufficientEvidence || len(result.Sources) == 0 || result.Sources[0].SourceID != ids[tc.source] || !slices.Contains(result.Answer.SourceIDs, ids[tc.source]) || !result.Sources[0].CapturedAt.Equal(observed) || !result.Plan.Scope.IsZero() || !strings.Contains(result.Answer.Text, tc.clock) || !strings.Contains(result.Answer.Text, "geen eindtijd") {
			t.Fatalf("Dutch native event recall lost its actual source/time: %q %+v %v", tc.question, result, err)
		}
		history = append(history, assistant.Turn{Question: tc.question, Answer: result.Answer.Text, Plan: result.Plan, SourceIDs: result.Answer.SourceIDs})
	}
	for _, tc := range []struct {
		question string
		found    bool
	}{
		{"Wat heb ik donderdag gelezen over fysiotherapie in Mail?", true},
		{"Wat heb ik maandag gelezen over fysiotherapie in Mail?", false},
	} {
		result, err := c.AskMemory(ctx, tc.question)
		if err != nil || result.Answer.InsufficientEvidence == tc.found || (len(result.Answer.SourceIDs) > 0) != tc.found {
			t.Fatalf("Dutch observation query used the event date: %q %+v %v", tc.question, result, err)
		}
	}
	for _, id := range ids {
		if err := s.DeleteObservationRecord(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	for _, question := range []string{"Zoek mijn afspraak fysiotherapie", "En op maandag?", "Make it shorter"} {
		result, err := c.AskMemoryWithHistory(ctx, question, history)
		if err != nil || !result.Answer.InsufficientEvidence || len(result.Answer.SourceIDs) != 0 || len(result.Sources) != 0 {
			t.Fatalf("Dutch recall revived erased confirmation: %q %+v %v", question, result, err)
		}
		if strings.Contains(result.Answer.Text, "14:30") || strings.Contains(result.Answer.Text, "09:45") {
			t.Fatal("Dutch deleted answer prose survived grounded continuation", result)
		}
	}
}

func TestDutchNativeTaskReadsAreLocalAndDoNotMutateRecords(t *testing.T) {
	c, _, _ := todayTaskClient(t)
	ctx := context.Background()
	one, err := c.CreateTask(ctx, "Controleer de factuur", nil)
	if err != nil {
		t.Fatal(err)
	}
	two, err := c.CreateTask(ctx, "Lees de bevestiging", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, question := range []string{"Welke taken staan nog open?", "Wat moet ik nog doen?", "Hoeveel taken heb ik nog?"} {
		result, err := c.ResolveEntry(ctx, question, EntryState{}, "UTC")
		if err != nil || result.Action.Kind != intent.TaskInspect || result.Answer == nil || !result.Answer.Direct || !strings.Contains(result.Answer.Answer.Text, "2 open task") {
			t.Fatalf("Dutch task question did not use local records: %q %+v %v", question, result, err)
		}
	}
	page, err := c.TaskPage(ctx, "", "")
	if err != nil || len(page.Items) != 2 {
		t.Fatal("Dutch reads changed the task collection", page, err)
	}
	for _, want := range []tasks.Item{one.Item, two.Item} {
		if !slices.ContainsFunc(page.Items, func(got tasks.Item) bool {
			return got.ID == want.ID && got.Revision == want.Revision && got.State == want.State && got.Title == want.Title && got.Reminder == nil
		}) {
			t.Fatal("Dutch read mutated a saved task", page, want)
		}
	}
}

func TestDutchEntryReadCommandsCannotBecomeSavedNotes(t *testing.T) {
	c, _, _ := todayTaskClient(t)
	for _, input := range []string{
		"Zoek mijn factuur 23 in Mail", "Vind mijn café notities van gisteren", "Hoe laat is mijn afspraak fysiotherapie op maandag",
		"En op dinsdag", "Woensdag", "Zoek op het internet naar fysiotherapie", "Zoek een manier om sneller te typen",
	} {
		result, err := c.ResolveEntry(context.Background(), input, EntryState{}, "UTC")
		if err != nil || result.Action.Kind != intent.Ask || result.Action.Text != input || result.Draft != nil || result.Target != nil {
			t.Errorf("Dutch read entered a write path: %q %+v %v", input, result, err)
		}
	}
	for _, tc := range []struct {
		input string
		kind  intent.Kind
	}{
		{"note: Zoek mijn factuur 23 in Mail", intent.Note},
		{"task: Zoek mijn factuur 23 in Mail", intent.Task},
	} {
		result, err := c.ResolveEntry(context.Background(), tc.input, EntryState{}, "UTC")
		if err != nil || result.Action.Kind != tc.kind {
			t.Errorf("explicit save authority changed: %q %+v %v", tc.input, result, err)
		}
	}
}
