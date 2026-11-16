package client

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
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

func TestQuickHistoryRetainsCitedSourceHandlesAcrossAnswersOnly(t *testing.T) {
	id := strings.Repeat("a", 64)
	result := assistant.Result{Answer: assistant.Answer{Text: "Mira leads Aurora.", SourceIDs: []string{id}}, Sources: []assistant.Evidence{{SourceID: id, Text: "Mira leads Aurora."}}, Plan: assistant.Plan{Kind: assistant.KindEntity, Entity: "Mira"}}
	turns := appendQuickConversation(nil, "Who is Mira?", result)
	result.Answer.SourceIDs[0] = strings.Repeat("b", 64)
	history := quickHistoryTurns(turns)
	if len(history) != 1 || len(history[0].SourceIDs) != 1 || history[0].SourceIDs[0] != id {
		t.Fatalf("lost or aliased answer provenance: %+v", history)
	}
	history[0].SourceIDs[0] = strings.Repeat("c", 64)
	if quickHistoryTurns(turns)[0].SourceIDs[0] != id {
		t.Fatal("history can mutate retained citations")
	}
	turns = appendQuickAction(turns, "Save a note", "Saved")
	if got := quickHistoryTurns(turns); len(got) != 0 {
		t.Fatal("private followup crossed action boundary", got)
	}
}

func TestMemoryContinuationReloadsEncryptedNoteAndForgetsDeletedSource(t *testing.T) {
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(store.Close)
	deleter, err := deletion.NewService(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient(store).WithNotes(notes.NewService(store, nil, nil)).WithDeletion(deleter)
	defer c.Close()
	note, err := c.AddNote("Mira leads the Aurora project.")
	if err != nil {
		t.Fatal(err)
	}
	first, err := c.AskMemory(context.Background(), "Who is Mira?")
	if err != nil {
		t.Fatal(err)
	}
	history := quickHistoryTurns(appendQuickConversation(nil, "Who is Mira?", first))
	g := &namedSubjectAnswerGenerator{}
	got, err := assistant.NewService(c, g, integrationTokenCounter{}, assistant.ContextTokenLimit).WithPersonalContext(true).AskWithHistory(context.Background(), "Make it shorter", history)
	if err != nil || got.General || got.ConversationTurns != 1 || g.memoryCalls != 1 || len(g.packed.SourceIDs) != 1 || g.packed.SourceIDs[0] != note.SourceID || !strings.Contains(g.packed.User, "Mira leads the Aurora project.") {
		t.Fatal("continuation didn't reload original encrypted source", got, g.packed, err)
	}
	if _, err := c.DeleteSource(note.SourceID); err != nil {
		t.Fatal(err)
	}
	g = &namedSubjectAnswerGenerator{}
	got, err = assistant.NewService(c, g, integrationTokenCounter{}, assistant.ContextTokenLimit).WithPersonalContext(true).AskWithHistory(context.Background(), "Make it shorter", history)
	if err != nil || g.memoryCalls != 0 || !got.Answer.InsufficientEvidence || len(got.Sources) != 0 {
		t.Fatal("deleted note came back through transcript", got, g.packed, err)
	}
}

func TestMemoryContinuationReloadsCurrentLibraryCorrection(t *testing.T) {
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(store.Close)
	ctx := context.Background()
	c := NewClient(store)
	defer c.Close()
	text := "Mira leads the OldProject project."
	source, _ := capture.SanitizeObservation("editor.app", "one.example", text, 4096)
	if err := store.PutObservationAt(ctx, source, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyEnrichment(ctx, enrich.Chunk{SourceID: source.ContentID(), ModelKey: enrich.ModelKey("fixture"), End: len(text), Next: len(text), Complete: true, Proposals: []derive.Proposal{{Kind: knowledge.Person, Name: "Mira", Fact: text, SourceID: source.ContentID()}}}); err != nil {
		t.Fatal(err)
	}
	first, err := c.AskMemory(ctx, "Who is Mira?")
	if err != nil {
		t.Fatal(err)
	}
	history := quickHistoryTurns(appendQuickConversation(nil, "Who is Mira?", first))
	if err := c.ChangeInferredFact(ctx, enrich.EntityID(knowledge.Person, "Mira", "editor.app", "one.example"), enrich.FactID(source.ContentID(), text), "Mira now leads Aurora.", false); err != nil {
		t.Fatal(err)
	}
	g := &namedSubjectAnswerGenerator{}
	got, err := assistant.NewService(c, g, integrationTokenCounter{}, assistant.ContextTokenLimit).WithPersonalContext(true).AskWithHistory(ctx, "What is her project?", history)
	if err != nil || g.memoryCalls != 1 || !strings.Contains(g.packed.User, "Mira now leads Aurora.") || !strings.Contains(g.packed.User, `"origin":"user_correction"`) {
		t.Fatal("fresh correction missing from followup", got, g.packed, err)
	}
}

func TestSourceFollowupRejectsAnEpochChangeDuringHydration(t *testing.T) {
	data := &followupChangingData{testData: &testData{}}
	c := NewClient(data)
	defer c.Close()
	data.change = func() { c.NotifyMemoryChanging(); c.NotifyMemoryStable() }
	got, err := c.RetrieveSources(context.Background(), assistant.Plan{Kind: assistant.KindLookup, Question: "Who is Mira?", Terms: []string{"mira"}}, []string{strings.Repeat("a", 64)})
	if !errors.Is(err, ErrMemoryChanging) || len(got.Evidence) != 0 {
		t.Fatal("stale hydration escaped mutation fence", got, err)
	}
}

type followupChangingData struct {
	*testData
	change func()
}

func (d *followupChangingData) GetObservation(context.Context, string) (capture.SanitizedObservation, bool, error) {
	d.change()
	return capture.SanitizedObservation{}, false, nil
}

func TestTimeFollowupRecomputesCurrentRecordedActivity(t *testing.T) {
	f := newAskFixture(t)
	defer f.client.Close()
	p := todayPlan(assistant.KindTime, slackApp)
	p.Question = "How much time did I spend in Slack today?"
	p.AppNames = []string{"Slack"}
	got, err := f.client.RetrieveSources(context.Background(), p, nil)
	if err != nil || len(got.Evidence) != 1 || !strings.Contains(got.Evidence[0].Text, "1h 30m") || !strings.Contains(got.Evidence[0].Text, "foreground") {
		t.Fatal("time followup lacks current structured evidence", got, err)
	}
	f.data.activity = nil
	got, err = f.client.RetrieveSources(context.Background(), p, nil)
	if err != nil || len(got.Evidence) != 0 {
		t.Fatal("stale activity reappeared", got, err)
	}
}

func TestSourceFollowupCannotBypassANewExplicitTimeScope(t *testing.T) {
	f := newAskFixture(t)
	defer f.client.Close()
	p := todayPlan(assistant.KindLookup, waApp)
	p.Question = "Anna invoice"
	p.Terms = []string{"anna", "invoice"}
	p.MatchOnly = true
	got, err := f.client.RetrieveSources(context.Background(), p, []string{f.ids["wa-old"]})
	if err != nil || len(got.Evidence) != 0 {
		t.Fatal("old citation bypassed explicit time filter", got, err)
	}
}

// A factual followup asks for new evidence about an already identified person.
// It must not be restricted to the biography cited in the preceding answer.
func TestFactualFollowupFindsNewSourceUnderInheritedSubjectAndCurrentFilters(t *testing.T) {
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(store.Close)
	deleter, err := deletion.NewService(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient(store).WithDeletion(deleter)
	defer c.Close()
	ctx := context.Background()
	now := time.Now()
	yesterday := time.Date(now.Year(), now.Month(), now.Day()-1, 12, 0, 0, 0, now.Location())
	c.askNow = func() time.Time { return now }
	bio, _ := capture.SanitizeObservation("notes.app", "", "Mira is our engineering colleague. BIO_ONLY", 1024)
	work, _ := capture.SanitizeObservation(slackApp, "", "Mira: I am working on the Aurora rollback. NEW_WORK", 1024)
	unrelated, _ := capture.SanitizeObservation(slackApp, "", "Ada: I am working on the unrelated release. UNRELATED_WORK", 1024)
	for _, row := range []struct {
		o  capture.SanitizedObservation
		at time.Time
	}{{bio, now}, {work, yesterday}, {unrelated, yesterday.Add(time.Hour)}} {
		if err := store.PutObservationAt(ctx, row.o, row.at); err != nil {
			t.Fatal(err)
		}
	}
	plan := assistant.Plan{Kind: assistant.KindEntity, Entity: "Mira", Terms: []string{"mira"}, Question: "Who is Mira?", MatchOnly: true}
	first := assistant.Result{Plan: plan, Answer: assistant.Answer{Text: "Mira is a colleague. OLD_BIO_ANSWER", SourceIDs: []string{bio.ContentID()}}, Sources: []assistant.Evidence{{SourceID: bio.ContentID(), AppID: bio.AppID(), Text: bio.Text()}}}
	history := quickHistoryTurns(appendQuickConversation(nil, "Who is Mira?", first))
	for _, question := range []string{"What was she working on in Slack yesterday?", "What is her project?"} {
		t.Run(question, func(t *testing.T) {
			g := &namedSubjectAnswerGenerator{}
			got, err := assistant.NewService(c, g, integrationTokenCounter{}, assistant.ContextTokenLimit).WithPersonalContext(true).AskWithHistory(ctx, question, history)
			if err != nil || g.memoryCalls != 1 || !strings.Contains(g.packed.User, "NEW_WORK") || strings.Contains(g.packed.User, "UNRELATED_WORK") || !slices.Contains(g.packed.SourceIDs, work.ContentID()) {
				t.Fatal("factual followup lost fresh subject-matching source", got, g.packed, err)
			}
			if strings.Contains(question, "yesterday") && (strings.Contains(g.packed.User, "OLD_BIO_ANSWER") || strings.Contains(g.packed.User, "BIO_ONLY") || got.ConversationTurns != 0) {
				t.Fatal("new filters admitted old out-of-scope prose", got, g.packed)
			}
		})
	}
	// Transformations retain their original source boundary.
	g := &namedSubjectAnswerGenerator{}
	got, err := assistant.NewService(c, g, integrationTokenCounter{}, assistant.ContextTokenLimit).WithPersonalContext(true).AskWithHistory(ctx, "Make it shorter", history)
	if err != nil || g.memoryCalls != 1 || strings.Contains(g.packed.User, "NEW_WORK") || len(g.packed.SourceIDs) != 1 || g.packed.SourceIDs[0] != bio.ContentID() {
		t.Fatal("rewrite expanded into uncited work sources", got, g.packed, err)
	}
	// New evidence can answer a factual question after the original biography
	// was deleted, without carrying its old answer into the request.
	if _, err := c.DeleteSource(bio.ContentID()); err != nil {
		t.Fatal(err)
	}
	g = &namedSubjectAnswerGenerator{}
	got, err = assistant.NewService(c, g, integrationTokenCounter{}, assistant.ContextTokenLimit).WithPersonalContext(true).AskWithHistory(ctx, "What is her project?", history)
	if err != nil || g.memoryCalls != 1 || !slices.Contains(g.packed.SourceIDs, work.ContentID()) || slices.Contains(g.packed.SourceIDs, bio.ContentID()) || strings.Contains(g.packed.User, "OLD_BIO_ANSWER") || got.ConversationTurns != 0 {
		t.Fatal("new factual answer revived deleted biography", got, g.packed, err)
	}
}
