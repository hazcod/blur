package client

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hazcod/blur/internal/notes"
)

func TestCloudSettingsRequireBoundAccountAndIndependentConsent(t *testing.T) {
	chat := LocalAISettings{Enabled: true, Provider: ProviderChatGPT, AccountID: strings.Repeat("a", 64), Model: "entitled-model"}
	if err := validateLocalAISettings(chat); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*LocalAISettings){
		func(s *LocalAISettings) { s.AccountID = "" },
		func(s *LocalAISettings) { s.CredentialRef = strings.Repeat("b", 32) },
		func(s *LocalAISettings) { s.Endpoint = "https://other.example" },
		func(s *LocalAISettings) { s.BackgroundSummaries = true },
		func(s *LocalAISettings) { s.AutomaticLibrary = true },
	} {
		s := chat
		change(&s)
		if validateLocalAISettings(s) == nil {
			t.Fatalf("unsafe cloud settings accepted: %+v", s)
		}
	}
	claude := LocalAISettings{Enabled: true, Provider: ProviderClaude, Model: "available-claude", CredentialRef: strings.Repeat("b", 32)}
	if err := validateLocalAISettings(claude); err != nil {
		t.Fatal(err)
	}
	for _, base := range []LocalAISettings{chat, claude} {
		base.RemoteAskEnabled, base.RemoteNotesEnabled = true, true
		if err := validateLocalAISettings(base); err != nil {
			t.Fatal("independent explicit cloud permissions refused", err)
		}
		base.Enabled = false
		if validateLocalAISettings(base) == nil {
			t.Fatal("disabled provider retained active remote permission")
		}
	}
}

func TestCloudSelectionKeepsSavedEvidenceAndNotesLocalUntilPermission(t *testing.T) {
	c, _, _ := todayTaskClient(t)
	service := c.notes.(*notes.Service)
	controller := newLocalAIController(filepath.Join(t.TempDir(), "ai.json"), service, &bootstrapSecrets{values: map[string][]byte{}})
	c.WithLocalAI(controller)
	defer controller.Close()
	settings := LocalAISettings{Enabled: true, Provider: ProviderChatGPT, AccountID: strings.Repeat("a", 64), Model: "entitled-model"}
	ctx := context.Background()
	unsafe := settings
	unsafe.RemoteAskEnabled = true
	if controller.Configure(ctx, unsafe) == nil {
		t.Fatal("first selection granted remote evidence permission")
	}
	unsafe.RemoteAskEnabled, unsafe.RemoteNotesEnabled = false, true
	if controller.Configure(ctx, unsafe) == nil {
		t.Fatal("first selection granted remote note processing")
	}
	if err := controller.Configure(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if !controller.Enabled() || service.AIConfigured() {
		t.Fatal("cloud choice either disabled general answers or enabled note transmission")
	}
	note, err := c.AddNote("The rollback owner is Mira.")
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.AskMemory(ctx, "Who is the rollback owner?")
	if err != nil || !result.Direct || result.SentItems != 0 || len(result.Sources) != 1 || result.Sources[0].SourceID != note.SourceID {
		t.Fatalf("unconsented saved memory attempted remote inference: %+v err=%v", result, err)
	}
	settings.RemoteAskEnabled, settings.RemoteNotesEnabled = true, true
	if err := controller.Configure(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if !service.AIConfigured() {
		t.Fatal("separate explicit note permission was not applied")
	}
	settings.AccountID = strings.Repeat("b", 64)
	if err := controller.Configure(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if saved := controller.Settings(); saved.RemoteAskEnabled || saved.RemoteNotesEnabled || service.AIConfigured() {
		t.Fatal("remote permissions followed another account", saved)
	}
}

func TestCloudCredentialReplacementClearsBothRemotePermissions(t *testing.T) {
	service := notes.NewService(nil, nil, nil)
	controller := newLocalAIController(filepath.Join(t.TempDir(), "ai.json"), service, &bootstrapSecrets{values: map[string][]byte{}})
	defer controller.Close()
	ctx := context.Background()
	settings := LocalAISettings{Enabled: true, Provider: ProviderClaude, Model: "available-claude"}
	if err := controller.Configure(ctx, settings, "first-synthetic-key"); err != nil {
		t.Fatal(err)
	}
	settings = controller.Settings()
	settings.RemoteAskEnabled, settings.RemoteNotesEnabled = true, true
	if err := controller.Configure(ctx, settings); err != nil {
		t.Fatal(err)
	}
	previousRef := controller.Settings().CredentialRef
	if err := controller.Configure(ctx, settings, "another-synthetic-key"); err != nil {
		t.Fatal(err)
	}
	if got := controller.Settings(); got.RemoteAskEnabled || got.RemoteNotesEnabled || got.CredentialRef == previousRef || service.AIConfigured() {
		t.Fatalf("permissions followed unverified replacement credential: %+v", got)
	}
}
