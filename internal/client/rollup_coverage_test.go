package client

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/chronicle"
	"github.com/hazcod/blur/internal/localapi"
)

func TestRollupCoverageRemainsVisibleThroughAskSearchAndMCP(t *testing.T) {
	for _, version := range []uint8{0, 1} {
		f := newAskFixture(t)
		row := rollupFixtureRecord(t, "day:2026-11-10", "- Reviewed the launch proposal.", false, rollupFixtureSource(1))
		row.CoverageVersion, row.Sampled = version, version != 0
		f.client.chronicles = rollupChronicles{rollups: []chronicle.Rollup{row}}
		plan := assistant.Plan{Kind: assistant.KindRecap, Scope: assistant.Scope{Start: row.Start, End: row.End}}
		retrieved, err := f.client.RetrievePlan(context.Background(), plan)
		if err != nil || !retrieved.Diagnosis.RecapSampled {
			t.Fatalf("Ask hid sampled/legacy coverage: %+v %v", retrieved, err)
		}
		hits, err := f.client.searchSummaries(context.Background(), assistant.Plan{}, []string{"launch", "proposal"})
		if err != nil || len(hits) == 0 {
			t.Fatalf("Search fixture failed: %+v %v", hits, err)
		}
		wire, err := json.Marshal(hits[0])
		var summary struct {
			Limited bool `json:"coverage_limited"`
		}
		if err != nil || json.Unmarshal(wire, &summary) != nil || !summary.Limited {
			t.Fatalf("Search hid summary coverage: %s %v", wire, err)
		}
		page, err := f.client.SearchMemoryPageContext(context.Background(), "launch proposal")
		if err != nil || len(page.Summaries) == 0 || !page.CoverageLimited {
			t.Fatalf("native Search page hid summary coverage: %+v %v", page, err)
		}
		f.client.chronicles = rollupLookupChronicles{rollups: map[string]chronicle.Rollup{row.ID: row}}
		wire, err = f.client.HandleMCPRead(context.Background(), localapi.RPCGetChronicle, json.RawMessage(`{"level":"day","period":"2026-11-10"}`))
		var response struct {
			Limited bool `json:"coverage_limited"`
		}
		if err != nil || json.Unmarshal(wire, &response) != nil || !response.Limited {
			t.Fatalf("MCP hid summary coverage: %s %v", wire, err)
		}
		f.client.Close()
	}
}
