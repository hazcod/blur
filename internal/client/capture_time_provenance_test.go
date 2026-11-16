package client

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/localapi"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/semantic"
	"github.com/hazcod/blur/internal/storage/durable"
)

func legacyRecallClient(t *testing.T) (*Client, *durable.Store, time.Time, string) {
	t.Helper()
	ctx := context.Background()
	path, key := filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey()
	s, err := durable.OpenDurable(path, key, durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	baseline := time.Date(2026, 11, 12, 12, 30, 0, 123, time.UTC)
	o, _ := capture.SanitizeObservation("reader.app", "example.com", "Drycleanres Aurora legacy", 512)
	if err := s.PutObservationAt(ctx, o, baseline); err != nil {
		t.Fatal(err)
	}
	// Authenticated state produced by the historical migration, not a user DB.
	marker := make([]byte, 10)
	marker[0], marker[1] = 1, 1
	binary.BigEndian.PutUint64(marker[2:], uint64(baseline.UnixNano()))
	if err := s.Update(ctx, func(tx *durable.Tx) error {
		return tx.PutIndexRecord("storage-meta", "capture-time-baseline-v1", marker)
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = durable.OpenDurable(path, key, durable.OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient(s)
	c.askNow = func() time.Time { return baseline.AddDate(0, 0, 1) }
	t.Cleanup(func() { c.Close(); _ = s.Close() })
	return c, s, baseline, o.ContentID()
}

func TestLegacyCaptureTimeRemainsUnknownInSearchMCPAndModelContext(t *testing.T) {
	c, _, baseline, id := legacyRecallClient(t)
	ctx := context.Background()
	page, err := c.SearchMemoryPageContext(ctx, "Aurora")
	if err != nil || len(page.Hits) != 1 || page.Hits[0].SourceID != id {
		t.Fatalf("legacy memory disappeared: %+v %v", page, err)
	}
	encoded, _ := json.Marshal(page.Hits[0])
	var hit map[string]any
	_ = json.Unmarshal(encoded, &hit)
	if hit["capture_time_unknown"] != true || hit["captured_at"] != nil {
		t.Fatalf("Search hid capture-time uncertainty: %s", encoded)
	}
	wire, err := c.HandleMCPRead(ctx, localapi.RPCSearchMemory, json.RawMessage(`{"query":"Aurora","limit":5}`))
	var output struct {
		Results []map[string]any `json:"results"`
	}
	if err != nil || json.Unmarshal(wire, &output) != nil || len(output.Results) != 1 || output.Results[0]["capture_time_unknown"] != true || output.Results[0]["captured_at"] != nil {
		t.Fatalf("MCP hid capture-time uncertainty: %s %v", wire, err)
	}
	result, err := c.RetrievePlan(ctx, assistant.Plan{Kind: assistant.KindLookup, Question: "What do you remember about Aurora?", Terms: []string{"aurora"}})
	if err != nil || len(result.Evidence) != 1 {
		t.Fatalf("unscoped Ask lost legacy content: %+v %v", result, err)
	}
	packed, err := assistant.PackContext("What do you remember about Aurora?", result.Evidence)
	if err != nil || !strings.Contains(packed.User, `"capture_time_unknown":true`) || strings.Contains(packed.User, baseline.Format("2006-01-02")) {
		t.Fatalf("model received a guessed date: %s %v", packed.User, err)
	}
}

func TestLegacyTimeFilteringAlsoAppliesToMeaningSearchAndLaterVisits(t *testing.T) {
	c, s, baseline, id := legacyRecallClient(t)
	ctx := context.Background()
	withTaskSemanticFixture(t, c, s, &taskRecallEmbedder{score: .99})
	query := semantic.Query{Limit: 8, Start: baseline.Truncate(time.Hour), End: baseline.Truncate(time.Hour).Add(time.Hour)}
	hits, err := c.memoryIndex.service.Search(ctx, "laundry", query)
	if err != nil || len(hits) != 0 {
		t.Fatalf("meaning search admitted guessed date: %+v %v", hits, err)
	}
	hits, err = c.memoryIndex.service.Search(ctx, "laundry", semantic.Query{Limit: 8})
	if err != nil || len(hits) != 1 || !hits[0].CapturedAt.IsZero() {
		t.Fatalf("unscoped meaning result claimed upgrade date: %+v %v", hits, err)
	}
	o, _, _ := s.GetObservation(ctx, id)
	visit := baseline.AddDate(0, 0, 1).Add(-time.Hour)
	if err := s.RecordObservationSighting(ctx, o, visit); err != nil {
		t.Fatal(err)
	}
	page, err := c.SearchMemoryPageContext(ctx, "Aurora today")
	if err != nil || len(page.Hits) != 1 || !page.Hits[0].CapturedAt.Equal(visit) {
		t.Fatalf("known later visit lost its time: %+v %v", page, err)
	}
	encoded, _ := json.Marshal(page.Hits[0])
	if strings.Contains(string(encoded), `"capture_time_unknown":true`) {
		t.Fatalf("known visit still presented as unknown: %s", encoded)
	}
}

func TestAuthenticatedActivityStillDatesLegacyContentWithUnknownFirstCapture(t *testing.T) {
	c, s, baseline, id := legacyRecallClient(t)
	ctx := context.Background()
	observed := baseline.AddDate(0, 0, -1)
	if err := s.PutActivitySegment(ctx, activity.Segment{ID: "legacy-authenticated-activity", AppID: "reader.app", Domain: "example.com", SourceIDs: []string{id}, WallStart: observed, WallEnd: observed.Add(time.Minute), Duration: int64(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []assistant.Kind{assistant.KindLookup, assistant.KindRecap} {
		result, err := c.RetrievePlan(ctx, assistant.Plan{Kind: kind, MatchOnly: true, Question: "Aurora work in that period", Terms: []string{"aurora"}, Scope: assistant.Scope{Start: observed.Add(-time.Hour), End: observed.Add(time.Hour), Explicit: true}})
		if err != nil || len(result.Evidence) != 1 || result.Evidence[0].SourceID != id || !result.Evidence[0].CapturedAt.Equal(observed) || result.Evidence[0].TimeUnknown {
			t.Fatalf("authenticated activity was lost with unknown original date: %+v %v", result, err)
		}
	}
}
