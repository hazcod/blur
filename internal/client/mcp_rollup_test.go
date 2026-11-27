package client

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/chronicle"
	"github.com/hazcod/blur/internal/localapi"
)

type rollupLookupChronicles struct {
	testChronicles
	rollups map[string]chronicle.Rollup
}

func (r rollupLookupChronicles) GetChronicleRollup(_ context.Context, id string) (chronicle.Rollup, bool, error) {
	row, ok := r.rollups[id]
	return row, ok, nil
}

func TestHandleMCPReadReturnsDayAndWeekRecapsWithProvenance(t *testing.T) {
	sources := make([]string, 70)
	for i := range sources {
		sources[i] = rollupFixtureSource(i)
	}
	day := rollupFixtureRecord(t, "day:2026-11-10", "- Reviewed the proposal.", true, sources...)
	day.ChildIDs = []string{"hour:20261110T09Z", "hour:20261110T10Z"}
	week := rollupFixtureRecord(t, "week:2026-W46", "- A planning week.", false, sources[:2]...)
	client := NewClient(&testData{})
	client.chronicles = rollupLookupChronicles{rollups: map[string]chronicle.Rollup{day.ID: day, week.ID: week}}
	type response struct {
		Found              bool      `json:"found"`
		Level              string    `json:"level"`
		Period             string    `json:"period"`
		Text               string    `json:"text"`
		ChildIDs           []string  `json:"child_ids"`
		SourceIDs          []string  `json:"source_ids"`
		SourceIDsTruncated bool      `json:"source_ids_truncated"`
		GeneratedAt        time.Time `json:"generated_at"`
		Dirty              bool      `json:"dirty"`
		HourID             string    `json:"hour_id"`
	}
	read := func(raw string) (response, error) {
		wire, err := client.HandleMCPRead(context.Background(), localapi.RPCGetChronicle, json.RawMessage(raw))
		var got response
		if err == nil {
			if jsonErr := json.Unmarshal(wire, &got); jsonErr != nil {
				t.Fatal(jsonErr)
			}
		}
		return got, err
	}
	got, err := read(`{"level":"day","period":"2026-11-10"}`)
	if err != nil || !got.Found || got.Level != "day" || got.Period != "2026-11-10" || got.Text != day.Text || !got.Dirty || len(got.ChildIDs) != 2 || got.GeneratedAt.IsZero() {
		t.Fatalf("day response = %+v err=%v", got, err)
	}
	if len(got.SourceIDs) != 64 || !got.SourceIDsTruncated {
		t.Fatalf("source IDs not bounded to 64 with a truncation flag: %d truncated=%t", len(got.SourceIDs), got.SourceIDsTruncated)
	}
	got, err = read(`{"level":"week","period":"2026-W46"}`)
	if err != nil || !got.Found || got.Level != "week" || got.Dirty || len(got.SourceIDs) != 2 || got.SourceIDsTruncated {
		t.Fatalf("week response = %+v err=%v", got, err)
	}
	// An unknown period is a clean miss, not an error.
	got, err = read(`{"level":"day","period":"2026-11-09"}`)
	if err != nil || got.Found || got.Text != "" {
		t.Fatalf("missing day = %+v err=%v", got, err)
	}
	// A rollup whose text was discarded after a source erasure is not served.
	cleared := rollupFixtureRecord(t, "day:2026-11-08", "", true)
	client.chronicles = rollupLookupChronicles{rollups: map[string]chronicle.Rollup{cleared.ID: cleared}}
	got, err = read(`{"level":"day","period":"2026-11-08"}`)
	if err != nil || got.Found || got.Text != "" {
		t.Fatalf("a placeholder was served as a recap: %+v err=%v", got, err)
	}
	// The default level still reads an hourly recap.
	client.chronicles = testChronicles{"hour:20261110T09Z": {HourID: "hour:20261110T09Z", Start: time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC), End: time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC), Text: "Hourly.", SourceIDs: []string{sources[0]}}}
	got, err = read(`{"level":"hour","hour_id":"hour:20261110T09Z"}`)
	if err != nil || !got.Found || got.HourID != "hour:20261110T09Z" || got.Level != "" {
		t.Fatalf("explicit hour level = %+v err=%v", got, err)
	}
}

func TestHandleMCPReadRejectsMalformedRollupLevelAndPeriod(t *testing.T) {
	client := NewClient(&testData{})
	client.chronicles = rollupLookupChronicles{}
	for _, raw := range []string{
		`{"level":"quarter","period":"2026-11"}`,
		`{"level":"month","period":"2026-13"}`,
		`{"level":"month","period":"2026-1"}`,
		`{"level":"month","period":"2026-11-10"}`,
		`{"level":"year","period":"2026-11"}`,
		`{"level":"year","period":"0000"}`,
		`{"level":"day"}`,
		`{"level":"day","period":"2026-13-01"}`,
		`{"level":"day","period":"2026-02-30"}`,
		`{"level":"day","period":"2026-W46"}`,
		`{"level":"day","period":"2026-11-10","hour_id":"hour:20261110T09Z"}`,
		`{"level":"week","period":"2026-11-10"}`,
		`{"level":"week","period":"2026-W54"}`,
		`{"level":"week","period":"2027-W53"}`,
		`{"level":"week","period":"2026-W5"}`,
		`{"level":"hour","hour_id":"hour:20261110T09Z","period":"2026-11-10"}`,
		`{"level":"hour"}`,
		`{"period":"2026-11-10"}`,
		`{"level":"day","period":"2026-11-10","extra":1}`,
		`{"level":"DAY","period":"2026-11-10"}`,
		`{"level":"day","period":"` + strings.Repeat("1", 40) + `"}`,
	} {
		if _, err := client.HandleMCPRead(context.Background(), localapi.RPCGetChronicle, json.RawMessage(raw)); err == nil {
			t.Errorf("accepted chronicle request %s", raw)
		}
	}
}

func TestHandleMCPReadRejectsRollupWithoutSources(t *testing.T) {
	row := rollupFixtureRecord(t, "day:2026-11-10", "- Reviewed the proposal.", false, rollupFixtureSource(1))
	row.SourceIDs = nil // derived text without provenance must never be returned
	client := NewClient(&testData{})
	client.chronicles = rollupLookupChronicles{rollups: map[string]chronicle.Rollup{row.ID: row}}
	if _, err := client.HandleMCPRead(context.Background(), localapi.RPCGetChronicle, json.RawMessage(`{"level":"day","period":"2026-11-10"}`)); err == nil {
		t.Fatal("a recap without source IDs was returned")
	}
}
