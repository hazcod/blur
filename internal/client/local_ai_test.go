package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/platform"
)

func TestLocalAISettingsRoundTripIsPrivateAndLoopbackOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "local-ai.json")
	want := LocalAISettings{Enabled: true, BackgroundSummaries: true, Endpoint: "http://127.0.0.1:11434", Model: "qwen3:4b"}
	if err := writeLocalAISettings(path, want); err != nil {
		t.Fatalf("write local AI settings: %v", err)
	}
	got, err := readLocalAISettings(path)
	if err != nil || got != want {
		t.Fatalf("read local AI settings = %#v, %v; want %#v", got, err, want)
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("settings file mode = %v, err=%v; want regular 0600", info, err)
	}
}

func TestLocalAISettingsRejectRemoteEndpointAndBadFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "local-ai.json")
	if err := writeLocalAISettings(path, LocalAISettings{Enabled: true, Endpoint: "http://example.test:11434", Model: "qwen3:4b"}); err == nil {
		t.Fatal("settings accepted a remote provider endpoint")
	}
	if err := os.WriteFile(path, []byte(`{"enabled":true,"endpoint":"http://127.0.0.1:11434","model":"x","extra":"unexpected"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readLocalAISettings(path); err == nil {
		t.Fatal("settings accepted an unknown field")
	}
}

func TestBackgroundChroniclesRequireEnabledLocalProvider(t *testing.T) {
	for _, settings := range []LocalAISettings{
		{BackgroundSummaries: true, Enabled: false, Provider: ProviderOllama, Endpoint: "http://127.0.0.1:11434", Model: "qwen3:4b"},
		{BackgroundSummaries: true, Enabled: true, Provider: ProviderOpenAICompatible, Endpoint: "https://api.openai.com/v1", Model: "gpt"},
	} {
		if validateLocalAISettings(settings) == nil {
			t.Fatalf("accepted unsafe background settings: %+v", settings)
		}
	}
	if err := validateLocalAISettings(LocalAISettings{BackgroundSummaries: true, Enabled: true, Provider: ProviderOllama, Endpoint: "http://127.0.0.1:11434", Model: "qwen3:4b"}); err != nil {
		t.Fatalf("valid local opt-in rejected: %v", err)
	}
}

func TestRemoteAskConsentRequiresEnabledCompatibleProvider(t *testing.T) {
	for _, settings := range []LocalAISettings{
		{RemoteAskEnabled: true, Enabled: false, Provider: ProviderOpenAICompatible, Endpoint: "https://api.example.test/v1", Model: "model"},
		{RemoteAskEnabled: true, Enabled: true, Provider: ProviderOllama, Endpoint: "http://127.0.0.1:11434", Model: "model"},
	} {
		if validateLocalAISettings(settings) == nil {
			t.Fatalf("accepted remote Ask consent without an enabled compatible provider: %+v", settings)
		}
	}
	if err := validateLocalAISettings(LocalAISettings{RemoteAskEnabled: true, Enabled: true, Provider: ProviderOpenAICompatible, Endpoint: "https://api.example.test/v1", Model: "model"}); err != nil {
		t.Fatalf("valid explicit remote Ask consent rejected: %v", err)
	}
}

func TestLocalAISettingsDoNotFollowSymlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local-ai.json")
	target := filepath.Join(dir, "target.json")
	if err := os.WriteFile(target, []byte("private sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := readLocalAISettings(path); err == nil {
		t.Fatal("read settings through symlink")
	}
	if err := writeLocalAISettings(path, LocalAISettings{Enabled: false}); err != nil {
		t.Fatalf("atomic replace of symlink setting: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "private sentinel" {
		t.Fatalf("symlink target changed: %q, err=%v", got, err)
	}
}

func TestLocalAIControllerPersistsExplicitEnableAndDisable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "local-ai.json")
	service := notes.NewService(nil, nil, nil)
	controller := newLocalAIController(path, service)
	settings := LocalAISettings{Enabled: true, Endpoint: "http://127.0.0.1:11434/", Model: "qwen3:4b"}
	if err := controller.Configure(context.Background(), settings); err != nil {
		t.Fatalf("enable local model: %v", err)
	}
	if !controller.Enabled() || !service.AIConfigured() || controller.Settings() != settings {
		t.Fatal("explicit model setting was not applied")
	}
	reloadedService := notes.NewService(nil, nil, nil)
	reloaded := newLocalAIController(path, reloadedService)
	if !reloaded.Enabled() || !reloadedService.AIConfigured() || reloaded.Settings() != settings {
		t.Fatal("local model setting was not restored")
	}
	settings.Enabled = false
	if err := reloaded.Configure(context.Background(), settings); err != nil {
		t.Fatalf("disable local model: %v", err)
	}
	if reloaded.Enabled() || reloadedService.AIConfigured() {
		t.Fatal("disabled model remained configured")
	}
}

func TestBackgroundSummarySettingIsExplicitAndCannotSelectRemoteProvider(t *testing.T) {
	path := filepath.Join(t.TempDir(), "local-ai.json")
	service := notes.NewService(nil, nil, nil)
	controller := newLocalAIController(path, service)
	if err := controller.SetBackgroundSummaries(context.Background(), true); err == nil {
		t.Fatal("enabled summaries before a local model was enabled")
	}
	settings := LocalAISettings{Enabled: true, Provider: ProviderOllama, Endpoint: "http://127.0.0.1:11434", Model: "qwen3:4b"}
	if err := controller.Configure(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	if err := controller.SetBackgroundSummaries(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	got, err := readLocalAISettings(path)
	if err != nil || !got.BackgroundSummaries {
		t.Fatalf("background setting did not persist: %+v %v", got, err)
	}
	if err := controller.Configure(context.Background(), LocalAISettings{Enabled: true, Provider: ProviderOpenAICompatible, Endpoint: "https://api.openai.com/v1", Model: "gpt", BackgroundSummaries: true}); err == nil {
		t.Fatal("remote provider accepted background summaries")
	}
}

func TestLocalAIControllerRejectsRemoteEndpointBeforeSaving(t *testing.T) {
	path := filepath.Join(t.TempDir(), "local-ai.json")
	service := notes.NewService(nil, nil, nil)
	controller := newLocalAIController(path, service)
	err := controller.Configure(context.Background(), LocalAISettings{Enabled: true, Endpoint: "http://example.test:11434", Model: "qwen3:4b"})
	if err == nil || service.AIConfigured() {
		t.Fatal("remote endpoint configured local AI")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("invalid settings were persisted: stat err=%v", err)
	}
}

func TestLocalAIControllerFailsClosedAndCanRepairCorruptSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "local-ai.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := notes.NewService(nil, nil, nil)
	controller := newLocalAIController(path, service)
	if !controller.NeedsRepair() || controller.Enabled() || service.AIConfigured() {
		t.Fatal("corrupt settings activated model processing")
	}
	if err := controller.Configure(context.Background(), LocalAISettings{Enabled: false}); err != nil {
		t.Fatalf("repair settings: %v", err)
	}
	if controller.NeedsRepair() {
		t.Fatal("repaired settings still marked invalid")
	}
}

func TestLocalAIControllerDoesNotActivateNotesWhenAskModelConfigFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "local-ai.json")
	settings := LocalAISettings{Enabled: true, Provider: ProviderOllama, Endpoint: "http://127.0.0.1:11434/proxy", Model: "qwen3:4b"}
	if err := writeLocalAISettings(path, settings); err != nil {
		t.Fatalf("write provider-valid settings: %v", err)
	}
	service := notes.NewService(nil, nil, nil)
	controller := newLocalAIController(path, service)
	if !controller.NeedsRepair() || controller.Enabled() || service.AIConfigured() {
		t.Fatal("partially valid startup settings activated note processing without the configured Ask model")
	}
}

// gatedSecrets lets a test hold Put in flight and fail Delete on demand.
type gatedSecrets struct {
	bootstrapSecrets
	putStarted chan struct{}
	release    chan struct{}
	deleteErr  error
}

func (s *gatedSecrets) Put(ctx context.Context, name string, value []byte) error {
	if s.putStarted != nil {
		close(s.putStarted)
		<-s.release
	}
	return s.bootstrapSecrets.Put(ctx, name, value)
}
func (s *gatedSecrets) Delete(ctx context.Context, name string) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	return s.bootstrapSecrets.Delete(ctx, name)
}

var _ platform.SecretStore = (*gatedSecrets)(nil)

func TestLocalAIConfigureDoesNotBlockReadersDuringKeychainIO(t *testing.T) {
	secrets := &gatedSecrets{bootstrapSecrets: bootstrapSecrets{values: map[string][]byte{}}, putStarted: make(chan struct{}), release: make(chan struct{})}
	controller := newLocalAIController(filepath.Join(t.TempDir(), "local-ai.json"), notes.NewService(nil, nil, nil), secrets)
	defer controller.Close()
	settings := LocalAISettings{Enabled: true, Provider: ProviderOpenAICompatible, Endpoint: "https://api.example.test/v1", Model: "m"}
	done := make(chan error, 1)
	go func() { done <- controller.Configure(context.Background(), settings, "secret") }()
	<-secrets.putStarted
	read := make(chan struct{})
	go func() {
		controller.Settings()
		controller.Enabled()
		controller.NeedsRepair()
		close(read)
	}()
	select {
	case <-read:
	case <-time.After(2 * time.Second):
		close(secrets.release)
		t.Fatal("readers blocked while Configure held a Keychain call")
	}
	close(secrets.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !controller.Settings().Enabled {
		t.Fatal("settings not applied")
	}
}

func TestLocalAIConfigureSucceedsWhenSupersededSecretDeleteFails(t *testing.T) {
	secrets := &gatedSecrets{bootstrapSecrets: bootstrapSecrets{values: map[string][]byte{}}}
	controller := newLocalAIController(filepath.Join(t.TempDir(), "local-ai.json"), notes.NewService(nil, nil, nil), secrets)
	defer controller.Close()
	settings := LocalAISettings{Enabled: true, Provider: ProviderOpenAICompatible, Endpoint: "https://api.example.test/v1", Model: "m"}
	if err := controller.Configure(context.Background(), settings, "first"); err != nil {
		t.Fatal(err)
	}
	secrets.deleteErr = errors.New("keychain denied")
	if err := controller.Configure(context.Background(), settings, "second"); err != nil {
		t.Fatalf("save failed because superseded secret could not be deleted: %v", err)
	}
	if controller.NeedsRepair() || controller.Settings().CredentialRef == "" {
		t.Fatal("controller marked for repair or lost the new credential reference")
	}
}
