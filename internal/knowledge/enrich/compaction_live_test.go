package enrich_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/knowledge/derive"
	"github.com/hazcod/blur/internal/knowledge/enrich"
)

type nativeBatchCompactor struct {
	*derive.CompactingGenerator
	calls, maxInput int
}

func (g *nativeBatchCompactor) GenerateCompacted(ctx context.Context, in []derive.Evidence, output int) ([]derive.Proposal, string, error) {
	g.calls++
	g.maxInput = max(g.maxInput, len(in[0].Text))
	return g.CompactingGenerator.GenerateCompacted(ctx, in, output)
}

func TestAppleQueuedCompactionSyntheticLargeAgentLog(t *testing.T) {
	if os.Getenv("BLUR_SYNTHETIC_APPLE_BACKGROUND") != "1" {
		t.Skip("explicit synthetic on-device check only")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	s := fairnessStore(t)
	var log strings.Builder
	for round := range 12 {
		for agent := range 10 {
			fmt.Fprintf(&log, "Round %d: agent_build_%02d validated its parser; queue checkpoint restoration awaits Morgan's review. This saved log does not establish the computer user's own actions.\n", round, agent)
		}
	}
	o := source(t, s, log.String())
	base, err := derive.NewAppleGenerator()
	if err != nil {
		t.Fatal(err)
	}
	g := &nativeBatchCompactor{CompactingGenerator: derive.WithCompaction(base, false)}
	defer g.CloseIdleConnections()
	w, err := enrich.NewService(s, s, g, enrich.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	started := time.Now()
	for {
		err = w.Backfill(ctx)
		if errors.Is(err, enrich.ErrMoreWork) {
			continue
		}
		if err != nil {
			t.Fatal("native queued source processing", err)
		}
		break
	}
	p, found, err := s.GetEnrichmentProgress(ctx, o.ContentID())
	if err != nil || !found || !p.Complete || p.Declined || p.Next != len(o.Text()) || p.CompactionParts != g.calls {
		t.Fatal("native processing lost an accepted source span", p, err)
	}
	if g.maxInput <= enrich.ChunkBytes {
		t.Fatal("native admission still used only legacy 1KiB batches", g.maxInput)
	}
	identifiers := map[string]bool{}
	for after := 0; ; {
		page, err := s.ListSourceCompactions(ctx, o.ContentID(), after, 32)
		if err != nil {
			t.Fatal(err)
		}
		for _, block := range page.Blocks {
			for agent := range 10 {
				id := fmt.Sprintf("agent_build_%02d", agent)
				identifiers[id] = identifiers[id] || strings.Contains(block.Text, id)
			}
		}
		if !page.HasMore {
			break
		}
		after = page.Next
	}
	for agent := range 10 {
		if !identifiers[fmt.Sprintf("agent_build_%02d", agent)] {
			t.Fatal("native summary blocks lost an exact agent name", agent)
		}
	}
	t.Logf("Native queued compaction: %d source bytes, %d calls, largest input %d bytes, elapsed %s", len(o.Text()), g.calls, g.maxInput, time.Since(started).Round(time.Millisecond))
}
