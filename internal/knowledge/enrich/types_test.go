package enrich

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/knowledge"
)

func TestChunksCoverCompleteUTF8SourceWithBoundedOverlap(t *testing.T) {
	text := strings.Repeat("aé界🙂\n", 1700)
	covered := make([]bool, len(text))
	start := 0
	for count := 0; ; count++ {
		end, next, complete, err := NextChunk(text, start)
		if err != nil || end-start > ChunkBytes || !utf8.ValidString(text[start:end]) || count > len(text) {
			t.Fatalf("invalid chunk %d:%d next=%d complete=%v err=%v", start, end, next, complete, err)
		}
		for i := start; i < end; i++ {
			covered[i] = true
		}
		if complete {
			break
		}
		if next <= start || next >= end || end-next > ChunkOverlap {
			t.Fatal("chunk did not advance with bounded overlap")
		}
		start = next
	}
	for _, seen := range covered {
		if !seen {
			t.Fatal("chunking silently omitted evidence")
		}
	}
	if _, _, _, err := NextChunk("é", 1); err == nil {
		t.Fatal("accepted split rune")
	}
}

func TestInferredIdentityDoesNotMergeSameNamesAcrossContexts(t *testing.T) {
	id := EntityID(knowledge.Person, "Alex", "chat.app", "one.example")
	if !IsEntityID(id) || id != EntityID(knowledge.Person, " ALEX ", "chat.app", "one.example") {
		t.Fatal("unstable normalized identity")
	}
	if id == EntityID(knowledge.Person, "Alex", "chat.app", "two.example") || id == EntityID(knowledge.Project, "Alex", "chat.app", "one.example") {
		t.Fatal("unrelated mentions were merged")
	}
}
