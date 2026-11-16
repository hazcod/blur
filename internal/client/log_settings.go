package client

import (
	"context"
	"time"

	"github.com/hazcod/blur/internal/applog"
	"github.com/hazcod/blur/internal/preferences"
)

// applySavedLogLevel runs only while attaching the application preferences at
// startup. Rendering Settings never reads a file or changes the global logger.
func (c *Client) applySavedLogLevel() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if saved, err := c.preferences.Load(ctx); err == nil {
		_ = applog.SetLevel(saved.LogLevel)
	}
}

// ConfigureLogLevel atomically edits only the log field of the latest saved
// preferences. Commit and runtime application share the full-preference writer
// lock, so a later successful configuration cannot be applied out of order.
func (c *Client) ConfigureLogLevel(ctx context.Context, level string) (preferences.Config, error) {
	if c == nil || c.preferences == nil || ctx == nil {
		return preferences.Config{}, ErrNotConfigured
	}
	switch level {
	case "debug", "info", "warn", "error":
	default:
		return preferences.Config{}, ErrInvalidRequest
	}
	operation, done, err := c.beginOperation(ctx, 8*time.Second)
	if err != nil {
		return preferences.Config{}, err
	}
	defer done()
	c.preferencesConfigMu.Lock()
	defer c.preferencesConfigMu.Unlock()
	saved, err := c.preferences.Update(operation, func(config preferences.Config) (preferences.Config, error) {
		config.LogLevel = level
		return config, nil
	})
	if err != nil {
		return preferences.Config{}, err
	}
	_ = applog.SetLevel(saved.LogLevel) // Store validation admits exactly these levels.
	return saved, nil
}
