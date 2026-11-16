package linux

import "testing"

func TestValidATSPISenderAcceptsShortUniqueNames(t *testing.T) {
	for name, want := range map[string]bool{
		":1.0": true, ":1.1": true, ":1.42": true, ":1.42.7": true, ":a-b.c_d": true,
		"": false, ":1": false, ":1.": false, ":.1": false, "1.0": false, ":1.bad!": false, ":1..2": false,
	} {
		if got := validATSPISender(name); got != want {
			t.Errorf("validATSPISender(%q) = %t, want %t", name, got, want)
		}
	}
}
