package enrich_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/hazcod/blur/internal/knowledge/enrich"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/durable"
)

// The deterministic provider checks storage/queue coverage at scale, rather
// than making a claim about the quality or speed of a model's generated prose.
func TestCompactionMegabyteCaptureVisitsEverySpanAcrossRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	path, key := filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey()
	s, err := durable.OpenDurable(path, key, durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	var log strings.Builder
	for round := 0; log.Len() < 1<<20; round++ {
		fmt.Fprintf(&log, "Round %05d agent_build_%02d checkpoint completed; parser result saved for review.\n", round, round%10)
	}
	log.WriteString("END_OF_MEGABYTE_AGENT_LOG")
	summary := publishedLibraryCapture(t, s, log.String())
	originals := map[string]string{}
	var sourceBytes int
	for after := ""; ; {
		page, err := s.ListCaptureEvidencePage(ctx, storage.CaptureEvidencePageRequest{After: after, Limit: 8, MaxBytes: storage.MaxObservationPageBytes})
		if err != nil {
			t.Fatal(err)
		}
		for _, source := range page.Sources {
			originals[source.SourceID()] = source.Text()
			sourceBytes += len(source.Text())
		}
		if !page.HasMore {
			break
		}
		if page.Next == after {
			t.Fatal("source directory did not advance")
		}
		after = page.Next
	}
	if sourceBytes != log.Len() || summary.Bytes() != int64(sourceBytes) || len(originals) < 32 {
		t.Fatal("large capture did not publish its entire source", sourceBytes, log.Len(), len(originals))
	}
	g := &compactingTestGenerator{allCaptureGenerator: allCaptureGenerator{inputs: map[string][]string{}}, last: map[string]string{}}
	w, err := enrich.NewService(s, s, g, enrich.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := w.Backfill(ctx); !errors.Is(err, enrich.ErrMoreWork) {
			t.Fatal("large capture failed to yield unfinished work", err)
		}
	}
	w.Close()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = durable.OpenDurable(path, key, durable.OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	w, err = enrich.NewService(s, s, g, enrich.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	for {
		err := w.Backfill(ctx)
		if err == nil {
			break
		}
		if !errors.Is(err, enrich.ErrMoreWork) {
			t.Fatal(err)
		}
	}
	blocks := 0
	for id, original := range originals {
		covered, after := make([]bool, len(original)), 0
		for _, span := range g.inputs[id] {
			at := strings.Index(original[after:], span)
			if at < 0 {
				t.Fatal("worker repeated or changed an accepted source span", id)
			}
			at += after
			for i := at; i < at+len(span); i++ {
				covered[i] = true
			}
			after = at + 1
		}
		for i, r := range original {
			if !unicode.IsSpace(r) && !covered[i] {
				t.Fatalf("large-capture processing skipped byte %d of source %s", i, id)
			}
		}
		progress, found, err := s.GetEnrichmentProgress(ctx, id)
		if err != nil || !found || !progress.Complete || progress.Declined || progress.Next != len(original) || progress.CompactionParts != len(g.inputs[id]) {
			t.Fatal("large capture has a false or missing completion receipt", progress, err)
		}
		count := 0
		for after := 0; ; {
			page, err := s.ListSourceCompactions(ctx, id, after, 7)
			if err != nil {
				t.Fatal(err)
			}
			for _, block := range page.Blocks {
				if block.Index != count {
					t.Fatal("summary block dropped or duplicated", count, block.Index)
				}
				count++
			}
			if !page.HasMore {
				break
			}
			if page.Next <= after {
				t.Fatal("summary page did not advance")
			}
			after = page.Next
		}
		if count != len(g.inputs[id]) {
			t.Fatal("processed span lost its persisted block", count, len(g.inputs[id]))
		}
		blocks += count
	}
	t.Logf("Processed %d accepted bytes across %d published parts and %d persisted summary blocks, including encrypted reopen", sourceBytes, len(originals), blocks)
}
