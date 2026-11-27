package capture

import (
	"strings"
	"testing"
)

func TestMeetsMinimumContentCountsNonWhitespaceCharacters(t *testing.T) {
	cases := []struct {
		name, app, text string
		want            bool
	}{
		{"single ASCII code point", "org.example.editor", "0", true},
		{"short terminal command", "com.apple.terminal", "ls", true},
		{"short status", "org.example.editor", "ok", true},
		{"padded single code point", "org.example.editor", " \n\t日  ", true},
		{"single accented code point", "org.example.editor", "é", true},
		{"single emoji code point", "org.example.editor", "😀", true},
		{"combining code point", "org.example.editor", "\u0301", true},
		{"31 characters", "org.example.editor", strings.Repeat("a", 31), true},
		{"whitespace only", "org.example.editor", strings.Repeat(" \n\t  ", 40), false},
		{"empty", "org.example.editor", "", false},
		{"short voice transcript", "sh.blur.meetings", "ok", true},
		{"short typed note", "sh.blur.notes", "ok", true},
		{"empty voice transcript", "sh.blur.meetings", "", false},
		{"blank typed note", "sh.blur.notes", " \n\t  ", false},
	}
	for _, c := range cases {
		if got := MeetsMinimumContent(c.app, c.text); got != c.want {
			t.Errorf("%s: got %t want %t", c.name, got, c.want)
		}
	}
}

func TestMinimumContentPreservesSingleVisibleGraphemes(t *testing.T) {
	const app = "org.example.editor"
	combining := "e\u0301"
	if !MeetsMinimumContent(app, combining) {
		t.Fatal("a decomposed accented character should be retained")
	}
	family := "\U0001F468\u200D\U0001F469\u200D\U0001F467\u200D\U0001F466"
	if !MeetsMinimumContent(app, family) {
		t.Fatal("a single ZWJ family should be retained")
	}
}
