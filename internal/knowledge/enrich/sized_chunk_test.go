package enrich_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/knowledge/enrich"
)

func TestMinimumSizedChunksAdvanceAcrossMixedWidthUnicode(t *testing.T) {
	for _, text := range []string{"a😀b😀c😀end", " 😀 😀tail", "é😀a😀終", strings.Repeat("a😀", 20)} {
		covered := make([]bool, len(text))
		for start := 0; start < len(text); {
			end, next, done, err := enrich.NextSizedChunk(text, start, utf8.UTFMax)
			if err != nil || end <= start || next <= start || next > end || !utf8.ValidString(text[start:end]) {
				t.Fatalf("minimum admission made no valid progress for %q at %d: end=%d next=%d done=%v err=%v", text, start, end, next, done, err)
			}
			for i := start; i < end; i++ {
				covered[i] = true
			}
			start = next
		}
		for i, ok := range covered {
			if !ok {
				t.Fatalf("minimum admission skipped source byte %d in %q", i, text)
			}
		}
	}
}
