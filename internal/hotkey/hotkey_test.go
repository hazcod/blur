package hotkey

import "testing"

func TestParseMapsEveryOffered(t *testing.T) {
	want := map[string][2]uint32{
		"alt+space":       {49, ModOption},
		"ctrl+space":      {49, ModControl},
		"cmd+shift+space": {49, ModCommand | ModShift},
		"ctrl+alt+space":  {49, ModControl | ModOption},
		"ctrl+alt+b":      {11, ModControl | ModOption},
		"cmd+shift+b":     {11, ModCommand | ModShift},
		"f18":             {79, 0},
		"f19":             {80, 0},
	}
	for _, option := range Options() {
		if option.Spec == "" {
			continue
		}
		code, mods, ok := Parse(option.Spec)
		if !ok || [2]uint32{code, mods} != want[option.Spec] {
			t.Errorf("Parse(%q) = %d,%d,%v want %v", option.Spec, code, mods, ok, want[option.Spec])
		}
		delete(want, option.Spec)
	}
	if len(want) != 0 {
		t.Fatalf("options missing %v", want)
	}
}

func TestParseRejectsInvalid(t *testing.T) {
	for _, spec := range []string{"", "space", "b", "alt+", "alt+alt+space", "hyper+space", "ALT+SPACE", "alt+z", "alt+space+b"} {
		if _, _, ok := Parse(spec); ok {
			t.Errorf("Parse(%q) accepted", spec)
		}
	}
}

func TestValidAndLabel(t *testing.T) {
	if !Valid("") || !Valid("alt+space") || Valid("nope") {
		t.Fatal("Valid misclassified")
	}
	if Label("alt+space") != "⌥Space" || Label("") != "Off" || Label("zzz") != "Off" {
		t.Fatal("Label wrong")
	}
}
