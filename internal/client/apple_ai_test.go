package client

import (
	"context"
	"errors"
	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/model"
	"github.com/hazcod/blur/internal/notes"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAppleSettingsValidateWithoutEndpointOrRuntime(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		settings := LocalAISettings{Enabled: enabled, Provider: "apple-foundation-models", Model: "system-language-model"}
		if err := validateLocalAISettings(settings); err != nil {
			t.Fatalf("valid Apple preferences require an endpoint or available runtime: %v", err)
		}
		path := filepath.Join(t.TempDir(), "local-ai.json")
		if err := writeLocalAISettings(path, settings); err != nil {
			t.Fatal(err)
		}
		got, err := readLocalAISettings(path)
		if err != nil || got != settings {
			t.Fatalf("Apple preferences did not round trip: %#v, %v", got, err)
		}
	}
}

func TestAppleSettingsPreserveExplicitLocalBackgroundOptIns(t *testing.T) {
	settings := LocalAISettings{Enabled: true, Provider: "apple-foundation-models", Model: "system-language-model", BackgroundSummaries: true, AutomaticLibrary: true}
	if err := validateLocalAISettings(settings); err != nil {
		t.Fatalf("Apple cannot use local background opt-ins: %v", err)
	}
	settings.Enabled = false
	if err := validateLocalAISettings(settings); err == nil {
		t.Fatal("disabled AI accepted active background jobs")
	}
}

func TestAppleSettingsRejectNetworkAndCredentialRouting(t *testing.T) {
	baseline := LocalAISettings{Enabled: true, Provider: "apple-foundation-models", Model: "system-language-model"}
	for _, change := range []func(*LocalAISettings){
		func(s *LocalAISettings) { s.Endpoint = "http://127.0.0.1:11434" },
		func(s *LocalAISettings) { s.CredentialRef = strings.Repeat("a", 32) },
		func(s *LocalAISettings) { s.RemoteAskEnabled = true },
		func(s *LocalAISettings) { s.Model = "custom-model" },
		func(s *LocalAISettings) { s.Model = "" },
	} {
		settings := baseline
		change(&settings)
		if err := validateLocalAISettings(settings); err == nil {
			t.Fatalf("unsafe Apple routing accepted: %#v", settings)
		}
	}
}

func TestAppleModelSaveStateNeedsNoEndpoint(t *testing.T) {
	saved := LocalAISettings{Enabled: true, Provider: "apple-foundation-models", Model: "system-language-model"}
	d := &desktop{localAIEnabled: true, modelProvider: saved.Provider, modelName: saved.Model}
	if label, ready := d.localAIModelSaveState(saved); label != "Saved" || ready {
		t.Fatal(label, ready)
	}
	d.aiNeedsRepair = true
	if label, ready := d.localAIModelSaveState(saved); label != "Repair settings" || !ready {
		t.Fatal("Apple settings repair requires an endpoint", label, ready)
	}
	d.loading, d.status = true, "Saving AI settings…"
	if label, ready := d.localAIModelSaveState(saved); label != "Saving…" || ready {
		t.Fatal(label, ready)
	}
}

func TestAppleDisclosuresNameOSModelWithoutServer(t *testing.T) {
	settings := LocalAISettings{Enabled: true, Provider: "apple-foundation-models", Model: "system-language-model"}
	for _, copy := range []string{askProcessingDisclosure(settings), localAIDataFlowCopy(true, settings.Provider)} {
		if !strings.Contains(copy, "Apple") || !strings.Contains(copy, "on-device") || strings.Contains(copy, "Ollama") || strings.Contains(copy, "loopback") {
			t.Fatalf("Apple data flow is misleading: %q", copy)
		}
	}
}

func TestAppleProgressAndFailureCopyNamesSelectedProvider(t *testing.T) {
	settings := LocalAISettings{Enabled: true, Provider: "apple-foundation-models", Model: "system-language-model"}
	for _, question := range []string{"Who is Mira?", "What is SQLite?"} {
		copy := askProgressLabel(settings, question)
		if !strings.Contains(copy, "Apple") || strings.Contains(copy, "Ollama") {
			t.Fatalf("wrong provider progress: %q", copy)
		}
	}
	for _, err := range []error{context.DeadlineExceeded, assistant.ErrAnswer, errors.New("private details")} {
		copy := askErrorMessage(err, settings.Provider)
		if strings.Contains(copy, "Ollama") || !strings.Contains(copy, "Apple") || strings.Contains(copy, "private details") {
			t.Fatalf("wrong provider recovery: %q", copy)
		}
	}
}

type appleAskStub struct {
	integrationTokenCounter
	integrationAnswerGenerator
}

func (*appleAskStub) CloseIdleConnections() {}

func TestAppleControllerAdmitsLocalAskAndLibrarySuggestions(t *testing.T) {
	extractor, err := notes.NewOllamaExtractor("http://127.0.0.1:11434", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	service := notes.NewService(nil, extractor, nil)
	controller := &localAIController{service: service, settings: LocalAISettings{Enabled: true, Provider: "apple-foundation-models", Model: "system-language-model"}, askModel: &appleAskStub{}}
	c := NewClient(&testData{}).WithLocalAI(controller)
	defer c.Close()
	if _, ok := controller.askService(c); !ok {
		t.Fatal("Apple local provider cannot answer")
	}
	if !controller.canSuggestKnowledge() {
		t.Fatal("Apple local provider cannot suggest Library memories")
	}
}

func TestAppleBackgroundSummaryOptInCanBeSavedWithoutEndpoint(t *testing.T) {
	controller := &localAIController{path: filepath.Join(t.TempDir(), "ai.json"), settings: LocalAISettings{Enabled: true, Provider: "apple-foundation-models", Model: "system-language-model"}}
	if err := controller.SetBackgroundSummaries(context.Background(), true); err != nil {
		t.Fatal("Apple cannot opt into background summaries", err)
	}
	if !controller.Settings().BackgroundSummaries {
		t.Fatal("summary opt-in was lost")
	}
}

func TestAppleConfigurationRejectsProvidedCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ai.json")
	if err := writeLocalAISettings(path, LocalAISettings{}); err != nil {
		t.Fatal(err)
	}
	controller := newLocalAIController(path, notes.NewService(nil, nil, nil))
	defer controller.Close()
	settings := LocalAISettings{Provider: "apple-foundation-models", Model: "system-language-model"}
	if err := controller.Configure(context.Background(), settings, "unnecessary-secret"); err == nil {
		t.Fatal("Apple accepted an account credential")
	}
	if controller.Settings() != (LocalAISettings{}) {
		t.Fatal("rejected credential changed settings")
	}
}

func TestAppleSelectionClearsServerCredentialAndRemoteConsent(t *testing.T) {
	d := &desktop{modelProvider: ProviderOpenAICompatible, modelEndpoint: "https://example.test/v1", modelName: "remote-model", modelCredential: "secret", remoteAskEnabled: true}
	d.selectModelProvider("apple-foundation-models")
	if d.modelProvider != "apple-foundation-models" || d.modelEndpoint != "" || d.modelName != "system-language-model" || d.modelCredential != "" || d.remoteAskEnabled {
		t.Fatal("Apple selection retained network routing", d.modelProvider, d.modelEndpoint, d.modelName)
	}
	// Selecting the active provider must preserve the current draft.
	d.selectModelProvider("apple-foundation-models")
	if d.modelEndpoint != "" || d.modelName != "system-language-model" {
		t.Fatal("active selection changed Apple configuration")
	}
}

func TestAppleAvailabilityExplainsRecoveryWithoutLeakingDetails(t *testing.T) {
	for _, test := range []struct {
		available bool
		reason    string
		err       error
		want      string
	}{
		{available: true, want: "ready"},
		{reason: "apple_intelligence_not_enabled", want: "System Settings"},
		{reason: "apple_intelligence_not_enabled", err: model.ErrAppleUnavailable, want: "Turn on Apple Intelligence"},
		{reason: "model_not_ready", want: "readying"},
		{reason: "unsupported_os", want: "macOS 26"},
		{reason: "device_not_eligible", want: "supported Mac"},
		{err: errors.New("private-path-and-prompt"), want: "unavailable"},
	} {
		copy := appleAvailabilityCopy(model.AppleStatus{Available: test.available, Reason: test.reason}, test.err)
		if !strings.Contains(copy, test.want) || strings.Contains(copy, "private-path-and-prompt") || strings.Contains(copy, "Ollama") {
			t.Fatalf("Apple status %q: %q", test.reason, copy)
		}
	}
}

func TestAppleAutomaticLibraryNoteDisclosureNamesSelectedModel(t *testing.T) {
	copy := quickNotePrivacyHint(true, "apple-foundation-models", false, true)
	if !strings.Contains(copy, "Apple") || strings.Contains(copy, "Ollama") {
		t.Fatalf("wrong Library provider: %q", copy)
	}
}

func TestDisablingSavedAppleModelIgnoresUnsavedRemoteCredential(t *testing.T) {
	service := notes.NewService(nil, nil, nil)
	saved := LocalAISettings{Enabled: true, Provider: "apple-foundation-models", Model: "system-language-model"}
	controller := &localAIController{path: filepath.Join(t.TempDir(), "ai.json"), service: service, settings: saved}
	c := NewClient(&testData{}).WithLocalAI(controller)
	defer c.Close()
	app := &pausedAskApp{posted: make(chan func(), 8)}
	d := &desktop{app: app, client: c, localAIEnabled: true, modelProvider: ProviderOpenAICompatible, modelEndpoint: "https://unsaved.example.test/v1", modelName: "unsaved", modelCredential: "unsaved-secret"}
	d.configureLocalAI(false)
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for d.loading {
		select {
		case publish := <-app.posted:
			publish()
		case <-deadline.C:
			t.Fatal("disable did not complete")
		}
	}
	got := controller.Settings()
	if got.Enabled || got.Provider != saved.Provider || got.Model != saved.Model || got.Endpoint != "" {
		t.Fatalf("unsaved remote draft prevented disabling saved Apple model: %#v; %q", got, d.status)
	}
}
