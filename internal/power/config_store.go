package power

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
)

const maxConfigBytes = 1024

var ErrSettingsUnavailable = errors.New("power: settings unavailable")

type Controller struct {
	mu     sync.Mutex
	path   string
	system SystemStateSource
}

func NewController(path string) (*Controller, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, ErrSettingsUnavailable
	}
	return &Controller{path: path, system: NewSystemStateSource()}, nil
}

// Config reads the local non-secret policy, defaulting without writing when no
// preference exists yet.
func (c *Controller) Config(ctx context.Context) (Config, error) {
	if c == nil || c.path == "" || ctx == nil {
		return Config{}, ErrSettingsUnavailable
	}
	if err := ctx.Err(); err != nil {
		return Config{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return readConfig(c.path)
}

func (c *Controller) Configure(ctx context.Context, config Config) error {
	if c == nil || c.path == "" || ctx == nil {
		return ErrSettingsUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := Resolve(config, SystemState{}); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return writeConfig(ctx, c.path, config)
}

// Effective resolves the selected policy against a fresh OS signal. An
// unavailable signal returns Balanced under Follow System alongside the
// generic source error; callers can still use the conservative result.
func (c *Controller) Effective(ctx context.Context) (Effective, error) {
	if c == nil || c.system == nil {
		return Effective{}, ErrSettingsUnavailable
	}
	config, err := c.Config(ctx)
	if err != nil {
		return Effective{}, err
	}
	system, stateErr := c.system.ReadSystemState(ctx)
	if stateErr != nil && ctx.Err() != nil {
		return Effective{}, ctx.Err()
	}
	effective, resolveErr := Resolve(config, system)
	if resolveErr != nil {
		return Effective{}, resolveErr
	}
	if stateErr != nil {
		return effective, ErrSystemStateUnavailable
	}
	return effective, nil
}

func readConfig(path string) (Config, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return DefaultConfig(), nil
	}
	if err != nil || !privateConfigFile(info) || info.Size() > maxConfigBytes {
		return Config{}, ErrSettingsUnavailable
	}
	file, err := os.Open(path)
	if err != nil {
		return Config{}, ErrSettingsUnavailable
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !privateConfigFile(opened) || !os.SameFile(info, opened) || opened.Size() > maxConfigBytes {
		return Config{}, ErrSettingsUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(file, maxConfigBytes+1))
	if err != nil || len(data) > maxConfigBytes {
		clear(data)
		return Config{}, ErrSettingsUnavailable
	}
	defer clear(data)
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var config Config
	if decoder.Decode(&config) != nil {
		return Config{}, ErrSettingsUnavailable
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return Config{}, ErrSettingsUnavailable
	}
	if _, err := Resolve(config, SystemState{}); err != nil {
		return Config{}, ErrSettingsUnavailable
	}
	return config, nil
}

func writeConfig(ctx context.Context, path string, config Config) error {
	if ctx == nil || ctx.Err() != nil || path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrSettingsUnavailable
	}
	data, err := json.Marshal(config)
	if err != nil || len(data) > maxConfigBytes {
		clear(data)
		return ErrInvalidConfig
	}
	defer clear(data)
	temp, err := os.CreateTemp(filepath.Dir(path), ".power-*.tmp")
	if err != nil {
		return ErrSettingsUnavailable
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return ErrSettingsUnavailable
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return ErrSettingsUnavailable
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return ErrSettingsUnavailable
	}
	if err := temp.Close(); err != nil {
		return ErrSettingsUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return ErrSettingsUnavailable
	}
	if directory, err := os.Open(filepath.Dir(path)); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}
