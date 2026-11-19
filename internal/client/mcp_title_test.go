package client

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/localapi"
)

const mcpTestTitle = "Dima, Dziga (DM) - Cresco - Slack"

func titledObservation(t *testing.T, app, text string) capture.SanitizedObservation {
	t.Helper()
	o, err := capture.SanitizeObservation(app, "", text, 4096)
	if err != nil {
		t.Fatal(err)
	}
	return o.WithTitle(mcpTestTitle)
}

func TestMCPSnapshotsExposeWindowTitle(t *testing.T) {
	titled := titledObservation(t, "com.example.editor", "A titled capture with enough text.")
	plain, err := capture.SanitizeObservation("com.example.other", "", "A capture without any window title.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	data := &testData{observations: map[string]capture.SanitizedObservation{titled.ContentID(): titled, plain.ContentID(): plain}}
	wire, err := NewClient(data).HandleMCPRead(context.Background(), localapi.RPCGetSnapshots, json.RawMessage(fmt.Sprintf(`{"source_ids":["%s","%s"]}`, titled.ContentID(), plain.ContentID())))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Snapshots []map[string]any `json:"snapshots"`
	}
	if err := json.Unmarshal(wire, &out); err != nil || len(out.Snapshots) != 2 {
		t.Fatalf("%s %v", wire, err)
	}
	if out.Snapshots[0]["window_title"] != mcpTestTitle {
		t.Fatalf("titled snapshot = %v", out.Snapshots[0])
	}
	if _, present := out.Snapshots[1]["window_title"]; present {
		t.Fatal("title-less capture exposes window_title")
	}
}

func TestMCPWorkingMemoryExposesWindowTitle(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	titled := titledObservation(t, "com.tinyspeck.slackmacgap", "#general\nA message the user was composing.")
	data := &testData{
		observations:  map[string]capture.SanitizedObservation{titled.ContentID(): titled},
		observationAt: map[string]time.Time{titled.ContentID(): now.Add(-time.Hour)},
	}
	client := NewClient(data)
	client.askNow = func() time.Time { return now }
	wire, err := client.HandleMCPRead(context.Background(), localapi.RPCGetWorkingMemory, json.RawMessage(`{"hours":4,"limit":5}`))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(wire, &out); err != nil || len(out.Items) != 1 || out.Items[0]["window_title"] != mcpTestTitle {
		t.Fatalf("working memory = %s %v", wire, err)
	}
}

func TestMCPSessionCaptureExposesWindowTitle(t *testing.T) {
	titled := titledObservation(t, "com.example.editor", "A titled capture with enough text.")
	at := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	data := &testData{
		observations:  map[string]capture.SanitizedObservation{titled.ContentID(): titled},
		observationAt: map[string]time.Time{titled.ContentID(): at},
	}
	item, err := NewClient(data).sessionCapture(context.Background(), mcpSessionEntry{id: titled.ContentID(), seen: at}, 2048)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(item)
	var keys map[string]any
	_ = json.Unmarshal(raw, &keys)
	if keys["window_title"] != mcpTestTitle {
		t.Fatalf("session capture wire = %s", raw)
	}
}
