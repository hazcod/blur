package client

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/deletion"
	"github.com/hazcod/blur/internal/localapi"
	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/semantic"
	"github.com/hazcod/blur/internal/storage/durable"
)

func TestHybridFusionKeepsExactCodeMatchesAndDeduplicatesSemanticSources(t *testing.T) {
	first, second := strings.Repeat("a", 64), strings.Repeat("b", 64)
	exact := search.ExactPage{Hits: []search.IndexHit{{SourceID: first, Bytes: 20, Score: 4}}}
	semanticHits := []semantic.Hit{{SourceID: second, SourceBytes: 25, Score: .8, Start: 0, End: 20}, {SourceID: first, SourceBytes: 20, Score: .7, Start: 2, End: 20}}
	page := fuseSearchPages(exact, semanticHits, 2)
	if len(page.Hits) != 2 || page.Hits[0].SourceID != first || page.Hits[0].MatchStart != 2 || page.Bytes != 45 {
		t.Fatalf("fusion=%+v", page)
	}
	page = fuseSearchPages(exact, []semantic.Hit{{SourceID: second, SourceBytes: 25, Score: .1, Start: 0, End: 20}}, 2)
	if len(page.Hits) != 1 || page.Hits[0].SourceID != first {
		t.Fatalf("irrelevant semantic hit displaced exact code: %+v", page)
	}
}

func TestMemorySearchSettingsRejectRemoteAndUnsafeFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory-index.json")
	settings, err := readMemoryIndexSettings(path)
	if err != nil || !settings.Enabled || settings.Model != semantic.DefaultEmbeddingModel {
		t.Fatalf("defaults=%+v err=%v", settings, err)
	}
	settings.Enabled = true
	if err := writeMemoryIndexSettings(path, settings); err != nil {
		t.Fatal(err)
	}
	got, err := readMemoryIndexSettings(path)
	if err != nil || got != settings {
		t.Fatalf("saved=%+v err=%v", got, err)
	}
	settings.Endpoint = "https://remote.example"
	if err := writeMemoryIndexSettings(path, settings); err == nil {
		t.Fatal("remote embedding endpoint accepted")
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readMemoryIndexSettings(path); err == nil {
		t.Fatal("publicly readable search settings accepted")
	}
}

func TestMemorySearchUsesLocalMeaningByDefaultAndHonorsSavedDisable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory-index.json")
	settings, err := readMemoryIndexSettings(path)
	if err != nil || !settings.Enabled || settings.Endpoint != "http://127.0.0.1:11434" || settings.Model != semantic.DefaultEmbeddingModel {
		t.Fatalf("new memory silently limited to exact words: %+v err=%v", settings, err)
	}
	settings.Enabled = false
	if err := writeMemoryIndexSettings(path, settings); err != nil {
		t.Fatal(err)
	}
	restored, err := readMemoryIndexSettings(path)
	if err != nil || restored.Enabled || restored != settings {
		t.Fatalf("explicit choice to pause indexing lost: %+v err=%v", restored, err)
	}
}

func TestMemorySearchCopyExplainsCoverageAndIndependentModel(t *testing.T) {
	if got := memoryIndexStatusCopy(semantic.Status{State: "ready", IndexedSources: 42, Complete: true}); !strings.Contains(got, "42 sources indexed") {
		t.Fatalf("coverage omitted: %s", got)
	}
	if got := memoryIndexStatusCopy(semantic.Status{State: "unavailable", ErrorCode: "embedding_model_required"}); !strings.Contains(got, "embeddinggemma") {
		t.Fatalf("repair unclear: %s", got)
	}
	if got := quickNotePrivacyHint(false, ProviderOllama, true); !strings.Contains(got, "local Ollama") || strings.Contains(got, "AI is off") {
		t.Fatalf("independent search processing hidden: %s", got)
	}
}

// This opt-in acceptance check sends only synthetic sources to a real local
// embedding model. It covers the encrypted store, background worker, hybrid
// Search/Ask evidence, restart, note indexing, and source deletion together.
func TestLocalSemanticSearchThroughEncryptedClient(t *testing.T) {
	if os.Getenv("BLUR_EMBEDDING_INTEGRATION") != "1" {
		t.Skip("requires local embeddinggemma")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	directory := t.TempDir()
	path := filepath.Join(directory, "memory.db")
	store, err := durable.OpenDurable(path, securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	at := time.Now().UTC()
	var reservation string
	for i, text := range []string{"A reservation for two at Le Bernardin on Friday evening.", "The Go compiler rejects unused variables.", "We compared encrypted database deletion strategies."} {
		observation, err := capture.SanitizeObservation("reader.app", "research.example", text, 4096)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.PutObservationAt(ctx, observation, at); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			reservation = observation.ContentID()
		}
	}
	openClient := func() *Client {
		deleter, err := deletion.NewService(store, nil)
		if err != nil {
			t.Fatal(err)
		}
		c := NewClient(store).WithDeletion(deleter).WithNotes(notes.NewService(store, nil, nil))
		c.memoryIndex = newMemoryIndexController(filepath.Join(directory, "memory-index.json"), store, store, semantic.Options{CanRead: func() bool { return !c.MemoryIsChanging() }, OnChange: c.notifyMemoryIndexChanged})
		c.addMemoryChangingHook(c.memoryIndex.Invalidate)
		t.Cleanup(c.memoryIndex.Close)
		return c
	}
	c := openClient()
	defer func() { c.memoryIndex.Close() }()
	settings := c.MemoryIndexSettings()
	settings.Enabled = true
	if err := c.ConfigureMemoryIndex(ctx, settings); err != nil {
		t.Fatal(err)
	}
	waitMemoryIndex(ctx, t, c, 3)
	hits, err := c.SearchMemoryContext(ctx, "Which restaurant did I book for dinner?")
	if err != nil || len(hits) == 0 || hits[0].SourceID != reservation {
		t.Fatalf("hybrid recall=%+v err=%v", hits, err)
	}
	plan := assistant.Plan{Kind: assistant.KindLookup, Question: "Which restaurant did I book for dinner?", Terms: []string{"dinner", "restaurant"}, AppIDs: []string{"reader.app"}, Scope: assistant.Scope{Start: at.Add(-time.Minute), End: at.Add(time.Minute)}}
	evidence, err := c.RetrievePlan(ctx, plan)
	if err != nil || len(evidence.Evidence) == 0 || evidence.Evidence[0].SourceID != reservation || evidence.Evidence[0].MatchEnd == 0 {
		t.Fatalf("scoped Ask evidence=%+v err=%v", evidence, err)
	}
	packed, err := assistant.PackContext(plan.Question, evidence.Evidence)
	if err != nil || !strings.Contains(packed.User, "Le Bernardin") {
		t.Fatalf("citable passage not sent to context: %+v err=%v", packed, err)
	}
	mcp, err := c.HandleMCPRead(ctx, localapi.RPCSearchMemory, json.RawMessage(`{"query":"Which restaurant did I book for dinner?","limit":3}`))
	var mcpResponse mcpSearchResponse
	if err != nil || json.Unmarshal(mcp, &mcpResponse) != nil || len(mcpResponse.Results) == 0 || mcpResponse.Results[0].SourceID != reservation {
		t.Fatalf("semantic MCP recall=%s err=%v", mcp, err)
	}
	outside := plan
	outside.AppIDs = []string{"different.app"}
	if got, err := c.RetrievePlan(ctx, outside); err != nil || len(got.Evidence) != 0 {
		t.Fatalf("semantic app scope leaked: %+v err=%v", got, err)
	}
	outside = plan
	outside.Scope = assistant.Scope{Start: at.AddDate(0, 0, -2), End: at.AddDate(0, 0, -1)}
	if got, err := c.RetrievePlan(ctx, outside); err != nil || len(got.Evidence) != 0 {
		t.Fatalf("semantic time scope leaked: %+v err=%v", got, err)
	}
	note, err := c.AddNote("My preferred editor is Neovim with Lua configuration.")
	if err != nil || note.SourceID == "" {
		t.Fatalf("add note=%+v err=%v", note, err)
	}
	waitMemoryIndex(ctx, t, c, 4)
	c.memoryIndex.Close()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = durable.OpenDurable(path, securestore.OfflineFixtureKey(), durable.OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	c = openClient()
	waitMemoryIndex(ctx, t, c, 4)
	hits, err = c.SearchMemoryContext(ctx, "Waar wilde ik vrijdag gaan eten?")
	if err != nil || len(hits) == 0 || hits[0].SourceID != reservation {
		t.Fatalf("restart multilingual recall=%+v err=%v", hits, err)
	}
	if _, err := c.DeleteSource(reservation); err != nil {
		t.Fatal(err)
	}
	waitMemoryIndex(ctx, t, c, 3)
	hits, err = c.SearchMemoryContext(ctx, "Which restaurant did I book for dinner?")
	if err != nil {
		t.Fatal(err)
	}
	for _, hit := range hits {
		if hit.SourceID == reservation {
			t.Fatal("deleted source remained searchable")
		}
	}
	encoded, _ := json.Marshal(c.MemoryIndexSettings())
	if strings.Contains(string(encoded), "Le Bernardin") {
		t.Fatal("source content escaped into settings")
	}
}

func waitMemoryIndex(ctx context.Context, t *testing.T, c *Client, count int) {
	t.Helper()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		status := c.MemoryIndexStatus()
		if status.State == "unavailable" {
			t.Fatalf("local semantic index unavailable: %+v", status)
		}
		if status.Complete && status.IndexedSources == count {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("index wait: %+v: %v", status, ctx.Err())
		case <-ticker.C:
		}
	}
}

func TestHybridFusionAppliesHigherAmbiguityThresholdWithoutDroppingExactHits(t *testing.T) {
	first, weak, strong := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	exact := search.ExactPage{Hits: []search.IndexHit{{SourceID: first, Bytes: 20, Score: 4}}}
	hits := []semantic.Hit{{SourceID: first, SourceBytes: 20, Score: .35, Start: 0, End: 20}, {SourceID: weak, SourceBytes: 20, Score: .4, Start: 0, End: 20}, {SourceID: strong, SourceBytes: 20, Score: .7, Start: 0, End: 20}}
	page := fuseSearchPages(exact, hits, 8, .55)
	if len(page.Hits) != 2 {
		t.Fatal("ambiguity threshold accepted weak source", page)
	}
	seen := map[string]bool{}
	for _, hit := range page.Hits {
		seen[hit.SourceID] = true
	}
	if !seen[first] || !seen[strong] || seen[weak] {
		t.Fatal("strong semantic or exact evidence was lost", page)
	}
}
