package preferences

import (
	"strings"

	"golang.org/x/text/language"
)

const (
	VoiceSourceBoth       = "both"
	VoiceSourceMicrophone = "microphone"
	VoiceSourceSystem     = "system"
)

// VoiceNotesConfig contains choices for the next new recording. It contains no
// pointers or mutable collections and is independent of any recording metadata.
type VoiceNotesConfig struct {
	Source string `json:"source"`
	Locale string `json:"locale"`
}

func DefaultVoiceNotesConfig() VoiceNotesConfig {
	return VoiceNotesConfig{Source: VoiceSourceBoth, Locale: "en-US"}
}

// NormalizeVoiceNotes accepts bounded language tags, without claiming that the
// speech language is installed. Device availability is checked at recording time.
func NormalizeVoiceNotes(config VoiceNotesConfig) (VoiceNotesConfig, error) {
	if config.Source == "" {
		config.Source = VoiceSourceBoth
	}
	switch config.Source {
	case VoiceSourceBoth, VoiceSourceMicrophone, VoiceSourceSystem:
	default:
		return VoiceNotesConfig{}, ErrSettingsUnavailable
	}
	config.Locale = strings.TrimSpace(config.Locale)
	if config.Locale == "" {
		config.Locale = "en-US"
	}
	if len(config.Locale) > 64 {
		return VoiceNotesConfig{}, ErrSettingsUnavailable
	}
	for _, c := range config.Locale {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' {
			return VoiceNotesConfig{}, ErrSettingsUnavailable
		}
	}
	tag, err := language.Parse(config.Locale)
	if err != nil || tag == language.Und {
		return VoiceNotesConfig{}, ErrSettingsUnavailable
	}
	config.Locale = tag.String()
	return config, nil
}
