package assistant

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func endsOf(text string) *documentEnds {
	var e documentEnds
	// Feed in uneven parts to exercise the rolling buffers.
	for len(text) > 0 {
		n := 777
		if n > len(text) {
			n = len(text)
		}
		for n < len(text) && !utf8.RuneStart(text[n]) {
			n++
		}
		e.add(text[:n])
		text = text[n:]
	}
	return &e
}

func TestExcerptBoundsAndKeepsBothEnds(t *testing.T) {
	doc := "START " + strings.Repeat("filler words go here without any stop ", 2000) + "the very end"
	head, tail, whole := endsOf(doc).excerpt()
	if whole || len(head) > excerptHeadBytes || len(tail) > excerptTailBytes {
		t.Fatalf("bounds: whole=%v head=%d tail=%d", whole, len(head), len(tail))
	}
	if !strings.HasPrefix(head, "START") || !strings.HasSuffix(tail, "the very end") {
		t.Fatalf("ends lost: %q ... %q", head[:10], tail[len(tail)-12:])
	}
}

func TestExcerptShortDocumentIsReturnedWhole(t *testing.T) {
	head, tail, whole := endsOf("A short page. Nothing more.").excerpt()
	if !whole || head != "A short page. Nothing more." || tail != "" {
		t.Fatalf("%v %q %q", whole, head, tail)
	}
}

func TestExcerptCutsAtSentenceBoundaryWithinWindow(t *testing.T) {
	body := strings.Repeat("a", 1100) + ". " + strings.Repeat("b", 3000) + ". " + strings.Repeat("c", 300) + ". Last sentence here"
	head, tail, _ := endsOf(body).excerpt()
	if !strings.HasSuffix(head, ".") || len(head) != 1101 {
		t.Fatalf("head not cut at the sentence end: len=%d tail=%q", len(head), head[len(head)-3:])
	}
	if !strings.HasPrefix(tail, "ccc") {
		t.Fatalf("tail does not start on a sentence: %q", tail[:10])
	}
}

func TestExcerptIsUTF8Safe(t *testing.T) {
	for _, filler := range []string{"é", "日本", "🙂"} {
		doc := strings.Repeat(filler, 3000)
		for shift := 0; shift < 4; shift++ {
			head, tail, whole := endsOf(strings.Repeat("x", shift) + doc).excerpt()
			if whole || !utf8.ValidString(head) || !utf8.ValidString(tail) || len(head) > excerptHeadBytes || len(tail) > excerptTailBytes {
				t.Fatalf("filler %q shift %d: invalid or oversized excerpt", filler, shift)
			}
		}
	}
}

func TestExcerptThresholdSizesNeverDropTextSilently(t *testing.T) {
	for _, size := range []int{1399, 1400, 1401, 1450, 1600, 1601} {
		for _, filler := range []string{"x", "é"} {
			for _, lead := range []string{"", "x"} {
				doc := lead + strings.Repeat(filler, size)
				doc = doc[:size]
				for !utf8.ValidString(doc) {
					doc = doc[:len(doc)-1]
				}
				head, tail, whole := endsOf(doc).excerpt()
				if len(doc) <= excerptHeadBytes+excerptTailBytes {
					if !whole || head != doc || tail != "" {
						t.Fatalf("size %d %q lead %q: complete document not returned whole (whole=%v head=%d)", size, filler, lead, whole, len(head))
					}
					continue
				}
				if whole || !strings.HasPrefix(doc, head) || !strings.HasSuffix(doc, tail) || len(head) == 0 || len(tail) == 0 || len(head) > excerptHeadBytes || len(tail) > excerptTailBytes || !utf8.ValidString(head) || !utf8.ValidString(tail) {
					t.Fatalf("size %d %q lead %q: bad head/tail split whole=%v head=%d tail=%d", size, filler, lead, whole, len(head), len(tail))
				}
			}
		}
	}
}
