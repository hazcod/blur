package notes

import (
	"context"
	"strings"
	"testing"

	"github.com/hazcod/blur/internal/capture"
)

func TestAppleExtractionAcceptsCanonicalNoteRevisionsAndRejectsOtherDomains(t *testing.T) {
	for _, tc := range []struct {
		domain string
		valid  bool
	}{
		{"local", true},
		{"r2." + strings.Repeat("a", 64) + ".notes.local", true},
		{"r02." + strings.Repeat("a", 64) + ".notes.local", false},
		{"r0." + strings.Repeat("a", 64) + ".notes.local", false},
		{"r2.user.notes.local", false},
		{"remote.example", false},
	} {
		t.Run(tc.domain, func(t *testing.T) {
			o, err := capture.SanitizeObservation("sh.blur.notes", tc.domain, "Review the draft tomorrow", 4096)
			if err != nil {
				t.Fatal(err)
			}
			model := &appleBudgetCompletion{appleCompletionStub: appleCompletionStub{text: `{"proposals":[]}`}, count: 100, capacity: 4096}
			_, err = (&AppleExtractor{client: model}).Extract(context.Background(), o)
			if tc.valid && (err != nil || len(model.requests) != 1) {
				t.Fatalf("current note cannot extract: %v", err)
			}
			if !tc.valid && (err == nil || len(model.requests) != 0) {
				t.Fatalf("non-note domain reached model: %v", err)
			}
		})
	}
}
