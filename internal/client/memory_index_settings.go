package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/hazcod/blur/internal/semantic"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

var errMemoryIndexSettings = errors.New("client: invalid memory search settings")

// MemoryIndexSettings selects only a local embedding provider. These settings
// contain no source text, credentials, vectors, or search history.
type MemoryIndexSettings struct {
	Enabled    bool   `json:"enabled"`
	Endpoint   string `json:"endpoint"`
	Model      string `json:"model"`
	Dimensions int    `json:"dimensions"`
}

func DefaultMemoryIndexSettings() MemoryIndexSettings {
	// Compatibility defaults for explicitly constructed legacy controllers.
	// Normal startup does not construct this separate embedding service.
	return MemoryIndexSettings{Enabled: true, Endpoint: "http://127.0.0.1:11434", Model: semantic.DefaultEmbeddingModel, Dimensions: semantic.DefaultDimensions}
}

func validateMemoryIndexSettings(settings MemoryIndexSettings) error {
	embedder, err := semantic.NewOllamaEmbedder(settings.Endpoint, settings.Model, settings.Dimensions)
	if err != nil {
		return errMemoryIndexSettings
	}
	embedder.CloseIdleConnections()
	return nil
}

func readMemoryIndexSettings(path string) (MemoryIndexSettings, error) {
	if path == "" {
		return MemoryIndexSettings{}, errMemoryIndexSettings
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return DefaultMemoryIndexSettings(), nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxLocalAISettingsBytes {
		return MemoryIndexSettings{}, errMemoryIndexSettings
	}
	file, err := os.Open(path)
	if err != nil {
		return MemoryIndexSettings{}, errMemoryIndexSettings
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil || bolt.ValidatePrivateFile(file) != nil || !os.SameFile(info, opened) || opened.Size() > maxLocalAISettingsBytes {
		return MemoryIndexSettings{}, errMemoryIndexSettings
	}
	data, err := io.ReadAll(io.LimitReader(file, maxLocalAISettingsBytes+1))
	if err != nil || len(data) > maxLocalAISettingsBytes {
		clear(data)
		return MemoryIndexSettings{}, errMemoryIndexSettings
	}
	defer clear(data)
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var settings MemoryIndexSettings
	if decoder.Decode(&settings) != nil {
		return MemoryIndexSettings{}, errMemoryIndexSettings
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF || validateMemoryIndexSettings(settings) != nil {
		return MemoryIndexSettings{}, errMemoryIndexSettings
	}
	return settings, nil
}

func writeMemoryIndexSettings(path string, settings MemoryIndexSettings) error {
	if path == "" || validateMemoryIndexSettings(settings) != nil {
		return errMemoryIndexSettings
	}
	data, err := json.Marshal(settings)
	if err != nil {
		return errMemoryIndexSettings
	}
	defer clear(data)
	file, tempPath, err := bolt.CreatePrivateTempFile(filepath.Dir(path), ".memory-index-")
	if err != nil {
		return errMemoryIndexSettings
	}
	defer func() { _ = os.Remove(tempPath) }()
	defer func() { _ = file.Close() }()
	if _, err := file.Write(data); err != nil {
		return errMemoryIndexSettings
	}
	if file.Sync() != nil || file.Close() != nil {
		return errMemoryIndexSettings
	}
	if os.Rename(tempPath, path) != nil {
		return errMemoryIndexSettings
	}
	if directory, err := os.Open(filepath.Dir(path)); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}
