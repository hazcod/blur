package preferences

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/hotkey"
)

const (
	maxSettingsBytes    = 8192
	maxExclusionEntries = 32
)

var ErrSettingsUnavailable = errors.New("preferences: settings unavailable")

type Config struct {
	CaptureEnabled           bool     `json:"capture_enabled"`
	MenuBarItemEnabled       bool     `json:"menu_bar_item_enabled"`
	SafariPageCaptureEnabled bool     `json:"safari_page_capture_enabled"`
	ClaudeMCPEnabled         bool     `json:"claude_mcp_enabled"`
	CaptureDefaultDisabled   bool     `json:"capture_default_disabled,omitempty"`
	ExcludedApps             []string `json:"excluded_apps,omitempty"`
	ExcludedDomains          []string `json:"excluded_domains,omitempty"`
	IncludedApps             []string `json:"included_apps,omitempty"`
	IncludedDomains          []string `json:"included_domains,omitempty"`
	// QuickAskHotkey is the canonical global shortcut that toggles Quick ask
	// (see internal/hotkey), or "" for off.
	QuickAskHotkey string `json:"quick_ask_hotkey,omitempty"`
}

func DefaultConfig() Config { return Config{CaptureEnabled: true, MenuBarItemEnabled: true} }

type Store struct {
	mu   sync.Mutex
	path string
}

func NewStore(path string) (*Store, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, ErrSettingsUnavailable
	}
	return &Store{path: path}, nil
}

func (s *Store) Load(ctx context.Context) (Config, error) {
	if s == nil || s.path == "" || ctx == nil {
		return Config{}, ErrSettingsUnavailable
	}
	if err := ctx.Err(); err != nil {
		return Config{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	info, err := os.Lstat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return DefaultConfig(), nil
	}
	if err != nil || !privateSettingsFile(info) || info.Size() > maxSettingsBytes {
		return Config{}, ErrSettingsUnavailable
	}
	file, err := os.Open(s.path)
	if err != nil {
		return Config{}, ErrSettingsUnavailable
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !privateSettingsFile(opened) || !os.SameFile(info, opened) || opened.Size() > maxSettingsBytes {
		return Config{}, ErrSettingsUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(file, maxSettingsBytes+1))
	if err != nil || len(data) > maxSettingsBytes {
		return Config{}, ErrSettingsUnavailable
	}
	defer clear(data)
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var config Config
	if decoder.Decode(&config) != nil {
		return Config{}, ErrSettingsUnavailable
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || len(fields) < 2 || len(fields) > 11 {
		return Config{}, ErrSettingsUnavailable
	}
	if _, ok := fields["capture_enabled"]; !ok {
		return Config{}, ErrSettingsUnavailable
	}
	if _, ok := fields["menu_bar_item_enabled"]; !ok {
		return Config{}, ErrSettingsUnavailable
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return Config{}, ErrSettingsUnavailable
	}
	config, ok := normalizeConfig(config)
	if !ok {
		return Config{}, ErrSettingsUnavailable
	}
	return config, nil
}

func (s *Store) Save(ctx context.Context, config Config) error {
	if s == nil || s.path == "" || ctx == nil {
		return ErrSettingsUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	config, ok := normalizeConfig(config)
	if !ok {
		return ErrSettingsUnavailable
	}
	data, err := json.Marshal(config)
	if err != nil || len(data) > maxSettingsBytes {
		clear(data)
		return ErrSettingsUnavailable
	}
	defer clear(data)
	temp, err := os.CreateTemp(filepath.Dir(s.path), ".preferences-*.tmp")
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
	if err := os.Rename(tempPath, s.path); err != nil {
		return ErrSettingsUnavailable
	}
	if directory, err := os.Open(filepath.Dir(s.path)); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}

func normalizeConfig(config Config) (Config, bool) {
	if len(config.ExcludedApps) > maxExclusionEntries || len(config.ExcludedDomains) > maxExclusionEntries ||
		len(config.IncludedApps) > maxExclusionEntries || len(config.IncludedDomains) > maxExclusionEntries {
		return Config{}, false
	}
	normalized, ok := capture.NormalizeExclusions(capture.Exclusions{
		Apps: config.ExcludedApps, Domains: config.ExcludedDomains,
	})
	if !ok {
		return Config{}, false
	}
	config.ExcludedApps = normalized.Apps
	config.ExcludedDomains = normalized.Domains
	allowed, ok := capture.NormalizeExclusions(capture.Exclusions{Apps: config.IncludedApps, Domains: config.IncludedDomains})
	if !ok {
		return Config{}, false
	}
	config.IncludedApps = allowed.Apps
	config.IncludedDomains = allowed.Domains
	if !hotkey.Valid(config.QuickAskHotkey) {
		// An unknown shortcut is dropped rather than failing the whole file.
		config.QuickAskHotkey = ""
	}
	return config, true
}
