package bolt

import "testing"

func TestWindowsPrivateACLAcceptsOnlyFullFileControl(t *testing.T) {
	for _, test := range []struct {
		name string
		mask uint32
		want bool
	}{
		{"generic full control", 0x10000000, true},
		{"mapped file full control", 0x001f01ff, true},
		{"read only", 0x00120089, false},
		{"zero rights", 0, false},
		{"unexpected rights", 0x101f01ff, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := windowsFullControlMask(test.mask); got != test.want {
				t.Fatalf("full-control ACL mask %#x accepted=%t, want %t", test.mask, got, test.want)
			}
		})
	}
}
