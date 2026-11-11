package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/hazcod/blur/internal/notes"
)

const maxLocalAISettingsBytes = 2048

var errLocalAISettings = errors.New("macos client: invalid local AI settings")

// LocalAISettings contains only non-secret routing preferences. Ollama is
// constrained to loopback; compatible remote endpoints require HTTPS and a
// separate persisted consent before Ask evidence can be sent.
type LocalAISettings struct {
	Enabled             bool   `json:"enabled"`
	BackgroundSummaries bool   `json:"background_summaries,omitempty"`
	RemoteAskEnabled    bool   `json:"remote_ask_enabled,omitempty"`
	Provider            string `json:"provider,omitempty"`
	Endpoint            string `json:"endpoint,omitempty"`
	Model               string `json:"model,omitempty"`
	CredentialRef       string `json:"credential_ref,omitempty"`
}

const (
	ProviderOllama             = "ollama"
	ProviderOpenAICompatible   = "openai-compatible"
	openAICompatibleSecretName = "ai-provider-"
)

func validateLocalAISettings(settings LocalAISettings) error {
	if !settings.Enabled && !settings.BackgroundSummaries && !settings.RemoteAskEnabled && settings.Endpoint == "" && settings.Model == "" && settings.Provider == "" && settings.CredentialRef == "" {
		return nil
	}
	if settings.RemoteAskEnabled && (!settings.Enabled || settings.Provider != ProviderOpenAICompatible) {
		return errLocalAISettings
	}
	if settings.BackgroundSummaries && (!settings.Enabled || settings.Provider != "" && settings.Provider != ProviderOllama) {
		return errLocalAISettings
	}
	provider := settings.Provider
	if provider == "" {
		provider = ProviderOllama // Backward compatibility for existing Ollama preferences.
	}
	if settings.Endpoint == "" || settings.Model == "" || len(settings.Model) > 128 || strings.ContainsAny(settings.Model, "\x00\r\n") {
		return errLocalAISettings
	}
	if settings.CredentialRef != "" && (provider != ProviderOpenAICompatible || !validCredentialRef(settings.CredentialRef)) {
		return errLocalAISettings
	}
	switch provider {
	case ProviderOllama:
		if _, err := notes.NewOllamaExtractor(settings.Endpoint, settings.Model); err != nil {
			return errLocalAISettings
		}
	case ProviderOpenAICompatible:
		if _, err := notes.NewOpenAICompatibleExtractor(settings.Endpoint, settings.Model, "validation-only"); err != nil {
			return errLocalAISettings
		}
	default:
		return errLocalAISettings
	}
	return nil
}

func validCredentialRef(ref string) bool {
	if len(ref) != 32 {
		return false
	}
	for i := range ref {
		if !(ref[i] >= '0' && ref[i] <= '9' || ref[i] >= 'a' && ref[i] <= 'f') {
			return false
		}
	}
	return true
}

func readLocalAISettings(path string) (LocalAISettings, error) {
	if path == "" {
		return LocalAISettings{}, errLocalAISettings
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return LocalAISettings{}, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > maxLocalAISettingsBytes {
		return LocalAISettings{}, errLocalAISettings
	}
	file, err := os.Open(path)
	if err != nil {
		return LocalAISettings{}, errLocalAISettings
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) || opened.Size() > maxLocalAISettingsBytes {
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
	temp, err := os.CreateTemp(directory, ".local-ai-*.tmp")
	if err != nil {
		return errLocalAISettings
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return errLocalAISettings
	}
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
