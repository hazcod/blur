//go:build darwin

package capture

import (
	"context"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestDocumentRangeOnlyReadsFullSourceBeyondVisibleTail(t *testing.T) {
	raw := strings.Repeat("technical 日本語 notes🙂 ", 2000) + "TAIL_FULL_RANGE"
	f := &alternateTextFixture{text: raw, mode: "range", count: int64(len(utf16.Encode([]rune(raw)))), visible: &nativeTextRange{Location: 10, Length: 20}}
	n, _ := alternateNativeText(t, f)
	text, err := n.openText(context.Background())
	if err != nil || text == nil {
		t.Fatal(err)
	}
	defer text.release()
	var got strings.Builder
	for count := 0; count < 10000; count++ {
		piece, done, err := text.read(context.Background(), 128)
		if err != nil {
			t.Fatal(err)
		}
		got.WriteString(piece)
		if done {
			break
		}
		if count == 9999 {
			t.Fatal("range did not end")
		}
	}
	if got.String() != raw {
		t.Fatalf("lost reported full range: got=%d want=%d", got.Len(), len(raw))
	}
	if partial, ok := text.(interface{ coverageLimited() bool }); !ok || partial.coverageLimited() {
		t.Fatal("known complete range not reported")
	}
}
func TestDocumentRangeRejectsLengthOrSensitivityChange(t *testing.T) {
	for _, secure := range []bool{false, true} {
		t.Run(map[bool]string{false: "length", true: "secure"}[secure], func(t *testing.T) {
			f := &alternateTextFixture{text: "Mira owns rollback.", mode: "range", count: 19}
			n, meta := alternateNativeText(t, f)
			text, err := n.openText(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer text.release()
			f.onParameter = func() {
				if secure {
					meta.subrole = "AXSecureTextArea"
				} else {
					f.count++
				}
			}
			got, _, err := text.read(context.Background(), 128)
			if err == nil || got != "" {
				t.Fatal("changed source escaped", got, err)
			}
		})
	}
}
