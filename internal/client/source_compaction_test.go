package client

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/knowledge/enrich"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

func TestRecapUsesOnlyCompleteSourceCompactions(t *testing.T) {
	ctx := context.Background()
	s, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	o, _ := capture.SanitizeObservation("terminal.app", "", strings.Repeat("Work output. ", 140)+"Middle decision: Atlas waits for review.", capture.MaxObservationTextBytes)
	if err := s.PutObservationAt(ctx, o, time.Now()); err != nil {
		t.Fatal(err)
	}
	c := NewClient(s)
	defer c.Close()
	raw := assistant.Evidence{SourceID: o.ContentID(), Text: o.Text(), AppID: o.AppID(), CapturedAt: time.Now()}
	at := 0
	for {
		end, next, done, _ := enrich.NextChunk(o.Text(), at)
		if err := s.ApplyEnrichment(ctx, enrich.Chunk{SourceID: o.ContentID(), ModelKey: enrich.ModelKey("compact"), Start: at, End: end, Next: next, Complete: done, CompactionVersion: 1, Compaction: "Atlas waits for review."}); err != nil {
			t.Fatal(err)
		}
		got, err := c.compactRecapSources(ctx, []assistant.Evidence{raw})
		if err != nil {
			t.Fatal(err)
		}
		if done {
			if strings.Count(got[0].Text, "Atlas waits for review.") != 2 || got[0].Layer != assistant.LayerCompaction || got[0].SourceID != raw.SourceID || got[0].Origin != assistant.OriginInferred {
				t.Fatal("unlabelled or missing completed compaction", got)
			}
			break
		}
		if got[0].Text != raw.Text || got[0].Layer != assistant.LayerSource {
			t.Fatal("unfinished compaction hid raw tail", got)
		}
		at = next
	}
}
