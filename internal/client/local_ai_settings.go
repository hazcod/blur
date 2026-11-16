package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/semantic"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

const maxLocalAISettingsBytes = 2048

var errLocalAISettings = errors.New("macos client: invalid local AI settings")

// LocalAISettings contains only non-secret routing preferences. Apple uses the
// OS on-device model without an endpoint or key. Ollama is constrained to loopback; compatible remote endpoints require HTTPS and a
// separate persisted consent before Ask evidence can be sent.
type LocalAISettings struct {
	Enabled             bool   `json:"enabled"`
	BackgroundSummaries bool   `json:"background_summaries"`
	AutomaticLibrary    bool   `json:"automatic_library"`
	RemoteAskEnabled    bool   `json:"remote_ask_enabled,omitempty"`
	RemoteNotesEnabled  bool   `json:"remote_notes_enabled,omitempty"`
	Provider            string `json:"provider,omitempty"`
	AccountID           string `json:"account_id,omitempty"`
	WorkspaceID         string `json:"workspace_id,omitempty"`
	Endpoint            string `json:"endpoint,omitempty"`
	Model               string `json:"model,omitempty"`
	CredentialRef       string `json:"credential_ref,omitempty"`
}

const (
	ProviderOllama             = "ollama"
	ProviderApple              = "apple-foundation-models"
	ProviderChatGPT            = "chatgpt"
	ProviderClaude             = "anthropic"
	AppleSystemModel           = "system-language-model"
	ProviderOpenAICompatible   = "openai-compatible"
	openAICompatibleSecretName = "ai-provider-"
)

func validateLocalAISettings(settings LocalAISettings) error {
	if !settings.Enabled && !settings.BackgroundSummaries && !settings.AutomaticLibrary && !settings.RemoteAskEnabled && !settings.RemoteNotesEnabled && settings.Endpoint == "" && settings.Model == "" && settings.Provider == "" && settings.CredentialRef == "" && settings.AccountID == "" && settings.WorkspaceID == "" {
		return nil
	}
	if (settings.RemoteAskEnabled || settings.RemoteNotesEnabled) && (!settings.Enabled || !isRemoteCompletionProvider(settings.Provider)) {
		return errLocalAISettings
	}
	if settings.BackgroundSummaries && (!settings.Enabled || !isLocalCompletionProvider(effectiveProvider(settings))) {
		return errLocalAISettings
	}
	if settings.AutomaticLibrary && (!settings.Enabled || !isLocalCompletionProvider(effectiveProvider(settings))) {
		return errLocalAISettings
	}
	provider := settings.Provider
	if provider == "" {
		provider = ProviderOllama // Backward compatibility for existing Ollama preferences.
	}
	if settings.Model == "" || len(settings.Model) > 128 || strings.ContainsAny(settings.Model, "\x00\r\n") {
		return errLocalAISettings
	}
	if settings.CredentialRef != "" && (provider != ProviderOpenAICompatible && provider != ProviderClaude || !validCredentialRef(settings.CredentialRef)) {
		return errLocalAISettings
	}
	if settings.AccountID != "" && (provider != ProviderChatGPT || !semantic.ValidID(settings.AccountID)) {
		return errLocalAISettings
	}
	if settings.WorkspaceID != "" && (provider != ProviderClaude || len(settings.WorkspaceID) > 128 || strings.TrimSpace(settings.WorkspaceID) != settings.WorkspaceID || strings.ContainsAny(settings.WorkspaceID, "\x00\r\n")) {
		return errLocalAISettings
	}
	switch provider {
	case ProviderApple:
		if settings.Endpoint != "" || settings.Model != AppleSystemModel || settings.CredentialRef != "" {
			return errLocalAISettings
		}
	case ProviderOllama:
		if _, err := notes.NewOllamaExtractor(settings.Endpoint, settings.Model); err != nil {
			return errLocalAISettings
		}
	case ProviderOpenAICompatible:
		if _, err := notes.NewOpenAICompatibleExtractor(settings.Endpoint, settings.Model, "validation-only"); err != nil {
			return errLocalAISettings
		}
	case ProviderChatGPT:
		if settings.Endpoint != "" || !semantic.ValidID(settings.AccountID) || settings.CredentialRef != "" || settings.WorkspaceID != "" {
			return errLocalAISettings
		}
	case ProviderClaude:
		if settings.Endpoint != "" || settings.AccountID != "" {
			return errLocalAISettings
		}
	default:
		return errLocalAISettings
	}
	return nil
}

func isRemoteCompletionProvider(provider string) bool {
	return provider == ProviderOpenAICompatible || provider == ProviderChatGPT || provider == ProviderClaude
}

func sameRemoteAITarget(a, b LocalAISettings) bool {
	return a.Enabled && a.Provider == b.Provider && a.Endpoint == b.Endpoint && a.Model == b.Model && a.AccountID == b.AccountID && a.WorkspaceID == b.WorkspaceID && isRemoteCompletionProvider(b.Provider)
}

func validCredentialRef(ref string) bool {
	if len(ref) != 32 {
		return false
	}
	for i := range ref {
		if (ref[i] < '0' || ref[i] > '9') && (ref[i] < 'a' || ref[i] > 'f') {
			return false
		}
	}
	return true
}

func defaultLocalAISettings() LocalAISettings {
	if runtime.GOOS == "darwin" {
		return LocalAISettings{
			Enabled: true, Provider: ProviderApple, Model: AppleSystemModel,
			AutomaticLibrary: true, BackgroundSummaries: true,
		}
	}
	return LocalAISettings{}
}

func readLocalAISettings(path string) (LocalAISettings, error) {
	if path == "" {
		return LocalAISettings{}, errLocalAISettings
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return defaultLocalAISettings(), nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxLocalAISettingsBytes {
		return LocalAISettings{}, errLocalAISettings
	}
	file, err := os.Open(path)
	if err != nil {
		return LocalAISettings{}, errLocalAISettings
	}
	defer func(close func() error) { _ = close() }(file.Close)
	opened, err := file.Stat()
	if err != nil || bolt.ValidatePrivateFile(file) != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) || opened.Size() > maxLocalAISettingsBytes {
		return LocalAISettings{}, errLocalAISettings
	}
	data, err := io.ReadAll(io.LimitReader(file, maxLocalAISettingsBytes+1))
	if err != nil || len(data) > maxLocalAISettingsBytes {
		clear(data)
		return LocalAISettings{}, errLocalAISettings
	}
	defer clear(data)
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var settings LocalAISettings
	if decoder.Decode(&settings) != nil {
		return LocalAISettings{}, errLocalAISettings
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF || validateLocalAISettings(settings) != nil {
		return LocalAISettings{}, errLocalAISettings
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || fields == nil {
		return LocalAISettings{}, errLocalAISettings
	}
	if len(fields) == 0 {
		// An empty object has no saved choice. Any populated settings object,
		// including {"enabled":false}, retains its existing provider and flags.
		return defaultLocalAISettings(), nil
	}
	return settings, nil
}

func writeLocalAISettings(path string, settings LocalAISettings) error {
	if path == "" || validateLocalAISettings(settings) != nil {
		return errLocalAISettings
	}
	data, err := json.Marshal(settings)
	if err != nil || len(data) > maxLocalAISettingsBytes {
		clear(data)
		return errLocalAISettings
	}
	defer clear(data)
	directory := filepath.Dir(path)
	temp, tempPath, err := bolt.CreatePrivateTempFile(directory, ".local-ai-")
	if err != nil {
		return errLocalAISettings
	}
	defer func() { _ = os.Remove(tempPath) }()
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return errLocalAISettings
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return errLocalAISettings
	}
	if err := temp.Close(); err != nil {
		return errLocalAISettings
	}
	if err := os.Rename(tempPath, path); err != nil {
		return errLocalAISettings
	}
	if dir, err := os.Open(directory); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}
