//go:build darwin

package capture

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

// Mutate live range content, transition timing and round size independently of
// the cursor. Only the external AX provider is simulated: native CF conversion,
// privacy checks and resumable cursor are production code. This does not read
// real applications or stand in for their Accessibility acceptance tests.
func FuzzNativeRangeDocumentStability(f *testing.F) {
	for _, seed := range [][]byte{{0, 0, 10, 60, 2}, {1, 0, 31, 0, 3}, {2, 1, 20, 124, 1}, {3, 2, 8, 3, 2}, {4, 3, 16, 10, 1}, {5, 4, 4, 252, 1}} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) < 5 {
			return
		}
		unit := []string{"Owner Mira. ", "Mira 日本語 🙂. ", "Mira cafe\u0301. ", "Mira مذكرة. ", "Mira résumé. ", "Mira 개발 기록. "}[int(data[0])%6]
		original := strings.Repeat(unit, 1+int(data[2])%48)
		modified := strings.ReplaceAll(original, "Mira", "Noah")
		provider := &alternateTextFixture{text: original, mode: "range", count: int64(len(utf16.Encode([]rune(original))))}
		node, meta := alternateNativeText(t, provider)
		cursor, err := newAXDocumentCursor(&nativeRangeCursorFixture{node}, 4, func(context.Context) error { return nil }, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer cursor.close()
		mode, transition := int(data[1])%5, int(data[4])%20
		changed := false
		var got strings.Builder
		for round := 0; round < 4000; round++ {
			budget := 4 + (int(data[3])+round*int(data[len(data)-1]%17))%253
			limits := Limits{MaxBytes: budget, MaxNodes: 8, MaxDepth: 4, MaxDuration: time.Second}
			p, err := cursor.read(context.Background(), limits, func(_ context.Context, text string, _ bool) error {
				if !utf8.ValidString(text) || changed && mode >= 2 {
					t.Fatal("invalid or newly excluded content escaped its native boundary")
				}
				got.WriteString(text)
				return nil
			})
			if p.Bytes > budget {
				t.Fatal("capture escaped byte budget", p.Bytes, budget)
			}
			if err != nil {
				if !changed || !errors.Is(err, errBrowserPageChanged) {
					t.Fatal("native source failed outside the planned transition", err)
				}
				return
			}
			if p.Done {
				if got.String() != original && got.String() != modified {
					t.Fatal("complete receipt admitted missing, repeated or mixed native text")
				}
				return
			}
			if mode > 0 && !changed && got.Len() > 0 && round >= transition {
				changed = true
				switch mode {
				case 1:
					provider.text = modified
				case 2:
					provider.count++
				case 3:
					meta.subrole = "AXSecureTextArea"
				case 4:
					node.api.parameterizedValue = func(uintptr, uintptr, uintptr, *uintptr) int32 { return axAttributeUnsupported }
				}
			}
		}
		t.Fatal("bounded native range never terminated")
	})
}
