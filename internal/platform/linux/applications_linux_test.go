//go:build linux

package linux

import "testing"

func TestDesktopInfrastructureIsNotOffered(t *testing.T) {
	for id, want := range map[string]bool{
		"gnome-shell":            true,
		"ibus-extension-gtk3":    true,
		"xdg-desktop-portal-gtk": true,
		"xdg-dbus-proxy":         true,
		"firefox":                false,
		"ptyxis":                 false,
		"code":                   false,
	} {
		if got := desktopInfrastructure(id); got != want {
			t.Errorf("desktopInfrastructure(%q) = %v, want %v", id, got, want)
		}
	}
}
