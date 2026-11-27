package browsercapture

import (
	"context"
	"testing"

	"github.com/hazcod/blur/internal/capture"
)

// The document path counts content across acknowledged batches. A blank first
// batch must not hide a later one-code-point document or publish blank content.
func TestDOMDocumentPreservesShortContentAcrossBatches(t *testing.T) {
	for _, tc := range []struct {
		name, tail string
		published  bool
	}{
		{"single ASCII", "x", true},
		{"single Unicode", "日", true},
		{"short status", "ok", true},
		{"empty", "", false},
		{"Unicode whitespace", "  \t", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, grant, store, now := documentReceiver(t)
			ctx := context.Background()
			for sequence, text := range []string{" \n", tc.tail} {
				wire := changeDocumentWire(t, grant, now, documentWire(t, grant, now, 1, sequence, text, sequence == 1, false), func(page map[string]any) { page["title"] = "" })
				if err := r.Accept(ctx, wire); err != nil {
					t.Fatalf("batch %d: %v", sequence, err)
				}
			}
			if !tc.published {
				page, err := store.ListCaptures(ctx, "", 8)
				if err != nil || len(page.Editions) != 0 || len(store.published) != 0 || r.DroppedShort() != 1 {
					t.Fatalf("blank document published: page=%+v summaries=%d dropped=%d err=%v", page, len(store.published), r.DroppedShort(), err)
				}
				return
			}
			if len(store.published) != 1 || store.published[0].Coverage() != capture.CaptureComplete || r.DroppedShort() != 0 {
				t.Fatalf("short document dropped: summaries=%d dropped=%d", len(store.published), r.DroppedShort())
			}
			parts, err := store.ReadCaptureParts(ctx, store.published[0].ContentID(), -1, 8)
			if err != nil || parts.HasMore || len(parts.Parts) != 1 || parts.Parts[0].Text() != " \n"+tc.tail {
				t.Fatalf("short original changed: parts=%+v err=%v", parts, err)
			}
		})
	}
}
