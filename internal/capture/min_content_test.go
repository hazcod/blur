package capture

import (
	"strings"
	"testing"
)

func TestMeetsMinimumContentCountsNonWhitespaceCharacters(t *testing.T) {
	if MinObservationChars != 32 {
		t.Fatalf("MinObservationChars = %d", MinObservationChars)
	}
	cases := []struct {
		name, app, text string
		want            bool
	}{
		{"31 characters", "org.example.editor", strings.Repeat("a", 31), false},
		{"32 characters", "org.example.editor", strings.Repeat("a", 32), true},
		{"31 plus spaces", "org.example.editor", strings.Repeat("a b\n", 10) + "abcdefghijk  \t", false},
		{"32 multibyte runes", "org.example.editor", strings.Repeat("é", 32), true},
		{"31 multibyte runes", "org.example.editor", strings.Repeat("日", 31), false},
		{"whitespace only", "org.example.editor", strings.Repeat(" \n\t  ", 40), false},
		{"empty", "org.example.editor", "", false},
		{"voice transcript exempt", "sh.blur.meetings", "ok", true},
		{"typed note exempt", "sh.blur.notes", "ok", true},
	}
	for _, c := range cases {
		if got := MeetsMinimumContent(c.app, c.text); got != c.want {
			t.Errorf("%s: got %t want %t", c.name, got, c.want)
		}
	}
}
