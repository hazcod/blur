package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/deletion"
	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/knowledge/derive"
	"github.com/hazcod/blur/internal/knowledge/enrich"
	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

func TestLibraryCombinesManualAndAutomaticPagesWithoutLosingContinuation(t *testing.T) {
	s, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	ctx := context.Background()
	for i := 0; i < 51; i++ {
		if err := s.PutKnowledgeEntity(ctx, knowledge.Entity{ID: fmt.Sprintf("manual-%03d", i), Kind: knowledge.Topic, Name: fmt.Sprintf("Manual topic %d", i), State: knowledge.Confirmed}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		text := fmt.Sprintf("Project %d uses Go.", i)
		source, _ := capture.SanitizeObservation("editor.app", "code.example", text, 4096)
		if err := s.PutObservationAt(ctx, source, time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := s.ApplyEnrichment(ctx, enrich.Chunk{SourceID: source.ContentID(), ModelKey: enrich.ModelKey("fixture"), End: len(text), Next: len(text), Complete: true, Proposals: []derive.Proposal{{Kind: knowledge.Project, Name: fmt.Sprintf("Project %d", i), Fact: text, SourceID: source.ContentID()}}}); err != nil {
			t.Fatal(err)
		}
	}
	c := NewClient(s)
	defer c.Close()
	page, err := c.Knowledge("")
	if err != nil || len(page.Entities) != 50 || !page.HasMore {
		t.Fatalf("first page %+v %v", page, err)
	}
	next, err := c.Knowledge(page.Next)
	if err != nil {
		t.Fatal(err)
	}
	merged, err := appendKnowledgePage(page, next, page.Next)
	if err != nil || len(merged.Entities) != 54 || merged.HasMore {
		t.Fatalf("combined %+v %v", merged, err)
	}
	id := merged.Entities[53].ID
	if err := c.DeleteKnowledge(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.GetInferredKnowledge(ctx, id); err != nil || found {
		t.Fatal("automatic entry deletion was not routed", found, err)
	}
}

func TestDerivedPublicationFenceRejectsResultsFromBeforeDeletion(t *testing.T) {
	c := NewClient(nil)
	defer c.Close()
	epoch, stable := c.BeginMemoryRead()
	if !stable {
		t.Fatal("initial memory not stable")
	}
	c.NotifyMemoryChanging()
	c.NotifyMemoryStable()
	published := false
	if err := c.CommitDerivedMemory(epoch, func() error { published = true; return nil }); !errors.Is(err, ErrMemoryChanging) || published {
		t.Fatal("stale model output published")
	}
	epoch, _ = c.BeginMemoryRead()
	if err := c.CommitDerivedMemory(epoch, func() error { published = true; return nil }); err != nil || !published {
		t.Fatal("current publication rejected", err)
	}
}

func TestInferredLibraryAnswersRetainContextAndCorrectionProvenance(t *testing.T) {
	s, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	ctx := context.Background()
	c := NewClient(s)
	defer c.Close()
	var correctedID string
	for i, domain := range []string{"one.example", "two.example"} {
		text := fmt.Sprintf("Atlas uses language %d.", i)
		o, _ := capture.SanitizeObservation("editor.app", domain, text, 4096)
		if err := s.PutObservationAt(ctx, o, time.Now()); err != nil {
			t.Fatal(err)
		}
		chunk := enrich.Chunk{SourceID: o.ContentID(), ModelKey: enrich.ModelKey("fixture"), End: len(text), Next: len(text), Complete: true, Proposals: []derive.Proposal{{Kind: knowledge.Project, Name: "Atlas", Fact: text, SourceID: o.ContentID()}}}
		if err := s.ApplyEnrichment(ctx, chunk); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			correctedID = o.ContentID()
			if err := c.ChangeInferredFact(ctx, enrich.EntityID(knowledge.Project, "Atlas", "editor.app", domain), enrich.FactID(o.ContentID(), text), "Atlas now uses Go.", false); err != nil {
				t.Fatal(err)
			}
		}
	}
	evidence, _, err := c.entityEvidence(ctx, assistant.Plan{Kind: assistant.KindEntity, Entity: "Atlas"}, false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	correction, other := false, false
	for _, item := range evidence {
		if item.Origin == assistant.OriginUserCorrection {
			correction = true
			if item.SourceID == correctedID || !strings.Contains(item.Text, "Atlas now uses Go.") {
				t.Fatal("correction pretended to be captured text", item)
			}
		}
		if item.Origin == assistant.OriginInferred && item.Domain == "two.example" {
			other = true
		}
	}
	if !correction || !other {
		t.Fatalf("missing context/explicit correction %+v", evidence)
	}
	packed, err := assistant.PackContext("What is Atlas?", evidence)
	if err != nil || !strings.Contains(packed.User, `"origin":"user_correction"`) {
		t.Fatalf("correction origin lost at model boundary: %+v %v", packed, err)
	}
	scoped, err := c.inferredEntityEvidence(ctx, assistant.Plan{Entity: "Atlas", Domains: []string{"two.example"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range scoped {
		if item.Domain == "one.example" || item.Origin == assistant.OriginUserCorrection {
			t.Fatal("site scope leaked another context", item)
		}
	}
}

func TestEntitySummaryHasOwnCitationAndRetainsEverySupportingSource(t *testing.T) {
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()
	first, err := capture.SanitizeObservation("editor.app", "one.example", "Atlas builds command-line tools.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	second, err := capture.SanitizeObservation("editor.app", "two.example", "Atlas stores local project memory.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []capture.SanitizedObservation{first, second} {
		if err := store.PutObservationAt(ctx, source, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	entity := knowledge.Entity{
		ID: "atlas-project", Kind: knowledge.Project, Name: "Atlas", State: knowledge.Confirmed,
		Facts: []knowledge.Fact{
			{ID: "atlas-cli", Text: "Builds command-line tools.", State: knowledge.Confirmed, Sources: []knowledge.Source{{ID: first.ContentID()}}},
			{ID: "atlas-memory", Text: "Stores local project memory.", State: knowledge.Confirmed, Sources: []knowledge.Source{{ID: second.ContentID()}}},
		},
	}
	if err := store.PutKnowledgeEntity(ctx, entity); err != nil {
		t.Fatal(err)
	}
	c := NewClient(store)
	defer c.Close()
	evidence, _, err := c.entityEvidence(ctx, assistant.Plan{Kind: assistant.KindEntity, Entity: "Atlas"}, true, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var summary bool
	sources := map[string]bool{}
	for _, item := range evidence {
		if item.Layer == assistant.LayerDigest {
			summary = true
			if item.SourceID == first.ContentID() || item.SourceID == second.ContentID() || item.AppName != "Library" {
				t.Fatalf("Library summary reused a capture citation or lost its identity: %+v", item)
			}
		}
		if item.Layer == assistant.LayerSource {
			sources[item.SourceID] = true
		}
	}
	if !summary || !sources[first.ContentID()] || !sources[second.ContentID()] {
		t.Fatalf("Library summary lost a linked source: summary=%t sources=%v evidence=%+v", summary, sources, evidence)
	}
}

func TestOrdinaryTopicQuestionRetrievesCurrentLibraryCorrection(t *testing.T) {
	s, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	ctx := context.Background()
	c := NewClient(s)
	defer c.Close()
	text := "Atlas will launch in June."
	source, err := capture.SanitizeObservation("editor.app", "work.example", text, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutObservationAt(ctx, source, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyEnrichment(ctx, enrich.Chunk{SourceID: source.ContentID(), ModelKey: enrich.ModelKey("fixture"), End: len(text), Next: len(text), Complete: true, Proposals: []derive.Proposal{{Kind: knowledge.Project, Name: "Atlas", Fact: text, SourceID: source.ContentID()}}}); err != nil {
		t.Fatal(err)
	}
	entityID := enrich.EntityID(knowledge.Project, "Atlas", "editor.app", "work.example")
	if err := c.ChangeInferredFact(ctx, entityID, enrich.FactID(source.ContentID(), text), "Atlas now launches in July.", false); err != nil {
		t.Fatal(err)
	}
	plan, err := assistant.PlanQuestion("What did we decide about Atlas?", time.Now(), nil)
	if err != nil || plan.Kind != assistant.KindLookup || plan.Entity != "" {
		t.Fatalf("question should exercise ordinary topic retrieval: %+v err=%v", plan, err)
	}
	retrieval, err := c.RetrievePlan(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	var correction, oldCapture bool
	for _, item := range retrieval.Evidence {
		correction = correction || item.Origin == assistant.OriginUserCorrection && strings.Contains(item.Text, "Atlas now launches in July.")
		oldCapture = oldCapture || strings.Contains(item.Text, "Atlas will launch in June.")
	}
	if !correction || oldCapture {
		t.Fatalf("ordinary topic retrieval missed the correction or returned stale capture without it: %+v", retrieval.Evidence)
	}
	answer, err := c.AskMemory(ctx, "What did we decide about Atlas?")
	if err != nil || !answer.Direct || !strings.Contains(answer.Answer.Text, "Atlas now launches in July.") || strings.Contains(answer.Answer.Text, "Atlas will launch in June.") || len(answer.Sources) != 1 || answer.Sources[0].Origin != assistant.OriginUserCorrection {
		t.Fatalf("ordinary Ask did not prioritize the current Library correction: %+v err=%v", answer, err)
	}
}

func TestLibraryCopyDoesNotClaimCompleteCoverageOnFailure(t *testing.T) {
	if text := libraryStatusCopy(enrich.Status{State: "unavailable", ErrorCode: "capacity"}); !strings.Contains(text, "incomplete") {
		t.Fatal(text)
	}
	if text := libraryStatusCopy(enrich.Status{State: "unavailable", ErrorCode: "local_model_required"}); !strings.Contains(text, "Cloud models") {
		t.Fatal(text)
	}
	if text := libraryStatusCopy(enrich.Status{State: "paused"}); !strings.Contains(text, "power") {
		t.Fatal(text)
	}
}

func TestLibraryCoverageExplainsRecoveryForModelFailures(t *testing.T) {
	refused := enrich.Status{State: "idle", CoverageLimited: true, LastErrorCode: "model_refused"}
	if text := libraryStatusCopy(refused); !strings.Contains(text, "declined") || !strings.Contains(text, "different local model") {
		t.Fatalf("refusal recovery is unclear: %s", text)
	}
	if action, retries := libraryRetryAction(refused); action != "Check for new sources" || retries {
		t.Fatalf("refusal action implies a retry that cannot happen: %q retries=%t", action, retries)
	}
	invalid := enrich.Status{State: "idle", CoverageLimited: true, LastErrorCode: "invalid_output"}
	if text := libraryStatusCopy(invalid); !strings.Contains(text, "unusable results") || !strings.Contains(text, "Retry") {
		t.Fatalf("invalid-output recovery is unclear: %s", text)
	}
	if action, retries := libraryRetryAction(invalid); action != "Retry saved sources" || !retries {
		t.Fatalf("invalid-output action does not offer retry: %q retries=%t", action, retries)
	}
}

// Synthetic evidence only. This proves the actual local model, automatic
// controller, encrypted backfill, Library API, corrections, reopen and deletion
// together. It never invokes the manual Suggest facts action.
func TestLocalAutomaticLibraryThroughEncryptedClient(t *testing.T) {
	if os.Getenv("BLUR_LOCAL_MODEL_INTEGRATION") != "1" {
		t.Skip("requires a local answer model")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	dir := t.TempDir()
	path := filepath.Join(dir, "memory.db")
	key := securestore.OfflineFixtureKey()
	s, err := durable.OpenDurable(path, key, durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	text := "Project Atlas is owned by Mina. Atlas uses Rust for its desktop app."
	source, _ := capture.SanitizeObservation("editor.app", "code.example", text, 4096)
	if err := s.PutObservationAt(ctx, source, time.Now()); err != nil {
		t.Fatal(err)
	}
	openClient := func() *Client {
		deleter, err := deletion.NewService(s, nil)
		if err != nil {
			t.Fatal(err)
		}
		noteService := notes.NewService(s, nil, nil)
		controller := newLocalAIController(filepath.Join(dir, "local-ai.json"), noteService)
		c := NewClient(s).WithNotes(noteService).WithLocalAI(controller).WithDeletion(deleter)
		controller.SetLibraryRuntime(s, s, enrich.Options{Fence: c, OnChange: c.notifyMemoryIndexChanged})
		c.addMemoryChangingHook(controller.InvalidateLibrary)
		t.Cleanup(func() { c.Close(); controller.Close() })
		return c
	}
	waitReady := func(c *Client) {
		t.Helper()
		for {
			status := c.LibraryStatus()
			if status.State == "ready" && status.Complete {
				return
			}
			if status.State == "unavailable" {
				t.Fatalf("Library unavailable: %+v", status)
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-c.MemoryIndexChanges():
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	c := openClient()
	name := os.Getenv("BLUR_LOCAL_TEST_MODEL")
	if name == "" {
		name = "qwen3:14b"
	}
	if err := c.ConfigureLocalAI(ctx, LocalAISettings{Enabled: true, AutomaticLibrary: true, Endpoint: "http://127.0.0.1:11434", Model: name}); err != nil {
		t.Fatal(err)
	}
	waitReady(c)
	page, err := c.Knowledge("")
	if err != nil || len(page.Entities) == 0 {
		t.Fatalf("automatic Library stayed empty %+v %v", page, err)
	}
	entry := page.Entities[0]
	if len(entry.Facts) == 0 || entry.Facts[0].Sources[0].ID != source.ContentID() {
		t.Fatalf("missing source provenance %+v", entry)
	}
	if err := c.ChangeInferredFact(ctx, entry.ID, entry.Facts[0].ID, "My explicit corrected project fact.", false); err != nil {
		t.Fatal(err)
	}
	c.localAI.WakeLibrary()
	waitReady(c)
	c.Close()
	c.localAI.Close()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = durable.OpenDurable(path, key, durable.OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	c = openClient()
	waitReady(c)
	updated, err := c.InferredKnowledge(ctx, entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Facts[0].Text != "My explicit corrected project fact." || !updated.Facts[0].Sources[0].UserAuthored {
		t.Fatalf("correction lost after reopen: %+v", updated)
	}
	if _, err := c.DeleteSource(source.ContentID()); err != nil {
		t.Fatal(err)
	}
	updated, err = c.InferredKnowledge(ctx, entry.ID)
	if err != nil || len(updated.Facts) != 1 || len(updated.ActivitySourceIDs) != 0 {
		t.Fatalf("source deletion/provenance: %+v %v", updated, err)
	}
	if _, err := c.DeleteAll(ctx); err != nil {
		t.Fatal(err)
	}
	page, err = c.Knowledge("")
	if err != nil || len(page.Entities) != 0 {
		t.Fatalf("delete-all retained Library: %+v %v", page, err)
	}
}
