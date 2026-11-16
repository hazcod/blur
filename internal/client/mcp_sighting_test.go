package client

import (
	"context"
	"encoding/json"
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
