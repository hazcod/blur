package client

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/profile"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

func TestExplicitPreferencesWorkOfflineAndInvalidateOldAnswers(t *testing.T) {
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(store.Close)
	c := NewClient(store)
	defer c.Close()
	ctx := context.Background()
	epoch, _ := c.BeginMemoryRead()
	askCtx, cancel := context.WithCancel(ctx)
	id, _ := c.registerAsk(cancel)
	defer c.unregisterAsk(id)
	saved, err := c.CreateProfile(ctx, profile.WritingRule, "Use short sentences.", profile.Scope{Project: "Atlas"})
	if err != nil || saved.Record.Revision != 1 || !saved.Record.Enabled {
		t.Fatal(saved, err)
	}
	if c.MemoryReadCurrent(epoch) || askCtx.Err() == nil {
		t.Fatal("old answer can survive a changed explicit rule")
	}
	if !c.MemoryReadCurrent(saved.Epoch) {
		t.Fatal("newly saved rule cannot be published")
	}
	changed := saved.Record
	changed.Text = "Use a formal tone."
	changed.Enabled = false
	updated, err := c.UpdateProfile(ctx, changed)
	if err != nil || updated.Record.Revision != 2 {
		t.Fatal(updated, err)
	}
	if _, err := c.UpdateProfile(ctx, changed); !errors.Is(err, profile.ErrConflict) {
		t.Fatal("stale correction overwrote new rule", err)
	}
	page, err := c.ProfilePage(ctx, "")
	if err != nil || len(page.Records) != 1 || page.Records[0].Enabled {
		t.Fatal(page, err)
	}
	if _, err := c.DeleteProfile(ctx, saved.Record.ID, saved.Record.Revision); !errors.Is(err, profile.ErrConflict) {
		t.Fatal("stale delete", err)
	}
	if _, err := c.DeleteProfile(ctx, updated.Record.ID, updated.Record.Revision); err != nil {
		t.Fatal(err)
	}
	page, err = c.ProfilePage(ctx, "")
	if err != nil || len(page.Records) != 0 {
		t.Fatal(page, err)
	}
}

func TestExplicitProfileQuestionsWorkWithoutAnyModel(t *testing.T) {
	ctx := context.Background()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(store.Close)
	c := NewClient(store)
	defer c.Close()
	if _, err := c.CreateProfile(ctx, profile.Fact, "My preferred language is Dutch.", profile.Scope{}); err != nil {
		t.Fatal(err)
	}
	if !c.CanAskMemory() {
		t.Fatal("offline profile questions are hidden")
	}
	result, err := c.AskMemory(ctx, "What is my preferred language?")
	if err != nil || !result.Direct || !strings.Contains(result.Answer.Text, "Dutch") || len(result.Sources) != 1 || result.Sources[0].Text != "My preferred language is Dutch." {
		t.Fatal(result, err)
	}
}

func TestExplicitPreferenceCommitRejectsDeletionWindowAndOldEpoch(t *testing.T) {
	c := NewClient(nil)
	defer c.Close()
	epoch, _ := c.BeginMemoryRead()
	c.NotifyMemoryChanging()
	wrote := false
	if _, err := c.commitExplicitMemory(epoch, func() error { wrote = true; return nil }); !errors.Is(err, ErrMemoryChanging) || wrote {
		t.Fatal("write entered deletion window", err)
	}
	c.NotifyMemoryStable()
	if _, err := c.commitExplicitMemory(epoch, func() error { wrote = true; return nil }); !errors.Is(err, ErrMemoryChanging) || wrote {
		t.Fatal("stale action resurrected deleted memory", err)
	}
}

func TestPreparedPreferenceCannotResurrectAfterDeleteAllEpoch(t *testing.T) {
	ctx := context.Background()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(store.Close)
	c := NewClient(store)
	defer c.Close()
	epoch, _ := c.BeginMemoryRead()
	c.NotifyMemoryChanging()
	c.NotifyMemoryStable()
	if _, err := c.createProfileAtEpoch(ctx, profile.Fact, "I prefer Dutch.", profile.Scope{}, epoch); !errors.Is(err, ErrMemoryChanging) {
		t.Fatal("old queued action recreated forgotten preference", err)
	}
	page, _ := c.ProfilePage(ctx, "")
	if len(page.Records) != 0 {
		t.Fatal(page)
	}
}

func TestGenerativeRequestCanUseWritingRulesWithoutBecomingInspection(t *testing.T) {
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(store.Close)
	c := NewClient(store)
	defer c.Close()
	plan := assistant.Plan{Question: "Write an email using my writing rules."}
	if _, handled, err := c.AnswerDirect(context.Background(), plan); err != nil || handled {
		t.Fatal("generation became a list of saved rules", handled, err)
	}
}

func TestRuleDisclosureDoesNotClaimNoMemoryWasUsed(t *testing.T) {
	copy := generalAnswerDisclosure(2)
	if !strings.Contains(strings.ToLower(copy), "writing rules") || strings.Contains(strings.ToLower(copy), "no saved memory") {
		t.Fatal(copy)
	}
	turn := appendQuickConversation(nil, "Explain a compiler", assistant.Result{General: true, AppliedRules: 2, Answer: assistant.Answer{Text: "A draft."}})
	if len(turn) != 1 || !strings.Contains(quickTurnMeta(turn[0]), "writing rules") {
		t.Fatal(turn)
	}
}

func TestExplicitPreferenceCitationNamesUserProvenance(t *testing.T) {
	caption := askEvidenceCaption(assistant.Evidence{AppID: "blur.profile", AppName: "Explicit preference", Origin: assistant.OriginUserFact})
	if strings.Contains(caption, "blur.profile") || !strings.Contains(caption, "Saved by you") {
		t.Fatal(caption)
	}
}

func TestLibraryCitationExplainsCorrectionAndInferenceProvenance(t *testing.T) {
	correction := askEvidenceCaption(assistant.Evidence{AppName: "Library", Origin: assistant.OriginUserCorrection})
	inferred := askEvidenceCaption(assistant.Evidence{AppName: "Library", Origin: assistant.OriginInferred})
	if correction != "Your correction in Library." || inferred != "Inferred Library memory. Review if it looks wrong." {
		t.Fatalf("Library provenance was unclear: correction=%q inferred=%q", correction, inferred)
	}
}

func TestQuickExplicitFactsAndRulesRemainDistinctFromNotesAndAsks(t *testing.T) {
	for _, test := range []struct {
		input string
		want  quickInputKind
		body  string
	}{
		{"remember that I prefer Dutch.", quickInputFact, "I prefer Dutch."},
		{"writing rule: Be direct", quickInputRule, "Be direct"},
		{"note: writing rule: Be direct", quickInputNote, "writing rule: Be direct"},
		{"Do you remember that I prefer Dutch?", quickInputAsk, "Do you remember that I prefer Dutch?"},
		{"remember that article I was reading", quickInputAsk, "remember that article I was reading"},
	} {
		kind, body := classifyQuickInput(test.input)
		if kind != test.want || body != test.body {
			t.Fatalf("%q => %v %q", test.input, kind, body)
		}
	}
}
