package client

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

func TestSavedNoteRecallWorksWithoutAnswerModelOrLibrary(t *testing.T) {
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(store.Close)
	c := NewClient(store).WithNotes(notes.NewService(store, nil, nil))
	defer c.Close()
	note, err := c.AddNote("The rollback owner is Mira.")
	if err != nil {
		t.Fatal(err)
	}
	for _, question := range []string{"Who is Mira?", "What or who is Mira?", "Who or what is Mira?", "What is Mira?", "mira?", "Tell me about Mira", "What do you remember about Mira?"} {
		t.Run(question, func(t *testing.T) {
			result, err := c.AskMemory(context.Background(), question)
			if err != nil || !result.Direct || result.General || !strings.Contains(result.Answer.Text, "The rollback owner is Mira.") || len(result.Sources) != 1 || len(result.Answer.SourceIDs) != 1 || result.Answer.SourceIDs[0] != note.SourceID || result.Sources[0].SourceID != note.SourceID || result.SentItems != 0 || result.SentBytes != 0 {
				t.Fatal("saved note was hidden behind answer-model or Library setup", result, err)
			}
		})
	}
}

func TestOrdinaryFactualQuestionsProbeSavedNotes(t *testing.T) {
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(store.Close)
	c := NewClient(store).WithNotes(notes.NewService(store, nil, nil))
	defer c.Close()
	note, err := c.AddNote("The Aurora cache listens on port 4317. The rollback owner is Mira.")
	if err != nil {
		t.Fatal(err)
	}
	for _, question := range []string{"Which port does Aurora use?", "What port does Aurora listen on?", "Who is the rollback owner?"} {
		t.Run(question, func(t *testing.T) {
			result, err := c.AskMemory(context.Background(), question)
			if err != nil || result.General || len(result.Sources) != 1 || result.Sources[0].SourceID != note.SourceID {
				t.Fatal("ordinary factual question ignored matching saved evidence", result, err)
			}
		})
	}
}

func TestAmbiguousScopedQuestionDoesNotUseUnrelatedRecentEvidence(t *testing.T) {
	f := newAskFixture(t)
	defer f.client.Close()
	result, err := f.client.AskMemory(context.Background(), "What is SQLite in Slack?")
	if !errors.Is(err, assistant.ErrAskUnavailable) || len(result.Sources) != 0 {
		t.Fatal("unmatched general question picked unrelated recent Slack content", result, err)
	}
}
