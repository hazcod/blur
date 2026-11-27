package client

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

type namedSubjectAnswerGenerator struct {
	packed       assistant.PackedContext
	memoryCalls  int
	generalCalls int
}

func (g *namedSubjectAnswerGenerator) Generate(_ context.Context, packed assistant.PackedContext, _ int) (assistant.ModelAnswer, error) {
	g.packed = packed
	g.memoryCalls++
	return assistant.ModelAnswer{InsufficientEvidence: true}, nil
}

func (g *namedSubjectAnswerGenerator) GenerateGeneral(context.Context, string, int) (string, error) {
	g.generalCalls++
	return "A general response.", nil
}

func TestRetrievalCommandRespectsExplicitGeneralMode(t *testing.T) {
	c := libraryNameFixture(t)
	g := &namedSubjectAnswerGenerator{}
	result, err := assistant.NewService(c, g, integrationTokenCounter{}, assistant.ContextTokenLimit).AskWithHistoryMode(context.Background(), "Find voyagerpacket", nil, "general")
	if err != nil || !result.General || g.generalCalls != 1 || g.memoryCalls != 0 || len(result.Answer.SourceIDs) != 0 {
		t.Fatalf("explicit general mode became memory search: %+v general=%d memory=%d err=%v", result, g.generalCalls, g.memoryCalls, err)
	}
}

func TestUnknownNamedSubjectAttributeAbstainsWithOnlyMatchingEncryptedNote(t *testing.T) {
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(store.Close)
	c := NewClient(store).WithNotes(notes.NewService(store, nil, nil))
	defer c.Close()
	note, err := c.AddNote("The rollback owner is Mira Jansen.")
	if err != nil {
		t.Fatal(err)
	}
	// This note matches the requested attribute words, but names another topic.
	if _, err := c.AddNote("The deployment deadline for the unrelated project is November 20."); err != nil {
		t.Fatal(err)
	}
	for _, question := range []string{"What is Mira's deployment deadline?", "What is Mira Jansen’s deployment deadline?", "When is Mira's deployment due?"} {
		t.Run(question, func(t *testing.T) {
			g := &namedSubjectAnswerGenerator{}
			result, err := assistant.NewService(c, g, integrationTokenCounter{}, assistant.ContextTokenLimit).Ask(context.Background(), question)
			if err != nil || result.General || !result.Direct || !result.Answer.InsufficientEvidence || result.SentItems != 0 || g.memoryCalls != 0 || g.generalCalls != 0 || len(g.packed.SourceIDs) != 0 || len(result.Sources) != 1 || result.Sources[0].SourceID != note.SourceID || strings.Contains(result.Answer.Text, "rollback owner") {
				t.Fatalf("unknown attribute was not rejected before generation: result=%+v packed=%+v memoryCalls=%d generalCalls=%d err=%v", result, g.packed, g.memoryCalls, g.generalCalls, err)
			}
		})
	}
	for _, test := range []struct{ question, mode string }{
		{"What is Ada's deployment deadline?", ""},
		{"What is Mira's deployment deadline?", "general"},
		{"Write a poem about Mira's deployment deadline", ""},
		{"Explain Mira's deployment deadline", ""},
		{"What is SQLite in Slack?", ""},
	} {
		t.Run(test.question+test.mode, func(t *testing.T) {
			g := &namedSubjectAnswerGenerator{}
			result, err := assistant.NewService(c, g, integrationTokenCounter{}, assistant.ContextTokenLimit).AskWithHistoryMode(context.Background(), test.question, nil, test.mode)
			if err != nil || !result.General || len(result.Sources) != 0 || result.SentItems != 0 || g.memoryCalls != 0 || g.generalCalls != 1 || len(g.packed.SourceIDs) != 0 {
				t.Fatalf("general boundary used unrelated saved evidence: result=%+v memoryCalls=%d generalCalls=%d err=%v", result, g.memoryCalls, g.generalCalls, err)
			}
		})
	}
}
