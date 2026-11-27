package client

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/knowledge/derive"
	"github.com/hazcod/blur/internal/knowledge/enrich"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

func libraryNamesIn(facts []string, names ...string) map[string]bool {
	found := map[string]bool{}
	for _, name := range names {
		found[name] = hasFact(facts, name+" owns")
	}
	return found
}

func TestLibraryNameVariantsReachTheRightEntry(t *testing.T) {
	c := libraryNameFixture(t, "Threat-Bot", "José Müller", "Dima Petrov")
	all := []string{"Threat-Bot", "José Müller", "Dima Petrov"}
	for question, want := range map[string]string{
		"Tell me about threat bot":             "Threat-Bot",
		"Tell me about threatbot":              "Threat-Bot",
		"Tell me about threat-bot":             "Threat-Bot",
		"Tell me about Jose":                   "José Müller",
		"Tell me about Müller José":            "José Müller",
		"Tell me about Dima":                   "Dima Petrov",
		"Tell me about Petrov":                 "Dima Petrov",
		"Tell me about Dima Petrov's":          "Dima Petrov",
		"What did we decide about Dima?":       "Dima Petrov",
		"What did we decide about threat bot?": "Threat-Bot",
	} {
		found := libraryNamesIn(libraryFacts(t, c, question), all...)
		for _, name := range all {
			if found[name] != (name == want) {
				t.Errorf("%q: %v want only %q", question, found, want)
				break
			}
		}
	}
}

func TestLibraryNameVariantsKeepContextsSeparate(t *testing.T) {
	c := libraryNameFixture(t, "Dima Petrov", "Dima Ivanov")
	both := libraryNamesIn(libraryFacts(t, c, "Tell me about Dima"), "Dima Petrov", "Dima Ivanov")
	if !both["Dima Petrov"] || !both["Dima Ivanov"] {
		t.Fatalf("first-name question lost an entry: %v", both)
	}
	one := libraryNamesIn(libraryFacts(t, c, "Tell me about Dima Petrov"), "Dima Petrov", "Dima Ivanov")
	if !one["Dima Petrov"] || one["Dima Ivanov"] {
		t.Fatalf("full-name question widened: %v", one)
	}
}

func TestLibraryExactNameBeatsLongerNamesContainingIt(t *testing.T) {
	c := libraryNameFixture(t, "Atlas", "Project Atlas Migration Plan")
	got := libraryNamesIn(libraryFacts(t, c, "Tell me about Atlas"), "Atlas", "Project Atlas Migration Plan")
	if !got["Atlas"] || got["Project Atlas Migration Plan"] {
		t.Fatalf("exact name should win: %v", got)
	}
}

func TestLibraryNamePartsStayOffOrdinaryTopicQuestions(t *testing.T) {
	c := libraryEntryFixture(t, libraryEntry{knowledge.Project, "Production Deploy"}, libraryEntry{knowledge.Person, "Dima Petrov"})
	if got := libraryNamesIn(libraryFacts(t, c, "What did we decide about production?"), "Production Deploy"); got["Production Deploy"] {
		t.Fatal("an incidental word pulled a non-person entry into a topic answer")
	}
	if got := libraryNamesIn(libraryFacts(t, c, "Tell me about Production"), "Production Deploy"); !got["Production Deploy"] {
		t.Fatal("entity question lost its name-part match")
	}
	if got := libraryNamesIn(libraryFacts(t, c, "What did we decide about Dima?"), "Dima Petrov"); !got["Dima Petrov"] {
		t.Fatal("topic question lost a person named by first name")
	}
}

func TestLibraryNamePartsAreBoundedAndSeparate(t *testing.T) {
	three := libraryNameFixture(t, "Dima A", "Dima B", "Dima C")
	got := libraryNamesIn(libraryFacts(t, three, "Tell me about Dima"), "Dima A", "Dima B", "Dima C")
	if !got["Dima A"] || !got["Dima B"] || !got["Dima C"] {
		t.Fatalf("ambiguous first name merged or lost entries: %v", got)
	}
	var names []string
	for i := 0; i < 9; i++ {
		names = append(names, fmt.Sprintf("Dima %c", 'A'+i))
	}
	nine := libraryNameFixture(t, names...)
	plan, err := assistant.PlanQuestion("Tell me about Dima", time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	r, err := nine.RetrievePlan(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range r.Evidence {
		if item.Origin == assistant.OriginInferred {
			t.Fatalf("nine candidates should return none: %q", item.Text)
		}
	}
	if !r.Diagnosis.LibraryRecallLimited {
		t.Fatal("ambiguity was not reported as limited")
	}
}

func TestLibraryNamePartsCarryNoCorrectionAuthority(t *testing.T) {
	ctx := context.Background()
	s, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	text := "Dima Petrov owns the vendor contract renewal."
	source, err := capture.SanitizeObservation("editor.app", "work.example", text, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutObservationAt(ctx, source, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	chunk := enrich.Chunk{SourceID: source.ContentID(), ModelKey: enrich.ModelKey("fixture"), End: len(text), Next: len(text), Complete: true,
		Proposals: []derive.Proposal{{Kind: knowledge.Person, Name: "Dima Petrov", Fact: text, SourceID: source.ContentID()}}}
	if err := s.ApplyEnrichment(ctx, chunk); err != nil {
		t.Fatal(err)
	}
	entities, err := s.FindInferredKnowledge(ctx, "Dima Petrov")
	if err != nil || len(entities) != 1 {
		t.Fatal(entities, err)
	}
	if err := s.CorrectInferredFact(ctx, entities[0].ID, entities[0].Facts[0].ID, "Dima Petrov no longer owns the vendor contract."); err != nil {
		t.Fatal(err)
	}
	for more := true; more; {
		if more, err = s.IndexPendingExact(ctx, 32); err != nil {
			t.Fatal(err)
		}
	}
	c := NewClient(s)
	t.Cleanup(c.Close)
	hasCapture := func(question string) bool {
		plan, err := assistant.PlanQuestion(question, time.Now(), nil)
		if err != nil {
			t.Fatal(err)
		}
		r, err := c.RetrievePlan(ctx, plan)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range r.Evidence {
			if item.SourceID == source.ContentID() {
				return true
			}
		}
		return false
	}
	if !hasCapture("What did we decide about Dima?") {
		t.Fatal("a name-part match removed the original capture")
	}
	if hasCapture("What did we decide about Dima Petrov?") {
		t.Fatal("whole-name correction no longer supersedes its capture")
	}
}
