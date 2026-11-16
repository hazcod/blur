package client

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/localapi"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

func TestRevisitedPageReachesTodayAskRecapAndMCPBeforeActivityCloses(t *testing.T) {
	ctx := context.Background()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(store.Close)
	o, _ := capture.SanitizeObservation("editor.app", "example.com", "Mira owns the rollback for Aurora.", 512)
	first := time.Date(2026, 11, 9, 9, 0, 0, 0, time.Local)
	visit := first.AddDate(0, 0, 2)
	if err := store.PutObservationAt(ctx, o, first); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordObservationSighting(ctx, o, visit); err != nil {
		t.Fatal(err)
	}
	c := NewClient(store)
	defer c.Close()
	c.askNow = func() time.Time { return visit.Add(time.Hour) }
	view, err := c.Today(visit.Format("2006-01-02"))
	if err != nil || len(view.RecentCaptures) != 1 || view.RecentCaptures[0].SourceID != o.ContentID() || !view.RecentCaptures[0].CapturedAt.Equal(visit) {
		t.Fatalf("Today missed current visit: %+v %v", view, err)
	}
	for _, kind := range []assistant.Kind{assistant.KindLookup, assistant.KindRecap} {
		plan := assistant.Plan{Kind: kind, Question: "Who owns the Aurora rollback today?", Terms: []string{"mira"}, Scope: assistant.Scope{Start: visit.Add(-time.Hour), End: visit.Add(time.Hour), Explicit: true}}
		got, err := c.RetrievePlan(ctx, plan)
		if err != nil || len(got.Evidence) != 1 || got.Evidence[0].SourceID != o.ContentID() || !got.Evidence[0].CapturedAt.Equal(visit) {
			t.Fatalf("%v missed source visit: %+v %v", kind, got, err)
		}
	}
	wire, err := c.HandleMCPRead(ctx, localapi.RPCSearchMemory, json.RawMessage(`{"query":"Mira","limit":5}`))
	if err != nil {
		t.Fatal(err)
	}
	var response mcpSearchResponse
	if json.Unmarshal(wire, &response) != nil || len(response.Results) != 1 || response.Results[0].SourceID != o.ContentID() {
		t.Fatalf("MCP lost shared original source: %s", wire)
	}
}
