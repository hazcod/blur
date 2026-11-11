// Package power keeps system power signals separate from user work and defines
// bounded admission for optional background work.
package power

import (
	"errors"
	"time"
)

type Mode string

const (
	FollowSystem Mode = "follow-system"
	Balanced     Mode = "balanced"
	Saver        Mode = "saver"
	Performance  Mode = "performance"
)

const (
	MaxBackgroundAIJobs   = 2
	MaxBackgroundTokens   = 32_000
	MaxBackgroundDuration = 15 * time.Minute
)

var ErrInvalidConfig = errors.New("power: invalid configuration")

// Config is non-sensitive local policy. Zero budgets use the selected mode's
// defaults; explicit values can only lower the product-wide hard ceilings.
type Config struct {
	Mode                  Mode          `json:"mode"`
	MaxBackgroundAIJobs   int           `json:"max_background_ai_jobs,omitempty"`
	MaxBackgroundTokens   int           `json:"max_background_tokens,omitempty"`
	MaxBackgroundDuration time.Duration `json:"max_background_duration_ns,omitempty"`
}

func DefaultConfig() Config { return Config{Mode: FollowSystem} }

type SystemState struct {
	Known        bool
	SaverEnabled bool
}

type Provider string

const (
	LocalProvider  Provider = "local"
	RemoteProvider Provider = "remote"
)

type Effective struct {
	RequestedMode         Mode
	Mode                  Mode
	System                SystemState
	CaptureEnabled        bool
	RetentionEnabled      bool
	InteractiveAIEnabled  bool
	OptionalIndexing      bool
	Compaction            bool
	MaxBackgroundAIJobs   int
	MaxBackgroundTokens   int
	MaxBackgroundDuration time.Duration
}

func Resolve(config Config, system SystemState) (Effective, error) {
	if !validMode(config.Mode) || config.MaxBackgroundAIJobs < 0 || config.MaxBackgroundAIJobs > MaxBackgroundAIJobs || config.MaxBackgroundTokens < 0 || config.MaxBackgroundTokens > MaxBackgroundTokens || config.MaxBackgroundDuration < 0 || config.MaxBackgroundDuration > MaxBackgroundDuration {
		return Effective{}, ErrInvalidConfig
	}
	mode := config.Mode
	if mode == FollowSystem {
		mode = Balanced
		if system.Known && system.SaverEnabled {
			mode = Saver
		}
	}
	effective := Effective{
		RequestedMode:        config.Mode,
		Mode:                 mode,
		System:               system,
		CaptureEnabled:       true,
		RetentionEnabled:     true,
		InteractiveAIEnabled: true,
	}
	switch mode {
	case Saver:
		// Optional background tasks pause, but passive capture, retention,
		// deterministic work required by a user action, and interactive AI remain.
	case Balanced:
		effective.OptionalIndexing, effective.Compaction = true, true
		effective.MaxBackgroundAIJobs = 1
		effective.MaxBackgroundTokens = 2048
		effective.MaxBackgroundDuration = time.Minute
	case Performance:
		effective.OptionalIndexing, effective.Compaction = true, true
		effective.MaxBackgroundAIJobs = 2
		effective.MaxBackgroundTokens = 8192
		effective.MaxBackgroundDuration = 5 * time.Minute
	}
	if config.MaxBackgroundAIJobs > 0 && config.MaxBackgroundAIJobs < effective.MaxBackgroundAIJobs {
		effective.MaxBackgroundAIJobs = config.MaxBackgroundAIJobs
	}
	if config.MaxBackgroundTokens > 0 && config.MaxBackgroundTokens < effective.MaxBackgroundTokens {
		effective.MaxBackgroundTokens = config.MaxBackgroundTokens
	}
	if config.MaxBackgroundDuration > 0 && config.MaxBackgroundDuration < effective.MaxBackgroundDuration {
		effective.MaxBackgroundDuration = config.MaxBackgroundDuration
	}
	return effective, nil
}

func (e Effective) AllowBackgroundAI(provider Provider, tokens int, duration time.Duration) bool {
	return provider == LocalProvider && e.MaxBackgroundAIJobs > 0 && tokens > 0 && tokens <= e.MaxBackgroundTokens && duration > 0 && duration <= e.MaxBackgroundDuration
}

func validMode(mode Mode) bool {
	switch mode {
	case FollowSystem, Balanced, Saver, Performance:
		return true
	default:
		return false
	}
}
