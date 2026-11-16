package darwin

import (
	"errors"
	"testing"
)

func TestMeetingHotkeyRegistrationPreservesQuickAskAndRoutesByID(t *testing.T) {
	// Fake Carbon only: no global shortcut is actually registered.
	quickHotkey.loaded, quickHotkey.handlerOK = true, true
	quickHotkey.loadErr = nil
	var next uintptr
	removed := make(map[uintptr]bool)
	quickHotkey.register = func(_, _ uint32, _ uint64, _ uintptr, _ uint32, out *uintptr) int32 { next++; *out = next; return 0 }
	quickHotkey.unregist = func(ref uintptr) int32 { removed[ref] = true; return 0 }
	quickHotkey.target = func() uintptr { return 1 }
	t.Cleanup(func() {
		UnregisterMeetingHotkey()
		UnregisterQuickAskHotkey()
		quickHotkey.loaded, quickHotkey.handlerOK = false, false
		quickHotkey.register, quickHotkey.unregist, quickHotkey.target = nil, nil, nil
	})
	var quick, meeting int
	if err := RegisterQuickAskHotkey("alt+space", func() { quick++ }); err != nil {
		t.Fatal(err)
	}
	quickRef := quickHotkey.ref
	if err := RegisterMeetingHotkey("ctrl+alt+m", func() { meeting++ }); err != nil {
		t.Fatal(err)
	}
	if !dispatchBlurHotkey(uint64(hotkeySignature)|uint64(hotkeyIdentifier)<<32) || !dispatchBlurHotkey(uint64(hotkeySignature)|uint64(meetingHotkeyID)<<32) || quick != 1 || meeting != 1 {
		t.Fatal("separate shortcuts did not route independently")
	}
	if err := RegisterMeetingHotkey("alt+space", func() {}); !errors.Is(err, ErrHotkeyUnavailable) {
		t.Fatal("meeting conflict not reported")
	}
	if removed[quickRef] || quickHotkey.ref != quickRef || !dispatchBlurHotkey(uint64(hotkeySignature)|uint64(hotkeyIdentifier)<<32) || quick != 2 {
		t.Fatal("meeting conflict broke Quick Ask")
	}
	if dispatchBlurHotkey(uint64(7)|uint64(hotkeyIdentifier)<<32) || dispatchBlurHotkey(uint64(hotkeySignature)|uint64(99)<<32) {
		t.Fatal("foreign shortcut claimed by Blur")
	}
}
