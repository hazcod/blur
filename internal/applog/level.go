// Package applog controls the minimum level of Blur's application diagnostics.
// It does not change crash output, native framework messages, or log contents.
package applog

import (
	"errors"
	"log/slog"
)

var ErrLevel = errors.New("logging: invalid level")

// The zero value is Info. All installed application handlers share this one
// atomic level, including loggers captured by long-lived background services.
var minimum slog.LevelVar

func Level() slog.Leveler { return &minimum }

func CurrentLevel() string {
	switch minimum.Level() {
	case slog.LevelDebug:
		return "debug"
	case slog.LevelWarn:
		return "warn"
	case slog.LevelError:
		return "error"
	default:
		return "info"
	}
}

// SetLevel updates existing handlers immediately. Empty is the legacy default;
// unknown input leaves the active level unchanged and is never echoed in errors.
func SetLevel(value string) error {
	var level slog.Level
	switch value {
	case "debug":
		level = slog.LevelDebug
	case "", "info":
		level = slog.LevelInfo
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		return ErrLevel
	}
	minimum.Set(level)
	return nil
}
