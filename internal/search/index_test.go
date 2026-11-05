package search

import (
	"strings"
	"testing"
)

func TestNormalizeTokensIsUnicodeAwareAndDeterministic(t *testing.T) {
	got, err := NormalizeTokens("ÉCOLE café Café a-b naïve 中文 １２３", 8)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a", "b", "café", "naïve", "école", "中文", "１２３"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("NormalizeTokens() = %q, want %q", got, want)
	}
	if _, err := NormalizeTokens("one two three", 2); err == nil {
		t.Fatal("normalizer exceeded its unique-term bound")
	}
}
