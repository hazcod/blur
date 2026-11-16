package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/localapi"
)

type sightingFields struct {
	SourceID      string `json:"source_id"`
	SightingCount *int64 `json:"sighting_count"`
	LastSeenAt    string `json:"last_seen_at"`
}

func sightingRead(t *testing.T, corpus *scenarioCorpus, method, params, key string) []sightingFields {
	t.Helper()
	raw, err := corpus.client.HandleMCPRead(context.Background(), method, json.RawMessage(params))
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	var items []sightingFields
	if err := json.Unmarshal(envelope[key], &items); err != nil {
		t.Fatal(err)
	}
	return items
}

func TestMCPReadsReturnSightingCountAndLastSeenForEachCapture(t *testing.T) {
	corpus := newScenarioCorpus(t)
	app := "com.example.sightings"
	first := corpus.now.Add(-50 * time.Minute)
	repeated := sessionTestCapture(t, corpus, app, "repeated capture text that is longer than the minimum", first)
	single := sessionTestCapture(t, corpus, app, "single capture text that is longer than the minimum", first.Add(time.Minute))
	last := first.Add(40 * time.Minute)
	for _, at := range []time.Time{first.Add(5 * time.Minute), first.Add(70 * time.Second), last} {
		if err := corpus.store.RecordObservationSighting(context.Background(), repeated, at.UTC()); err != nil {
			t.Fatal(err)
		}
	}
	check := func(name string, items []sightingFields) {
		t.Helper()
		got := map[string]sightingFields{}
		for _, item := range items {
			got[item.SourceID] = item
		}
		r, s := got[repeated.ContentID()], got[single.ContentID()]
		if r.SightingCount == nil || *r.SightingCount != 4 {
			t.Fatalf("%s: repeated sighting_count = %v", name, r.SightingCount)
		}
		at, err := time.Parse(time.RFC3339Nano, r.LastSeenAt)
		if err != nil || !at.Equal(last.UTC()) {
			t.Fatalf("%s: repeated last_seen_at = %q (%v), want %s", name, r.LastSeenAt, err, last.UTC())
		}
		if s.SightingCount == nil || *s.SightingCount != 1 {
			t.Fatalf("%s: single sighting_count = %v", name, s.SightingCount)
		}
		if s.LastSeenAt == "" && name != "read_session" {
			t.Fatalf("%s: single last_seen_at missing", name)
		}
	}
	check("get_snapshots", sightingRead(t, corpus, localapi.RPCGetSnapshots,
		fmt.Sprintf(`{"source_ids":[%q,%q]}`, repeated.ContentID(), single.ContentID()), "snapshots"))
	check("get_working_memory", sightingRead(t, corpus, localapi.RPCGetWorkingMemory, `{"hours":2,"limit":8}`, "items"))
	check("read_session", sightingRead(t, corpus, localapi.RPCReadSession,
		fmt.Sprintf(`{"app":%q,"from":%d,"to":%d}`, app, first.Add(-time.Hour).Unix(), corpus.now.Add(time.Hour).Unix()), "captures"))
}

// The durable layer's legacy derivation (no count record, 3 chain sightings)
// is covered in storage/durable; this checks the MCP surface of a source with
// 3 sightings reports 3 and the newest time, then 4 after one more.
func TestMCPReadsReportThreeThenFourSightings(t *testing.T) {
	corpus := newScenarioCorpus(t)
	app := "com.example.chainsightings"
	first := corpus.now.Add(-90 * time.Minute)
	o := sessionTestCapture(t, corpus, app, "chain capture text that is longer than the minimum", first)
	ctx := context.Background()
	newest := first.Add(60 * time.Minute)
	for _, at := range []time.Time{first.Add(10 * time.Minute), newest} {
		if err := corpus.store.RecordObservationSighting(ctx, o, at.UTC()); err != nil {
			t.Fatal(err)
		}
	}
	ids := fmt.Sprintf(`{"source_ids":[%q]}`, o.ContentID())
	expect := func(want int64, last time.Time) {
		t.Helper()
		for name, items := range map[string][]sightingFields{
			"get_snapshots":      sightingRead(t, corpus, localapi.RPCGetSnapshots, ids, "snapshots"),
			"get_working_memory": sightingRead(t, corpus, localapi.RPCGetWorkingMemory, `{"hours":3,"limit":8}`, "items"),
		} {
			var mine []sightingFields
			for _, item := range items {
				if item.SourceID == o.ContentID() {
					mine = append(mine, item)
				}
			}
			if len(mine) != 1 {
				t.Fatalf("%s: %d matching items", name, len(mine))
			}
			items = mine
			at, err := time.Parse(time.RFC3339Nano, items[0].LastSeenAt)
			if items[0].SightingCount == nil || *items[0].SightingCount != want || err != nil || !at.Equal(last.UTC()) {
				t.Fatalf("%s: %+v want %d %s", name, items[0], want, last)
			}
		}
	}
	expect(3, newest)
	later := first.Add(80 * time.Minute)
	if err := corpus.store.RecordObservationSighting(ctx, o, later.UTC()); err != nil {
		t.Fatal(err)
	}
	expect(4, later)
}

type failingSightingData struct{ Data }

func (failingSightingData) ObservationSightingStats(context.Context, string) (int64, time.Time, bool, error) {
	return 0, time.Time{}, false, errors.New("corrupt")
}

func TestMCPSightingUnreadableMetadataOmitsFieldsKeepsCapture(t *testing.T) {
	c := &Client{data: failingSightingData{}}
	count, last, err := c.mcpSighting(context.Background(), "id", time.Now())
	if err != nil || count != 0 || last != "" {
		t.Fatalf("got %d %q %v", count, last, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := c.mcpSighting(ctx, "id", time.Now()); err == nil {
		t.Fatal("cancellation must still propagate")
	}
}
