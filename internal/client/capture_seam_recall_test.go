package client

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/localapi"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

func TestCaptureIdentifierSeamRemainsUsefulAcrossAskSearchMCPAndReopen(t *testing.T) {
	t.Setenv("TZ", "UTC")
	for _, label := range []string{"agent_build_01", "agent-build-01", "agent.build.01"} {
		t.Run(label, func(t *testing.T) { captureIdentifierSeamJourney(t, label) })
	}
}

func captureIdentifierSeamJourney(t *testing.T, label string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	path, key := filepath.Join(t.TempDir(), "seam.db"), securestore.OfflineFixtureKey()
	store, err := durable.OpenDurable(path, key, durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	fact := label + " finished quartz calibration."
	// The physical boundary is after the delimiter. The label's index terms
	// are individually complete, so a cross-word-only repair is insufficient.
	input := strings.Repeat(" ", capture.MaxCapturePartTextBytes-len("agent_")) + fact
	builder, err := capture.NewCaptureBuilder("com.apple.terminal", "terminal.example", strings.Repeat("b", 64), now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BeginCapture(ctx, builder.Header()); err != nil {
		t.Fatal(err)
	}
	appendParts := func(ctx context.Context, parts []capture.CapturePart) error {
		return store.AppendCaptureParts(ctx, builder.Header(), parts)
	}
	if err := builder.Write(ctx, input, appendParts); err != nil {
		t.Fatal(err)
	}
	summary, err := builder.Finish(ctx, capture.CaptureComplete, appendParts)
	if err != nil {
		t.Fatal(err)
	}
	if summary.PartCount() < 2 || summary.Bytes() != int64(len(input)) {
		t.Fatal("fixture did not retain the complete multipart original")
	}
	if _, err := store.PublishCapture(ctx, summary); err != nil {
		t.Fatal(err)
	}
	for {
		more, err := store.IndexPendingCaptures(ctx, 16)
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = durable.OpenDurable(path, key, durable.OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient(store)
	defer c.Close()
	c.askNow = func() time.Time { return now }
	query := `find "` + label + `" in Terminal today`
	page, err := c.SearchMemoryPageContext(ctx, query)
	if err != nil || len(page.Hits) != 1 || page.IndexingPending {
		for _, hit := range page.Hits {
			e, _, _ := store.GetSourceEvidence(ctx, hit.SourceID)
			t.Logf("hit=%+v text=%q", hit, e.Text())
		}
		t.Fatalf("multipart boundary lost the identifier in native Search: hits=%d pending=%t err=%v", len(page.Hits), page.IndexingPending, err)
	}
	id := page.Hits[0].SourceID
	evidence, found, err := store.GetSourceEvidence(ctx, id)
	if err != nil || !found {
		t.Fatal("native Search citation cannot be inspected", found, err)
	}
	span, multipart := evidence.Provenance()
	if !multipart || span.Start < 0 || span.End > int64(len(input)) || evidence.Text() != input[span.Start:span.End] || !strings.Contains(evidence.Text(), fact) {
		t.Fatalf("citation lost useful original context: span=%+v text=%q", span, evidence.Text())
	}
	ask, err := c.AskMemory(ctx, "What did "+label+" finish in Terminal today?")
	// Ask can additionally return explicitly qualified closest passages. The
	// answer must still include this complete original and its exact citation.
	if err != nil || ask.Answer.InsufficientEvidence || !strings.Contains(ask.Answer.Text, fact) || !slices.Contains(ask.Answer.SourceIDs, id) {
		t.Fatalf("Ask could not answer from the cited original: answer=%q citations=%v err=%v", ask.Answer.Text, ask.Answer.SourceIDs, err)
	}
	params, _ := json.Marshal(mcpSearchParams{Query: query, Limit: 8})
	wire, err := c.HandleMCPRead(ctx, localapi.RPCSearchMemory, params)
	if err != nil {
		t.Fatal(err)
	}
	var result mcpSearchResponse
	if err := json.Unmarshal(wire, &result); err != nil || len(result.Results) != 1 || result.Results[0].SourceID != id {
		t.Fatalf("MCP lost the native Search original: wire=%s err=%v", wire, err)
	}
	params, _ = json.Marshal(mcpSnapshotParams{SourceIDs: []string{id}, MaxBytesPerSource: 4096})
	wire, err = c.HandleMCPRead(ctx, localapi.RPCGetSnapshots, params)
	if err != nil {
		t.Fatal(err)
	}
	var snapshots mcpSnapshotResponse
	if err := json.Unmarshal(wire, &snapshots); err != nil || len(snapshots.Snapshots) != 1 || snapshots.Snapshots[0].SourceID != id || !strings.Contains(snapshots.Snapshots[0].Content, fact) || snapshots.Snapshots[0].DocumentID != summary.ContentID() {
		t.Fatalf("MCP cannot inspect the cited original range: wire=%s err=%v", wire, err)
	}
	if err := store.DeleteObservationRecord(ctx, id); err != nil {
		t.Fatal(err)
	}
	page, err = c.SearchMemoryPageContext(ctx, query)
	if err != nil || len(page.Hits) != 0 {
		t.Fatal("erased original range remained in native Search", len(page.Hits), err)
	}
	wire, err = c.HandleMCPRead(ctx, localapi.RPCSearchMemory, paramsForSeamSearch(query))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(wire, &result); err != nil || len(result.Results) != 0 {
		t.Fatal("erased range remained in MCP Search", string(wire), err)
	}
	if _, found, err := store.GetSourceEvidence(ctx, id); err != nil || found {
		t.Fatal("erased range retained native evidence", found, err)
	}
	params, _ = json.Marshal(mcpSnapshotParams{SourceIDs: []string{id}, MaxBytesPerSource: 4096})
	wire, err = c.HandleMCPRead(ctx, localapi.RPCGetSnapshots, params)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(wire, &snapshots); err != nil || len(snapshots.Snapshots) != 0 {
		t.Fatal("erased range retained MCP snapshot text", string(wire), err)
	}
}

func paramsForSeamSearch(query string) json.RawMessage {
	params, _ := json.Marshal(mcpSearchParams{Query: query, Limit: 8})
	return params
}
