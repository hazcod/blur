//go:build linux

package capture

import (
	"testing"

	platformlinux "github.com/hazcod/blur/internal/platform/linux"
)

// Terminals, document text, spin buttons and autocompletes carry
// user-entered content and must be pruned by the conservative Linux
// role policy; AT-SPI role ids follow the at-spi2 Role enum.
func TestATSPISensitiveRolesPruneUserEnteredContent(t *testing.T) {
	for _, tc := range []struct {
		role uint32
		name string
	}{
		{60, "terminal"},
		{94, "document text"},
		{52, "spin button"},
		{76, "autocomplete"},
		{61, "text"},
		{79, "entry"},
	} {
		roleName, sensitive := platformlinux.RoleForCapture(tc.role)
		if !sensitive || !isEditableTextRole(roleName) {
			t.Fatalf("%s (role %d) classified %q sensitive=%t; it must be pruned", tc.name, tc.role, roleName, sensitive)
		}
	}
	for _, tc := range []struct {
		role uint32
		name string
	}{
		{73, "paragraph"},
		{23, "frame"},
		{75, "application"},
		{43, "button"},
	} {
		roleName, sensitive := platformlinux.RoleForCapture(tc.role)
		if sensitive || isEditableTextRole(roleName) {
			t.Fatalf("%s (role %d) classified %q sensitive=%t; static content must stay readable", tc.name, tc.role, roleName, sensitive)
		}
	}
}

// The bitsets below are the state words a real GNOME desktop reports: a
// window accessible carries the active state bit only while it is the active
// window, and a focused widget carries the focused state bit.
func TestATSPIStateSetHasDecodesBitmaskWords(t *testing.T) {
	activeWindow := []uint32{1124073474, 0}
	inactiveWindow := []uint32{1124073472, 0}
	focusedWidget := []uint32{4096, 0}
	if !atspiStateSetHas(activeWindow, atspiStateActive) {
		t.Fatal("active window does not report the active state")
	}
	if atspiStateSetHas(activeWindow, atspiStateFocused) {
		t.Fatal("window accessible unexpectedly reported the focused state")
	}
	if atspiStateSetHas(inactiveWindow, atspiStateActive) || atspiStateSetHas(inactiveWindow, atspiStateFocused) {
		t.Fatal("inactive window reported a focus state")
	}
	if !atspiStateSetHas(focusedWidget, atspiStateFocused) {
		t.Fatal("focused widget does not report the focused state")
	}
	if atspiStateSetHas(focusedWidget, atspiStateActive) {
		t.Fatal("focused widget unexpectedly reported the active state")
	}
	if atspiStateSetHas(nil, atspiStateFocused) || atspiStateSetHas(nil, atspiStateActive) {
		t.Fatal("absent state words reported a focus state")
	}
	if atspiStateSetHas(activeWindow, 40) || atspiStateSetHas(activeWindow, 95) {
		t.Fatal("out-of-range state bits were reported as present")
	}
}
