package client

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/deletion"
	"github.com/hazcod/blur/internal/preferences"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/durable"
)

type multipartAnswerGenerator struct{ id string }

func (g multipartAnswerGenerator) Generate(_ context.Context, packed assistant.PackedContext, _ int) (assistant.ModelAnswer, error) {
	if !strings.Contains(packed.User, "rollback owner is Mira") || !slices.Contains(packed.SourceIDs, g.id) {
		return assistant.ModelAnswer{}, errors.New("published tail missing from model context")
	}
	return assistant.ModelAnswer{Text: "Mira owns the rollback.", SourceIDs: []string{g.id}}, nil
}

type wholeDocumentIntegrationGenerator struct {
	mapText  strings.Builder
	mapCalls int
}

func (g *wholeDocumentIntegrationGenerator) Generate(_ context.Context, packed assistant.PackedContext, _ int) (assistant.ModelAnswer, error) {
	var payload struct {
		Evidence []struct {
			Text string `json:"text"`
		} `json:"evidence"`
	}
	if err := json.Unmarshal([]byte(packed.User), &payload); err != nil {
		return assistant.ModelAnswer{}, err
	}
	if strings.Contains(packed.System, "BLUR FINAL WHOLE-DOCUMENT ANSWER") {
		return assistant.ModelAnswer{Text: "The rollback owner is Mira.", SourceIDs: append([]string(nil), packed.SourceIDs...)}, nil
	}
	var text string
	for _, item := range payload.Evidence {
		text += item.Text
	}
	if strings.Contains(packed.System, "BLUR WHOLE-DOCUMENT MAP") {
		g.mapCalls++
		g.mapText.WriteString(text)
	}
	answer := "The document describes compiler implementation details."
	if strings.Contains(text, "rollback owner is Mira") {
		answer = "The captured text states that the rollback owner is Mira."
	}
	return assistant.ModelAnswer{Text: answer, SourceIDs: append([]string(nil), packed.SourceIDs...)}, nil
}

func TestNativeMultipartTailReachesPublicSearchAndAskThenWholeSourceDeletion(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	path := filepath.Join(directory, "memory.db")
	key := securestore.OfflineFixtureKey()
	store, err := durable.OpenDurable(path, key, durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	prefs, err := preferences.NewStore(filepath.Join(directory, "preferences.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := preferences.DefaultConfig()
	cfg.CaptureEnabled = true
	if err = prefs.Save(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	sink := &nativeCaptureCommitSink{store: store, preferences: prefs}
	builder, err := capture.NewCaptureBuilder("editor.app", "example.test", strings.Repeat("d", 64), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	defer builder.Abort()
	if err = sink.BeginCapture(ctx, builder.Header()); err != nil {
		t.Fatal(err)
	}
	emit := func(ctx context.Context, parts []capture.CapturePart) error {
		return sink.AppendCaptureParts(ctx, builder.Header(), parts)
	}
	text := strings.Repeat("Compiler implementation details and reviewed code.\n", 7000) + "The rollback owner is Mira."
	documentText := text
	for len(text) > 0 {
		n := min(4096, len(text))
		if err = builder.Write(ctx, text[:n], emit); err != nil {
			t.Fatal(err)
		}
		text = text[n:]
	}
	summary, err := builder.Finish(ctx, capture.CaptureComplete, emit)
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.SearchExact(ctx, search.ExactQuery{Terms: []string{"mira"}, TopK: 8, MaxRows: 32, MaxBytes: search.MaxIndexBytes})
	if err != nil || len(before.Hits) != 0 {
		t.Fatal("unfinished capture leaked", before, err)
	}
	receipt, err := sink.PublishDocumentCapture(ctx, summary)
	if err != nil || !receipt.Committed || !receipt.New || receipt.SourceID == "" {
		t.Fatal("native publication", receipt, err)
	}
	for range 128 {
		more, err := store.IndexPendingCaptures(ctx, 16)
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
	}
	// Reopen the encrypted source before asking: in-memory staging cannot satisfy this proof.
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = durable.OpenDurable(path, key, durable.OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(store)
	start := summary.Header().CapturedAt().Truncate(time.Hour)
	window, err := readSourceWindow(ctx, store, storage.ObservationWindowRequest{Start: start, End: start.Add(time.Hour), Limit: 64, MaxBytes: storage.MaxObservationPageBytes})
	if err != nil || len(window.Sources) != int(summary.PartCount()) || window.Pending {
		t.Fatal("complete capture omitted from source timeline", len(window.Sources), summary.PartCount(), window.Pending, err)
	}
	apps, err := client.KnownApps(ctx)
	if err != nil || len(apps) == 0 {
		t.Fatal("new capture app omitted from planner", apps, err)
	}
	view, err := client.Today(summary.Header().CapturedAt().In(time.Local).Format("2006-01-02"))
	if err != nil || len(view.RecentCaptures) == 0 {
		t.Fatal("native document omitted from Today", view.RecentCaptures, err)
	}
	hits, err := client.SearchMemoryContext(ctx, "mira")
	if err != nil || len(hits) != 1 || !strings.Contains(hits[0].Text, "rollback owner is Mira") {
		t.Fatal("public Search omitted large captured tail", hits, err)
	}
	if hits[0].SourceID == summary.ContentID() || hits[0].SourceID == receipt.SourceID {
		t.Fatal("tail did not keep its own actual part identity")
	}
	stored, found, err := store.GetSourceEvidence(ctx, hits[0].SourceID)
	if err != nil || !found {
		t.Fatal("tail source could not be authenticated", found, err)
	}
	provenance, ok := stored.Provenance()
	if !ok {
		t.Fatal("multipart tail lost its edition identity")
	}
	retrieval, err := client.RetrievePlan(ctx, assistant.Plan{Kind: assistant.KindLookup, Question: "Mira", Terms: []string{"mira"}})
	if err != nil {
		t.Fatal("Ask retrieval failed", err)
	}
	var retrieved *assistant.Evidence
	for i := range retrieval.Evidence {
		if retrieval.Evidence[i].SourceID == hits[0].SourceID {
			retrieved = &retrieval.Evidence[i]
			break
		}
	}
	if retrieved == nil || retrieved.Document.EditionID != provenance.EditionID || retrieved.Document.FamilyID != provenance.FamilyID || retrieved.Document.Publication != provenance.Publication || retrieved.Document.PartIndex != provenance.PartIndex || retrieved.Document.PartCount != provenance.PartCount || retrieved.Document.TotalBytes != provenance.TotalBytes || retrieved.Document.Start != provenance.Start || retrieved.Document.End != provenance.End || retrieved.Document.Coverage != uint8(provenance.Coverage) {
		t.Fatal("Ask retrieval discarded authenticated document provenance", retrieval.Evidence, provenance)
	}
	var rebuilt strings.Builder
	var streamed int64
	if err := client.StreamWholeDocumentParts(ctx, *retrieved, func(part assistant.Evidence) error {
		if part.SourceID != capture.CapturePartSourceID(provenance.EditionID, provenance.Publication, part.Document.PartIndex) || part.Document.EditionID != provenance.EditionID || part.Document.Publication != provenance.Publication || part.Document.Start < 0 || part.Document.End-part.Document.Start != int64(len(part.Text)) {
			return errors.New("whole-document stream lost original part identity")
		}
		rebuilt.WriteString(part.Text)
		streamed++
		return nil
	}); err != nil || streamed != provenance.PartCount || rebuilt.String() != documentText {
		t.Fatal("whole-document streaming was incomplete or reordered", streamed, provenance.PartCount, err)
	}
	wrongPublication := *retrieved
	wrongPublication.Document.Publication = strings.Repeat("e", 64)
	yielded := 0
	if err := client.StreamWholeDocumentParts(ctx, wrongPublication, func(assistant.Evidence) error { yielded++; return nil }); !errors.Is(err, storage.ErrCaptureCursorChanged) || yielded != 0 {
		t.Fatal("whole-document stream accepted a replaced publication", yielded, err)
	}
	engineResult, err := search.New(store).Search(ctx, search.Request{Query: "mira", ResultLimit: 8, SnippetBytes: 512, ResponseBytes: search.MaxResponseBytes, IndexLimit: 64, IndexBytes: search.MaxIndexBytes, ScanLimit: 4})
	if err != nil || len(engineResult.Results) != 1 || engineResult.Results[0].ProvenanceKind != "raw_sanitized_capture_part" {
		t.Fatal("public search engine lost capture provenance", engineResult, err)
	}
	answer, err := assistant.NewService(client, multipartAnswerGenerator{id: hits[0].SourceID}, integrationTokenCounter{}, assistant.ContextTokenLimit).Ask(ctx, "Who is Mira?")
	if err != nil || answer.Answer.Text != "Mira owns the rollback." || len(answer.Sources) != 1 || answer.Sources[0].SourceID != hits[0].SourceID || !strings.Contains(answer.Sources[0].Text, "rollback owner is Mira") {
		t.Fatal("Ask could not cite published tail", answer, err)
	}
	wholeGenerator := &wholeDocumentIntegrationGenerator{}
	whole, err := assistant.NewService(client, wholeGenerator, integrationTokenCounter{}, assistant.ContextTokenLimit).Ask(ctx, "Summarize the entire page")
	if err != nil || whole.Answer.Text != "The rollback owner is Mira." || len(whole.Sources) == 0 || !slices.Contains(whole.Answer.SourceIDs, hits[0].SourceID) || wholeGenerator.mapCalls < 2 {
		t.Fatal("whole-document Ask did not process and cite the complete edition", whole.Answer, len(whole.Sources), wholeGenerator.mapCalls, err)
	}
	if wholeGenerator.mapText.String() != documentText {
		t.Fatalf("selected model did not receive every captured byte in order: got %d, want %d", wholeGenerator.mapText.Len(), len(documentText))
	}
	deleter, err := deletion.NewService(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = deleter.DeleteSource(ctx, hits[0].SourceID); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.GetCapture(ctx, summary.ContentID()); err != nil || found {
		t.Fatal("deleting tail retained whole source", found, err)
	}
	if after, err := client.SearchMemoryContext(ctx, "mira"); err != nil || len(after) != 0 {
		t.Fatal("deleted tail still searchable", after, err)
	}
}
