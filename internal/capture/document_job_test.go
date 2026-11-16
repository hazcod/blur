package capture

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestDocumentJobReadsWholeLargeSourceAcrossBoundedRounds(t *testing.T) {
	g := &documentTestGraph{}
	raw := strings.Repeat("func example() { return 42 }\n", 90000) + "tail-evidence-unique"
	root := &documentTestNode{g: g, id: 1, name: "AXWindow", children: []*documentTestNode{{g: g, id: 2, name: "AXTextArea", text: raw}, {g: g, id: 3, name: "AXStaticText", text: "another node"}}}
	cursor, err := newAXDocumentCursor(root.ref(), 32, func(context.Context) error { return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	builder, err := NewCaptureBuilder("org.example.editor", "", strings.Repeat("a", 64), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	job := &axCaptureDocumentJob{cursor: cursor, builder: builder, limits: DefaultLimits(), guard: func(context.Context) error { return nil }}
	defer job.Close()
	var saved strings.Builder
	var summary CaptureSummary
	rounds := 0
	for ; rounds < 100; rounds++ {
		p, err := job.Read(context.Background(), func(_ context.Context, parts []CapturePart) error {
			for _, part := range parts {
				saved.WriteString(part.Text())
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if p.Bytes > DefaultLimits().MaxBytes {
			t.Fatal("round exceeded budget")
		}
		if !p.Done && p.Summary.Validate() == nil {
			t.Fatal("incomplete source published summary")
		}
		if p.Done {
			summary = p.Summary
			break
		}
	}
	if rounds < 2 || summary.Validate() != nil || summary.Coverage() != CaptureComplete || saved.String() != raw+"\nanother node" {
		t.Fatalf("lost source tail: rounds=%d bytes=%d", rounds, saved.Len())
	}
	job.Close()
	if g.active != 0 || g.texts != 0 || g.doubleRelease != 0 {
		t.Fatalf("retained native leases: %+v", g)
	}
}
