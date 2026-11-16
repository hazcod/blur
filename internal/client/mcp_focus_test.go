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

func focusedObservation(t *testing.T, app, text string) capture.SanitizedObservation {
	t.Helper()
	o, err := capture.SanitizeObservation(app, "", text, 4096)
	if err != nil {
		t.Fatal(err)
	}
	return o.WithFocus("AXTextField", "Subject", "Compose > Header")
}

func TestMCPSnapshotsExposeFocusBreadcrumb(t *testing.T) {
	focused := focusedObservation(t, "com.example.editor", "A focused capture with enough text.")
	plain, err := capture.SanitizeObservation("com.example.other", "", "A capture without any breadcrumb.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	data := &testData{observations: map[string]capture.SanitizedObservation{focused.ContentID(): focused, plain.ContentID(): plain}}
	wire, err := NewClient(data).HandleMCPRead(context.Background(), localapi.RPCGetSnapshots, json.RawMessage(fmt.Sprintf(`{"source_ids":["%s","%s"]}`, focused.ContentID(), plain.ContentID())))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Snapshots []map[string]any `json:"snapshots"`
	}
	if err := json.Unmarshal(wire, &out); err != nil || len(out.Snapshots) != 2 {
		t.Fatalf("%s %v", wire, err)
	}
	if out.Snapshots[0]["focused_role"] != "AXTextField" || out.Snapshots[0]["focused_name"] != "Subject" || out.Snapshots[0]["focused_path"] != "Compose > Header" {
		t.Fatalf("focused snapshot = %v", out.Snapshots[0])
	}
	for _, key := range []string{"focused_role", "focused_name", "focused_path"} {
		if _, present := out.Snapshots[1][key]; present {
			t.Fatalf("breadcrumb-less capture exposes %s", key)
		}
	}
}

func TestMCPWorkingMemoryExposesFocusBreadcrumb(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	focused := focusedObservation(t, "com.tinyspeck.slackmacgap", "#general\nA message the user was composing.")
	data := &testData{
		observations:  map[string]capture.SanitizedObservation{focused.ContentID(): focused},
		observationAt: map[string]time.Time{focused.ContentID(): now.Add(-time.Hour)},
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
	if err := json.Unmarshal(wire, &out); err != nil || len(out.Items) != 1 {
		t.Fatalf("%s %v", wire, err)
	}
	if out.Items[0]["focused_role"] != "AXTextField" || out.Items[0]["focused_name"] != "Subject" || out.Items[0]["focused_path"] != "Compose > Header" {
		t.Fatalf("working memory item = %v", out.Items[0])
	}
}

func TestMCPWorkingMemoryDropsBreadcrumbBeforeBustingBudget(t *testing.T) {
	response := mcpWorkingMemoryResponse{Items: make([]mcpWorkingMemoryItem, 0, 8)}
	for i := 0; i < 8; i++ {
		response.Items = append(response.Items, mcpWorkingMemoryItem{
			SourceID: fmt.Sprintf("%064d", i), AppID: "app", CapturedAt: time.Unix(1, 0).UTC(),
			Content:        string(make([]byte, 0)) + fmt.Sprintf("%*s", maxMCPWorkingExcerpt, "x"),
			mcpFocusFields: mcpFocusFields{FocusedRole: "AXTextField", FocusedName: fmt.Sprintf("%0120d", i), FocusedPath: fmt.Sprintf("%0120d", i)},
		})
	}
	trimWorkingMemoryFocus(&response)
	encoded, _ := json.Marshal(response)
	if len(encoded) > maxMCPReadOutputBytes {
		t.Fatalf("response still %d bytes", len(encoded))
	}
	if response.Items[0].FocusedName == "" {
		t.Fatal("the first item lost its breadcrumb although dropping later ones sufficed")
	}
}

func TestMCPSessionCaptureExposesFocusBreadcrumb(t *testing.T) {
	focused := focusedObservation(t, "com.example.editor", "A focused capture with enough text.")
	plain, err := capture.SanitizeObservation("com.example.other", "", "A capture without any breadcrumb.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	data := &testData{
		observations:  map[string]capture.SanitizedObservation{focused.ContentID(): focused, plain.ContentID(): plain},
		observationAt: map[string]time.Time{focused.ContentID(): at, plain.ContentID(): at},
	}
	client := NewClient(data)
	item, err := client.sessionCapture(context.Background(), mcpSessionEntry{id: focused.ContentID(), seen: at}, 2048)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(item)
	var keys map[string]any
	_ = json.Unmarshal(raw, &keys)
	if keys["focused_role"] != "AXTextField" || keys["focused_name"] != "Subject" || keys["focused_path"] != "Compose > Header" {
		t.Fatalf("session capture wire = %s", raw)
	}
	item, err = client.sessionCapture(context.Background(), mcpSessionEntry{id: plain.ContentID(), seen: at}, 2048)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(item)
	keys = nil
	_ = json.Unmarshal(raw, &keys)
	if _, present := keys["focused_role"]; present {
		t.Fatalf("breadcrumb-less capture exposes focus fields: %s", raw)
	}
}
