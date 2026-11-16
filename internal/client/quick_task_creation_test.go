package client

import (
	"context"
	"strings"
	"testing"

	"github.com/hazcod/blur/internal/intent"
)

func TestQuickTaskCreationPhrasings(t *testing.T) {
	for input, want := range map[string]string{
		"add a task: follow up with Chloe": "follow up with Chloe",
		"add task: call the bank":          "call the bank",
		"Add a task call the bank":         "call the bank",
		"new task: renew passport":         "renew passport",
		"new task renew passport":          "renew passport",
		"to-do: water plants":              "water plants",
		"todo: water plants":               "water plants",
		"task: water plants":               "water plants",
		"add buy milk to my todo list":     "buy milk",
		"add buy milk to my to-do list":    "buy milk",
		"add buy milk to my task list":     "buy milk",
		"  add a task:   spaced out   ":    "spaced out",
	} {
		kind, body := classifyQuickInput(input)
		if kind != quickInputTask || body != want {
			t.Errorf("%q => %v %q, want task %q", input, kind, body, want)
		}
	}
}

func TestQuickTaskCreationNegatives(t *testing.T) {
	for _, input := range []string{
		"add a task manager app?",
		"add a task:",
		"add a task",
		"add task list",
		"add a task list for the team",
		"add to my todo list",
		"what is on my todo list",
		"remind me tomorrow at 9 to add a task",
	} {
		if kind, body := classifyQuickInput(input); kind == quickInputTask {
			t.Errorf("%q wrongly classified as task %q", input, body)
		}
	}
}

func TestQuickTaskCreationBoundsTitleOnRuneBoundary(t *testing.T) {
	kind, body := classifyQuickInput("add a task: " + strings.Repeat("é", 400))
	if kind != quickInputTask || len(body) > 512 || strings.ContainsRune(body, '�') || len(body)%2 != 0 {
		t.Fatalf("kind=%v len=%d", kind, len(body))
	}
}

func TestTaskCreationReceiptNamesTheCreatedTask(t *testing.T) {
	corpus := newScenarioCorpus(t)
	ctx := context.Background()
	resolved, err := corpus.client.ResolveEntry(ctx, "add task: renew the Brussels parking permit", EntryState{}, "Europe/Brussels")
	if err != nil || resolved.Action.Kind != intent.Task || resolved.Action.Text != "renew the Brussels parking permit" {
		t.Fatalf("not resolved as a task: %+v %v", resolved.Action, err)
	}
	result, err := corpus.client.CreateTask(ctx, resolved.Action.Text, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The quick panel shows this exact composition as the confirmation.
	receipt := taskResultCopy(result) + " " + result.Item.Title
	if !strings.Contains(receipt, "Task saved.") || !strings.Contains(receipt, "renew the Brussels parking permit") {
		t.Fatalf("confirmation does not name the task: %q", receipt)
	}
}
