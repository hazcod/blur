// Package hotkey names the global shortcuts Blur offers for opening Quick ask
// and maps them to native key codes. It is pure data so every platform and the
// preferences store can validate the same canonical strings.
package hotkey

import "strings"

// Carbon modifier masks (cmdKey, shiftKey, optionKey, controlKey).
const (
	ModCommand uint32 = 256
	ModShift   uint32 = 512
	ModOption  uint32 = 2048
	ModControl uint32 = 4096
)

// MaxSpecLength bounds a persisted spec.
const MaxSpecLength = 32

// Option is one choice offered in settings.
type Option struct {
	// Spec is the canonical persisted string; "" means off.
	Spec  string
	Label string
}

// Options lists the shortcuts offered in settings, Off first.
func Options() []Option {
	return []Option{
		{"", "Off"},
		{"alt+space", "⌥Space"},
		{"ctrl+space", "⌃Space"},
		{"cmd+shift+space", "⌘⇧Space"},
		{"ctrl+alt+space", "⌃⌥Space"},
		{"ctrl+alt+b", "⌃⌥B"},
		{"cmd+shift+b", "⌘⇧B"},
		{"f18", "F18"},
		{"f19", "F19"},
	}
}

// Label is the display name of a spec, or "Off" when empty or unknown.
func Label(spec string) string {
	for _, option := range Options() {
		if option.Spec == spec {
			return option.Label
		}
	}
	return "Off"
}

var keyCodes = map[string]uint32{"space": 49, "b": 11, "f18": 79, "f19": 80}

var modifierMasks = map[string]uint32{"cmd": ModCommand, "shift": ModShift, "alt": ModOption, "ctrl": ModControl}

// Parse maps a canonical spec such as "alt+space" to a Carbon virtual key code
// and modifier mask. Function keys need no modifier; other keys need one.
func Parse(spec string) (keyCode, modifiers uint32, ok bool) {
	if spec == "" || len(spec) > MaxSpecLength || spec != strings.ToLower(spec) {
		return 0, 0, false
	}
	parts := strings.Split(spec, "+")
	key := parts[len(parts)-1]
	code, known := keyCodes[key]
	if !known {
		return 0, 0, false
	}
	for _, part := range parts[:len(parts)-1] {
		mask, known := modifierMasks[part]
		if !known || modifiers&mask != 0 {
			return 0, 0, false
		}
		modifiers |= mask
	}
	if modifiers == 0 && !strings.HasPrefix(key, "f") {
		return 0, 0, false
	}
	return code, modifiers, true
}

// Valid reports whether spec is empty (off) or a supported shortcut.
func Valid(spec string) bool {
	if spec == "" {
		return true
	}
	_, _, ok := Parse(spec)
	return ok
}
