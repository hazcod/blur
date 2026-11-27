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

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/knowledge/derive"
	"github.com/hazcod/blur/internal/knowledge/enrich"
	"github.com/hazcod/blur/internal/model"
	"github.com/hazcod/blur/internal/power"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

type compactingTestGenerator struct {
	allCaptureGenerator
	last map[string]string
	err  error
}

type performanceCompactor struct {
	compactingTestGenerator
	output int
}

func (*performanceCompactor) ContextTokenLimit(context.Context) (int, error) { return 8192, nil }
func (g *performanceCompactor) GenerateCompacted(ctx context.Context, in []derive.Evidence, out int) ([]derive.Proposal, string, error) {
	g.output = out
	if out > model.MaxCompletionTokens {
		return nil, "", model.ErrOllamaConfig
	}
	return g.compactingTestGenerator.GenerateCompacted(ctx, in, out)
}
func TestCompactionAdmitsPerformanceRequestsWithinProviderOutputCeiling(t *testing.T) {
	ctx := context.Background()
	s, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	o, _ := capture.SanitizeObservation("terminal.app", "", "Agent parser completed validation.", capture.MaxObservationTextBytes)
	if err := s.PutObservation(ctx, o); err != nil {
		t.Fatal(err)
	}
	g := &performanceCompactor{compactingTestGenerator: compactingTestGenerator{allCaptureGenerator: allCaptureGenerator{inputs: map[string][]string{}}, last: map[string]string{}}}
	w, _ := enrich.NewService(s, s, g, enrich.Options{Policy: policy{mode: power.Performance}})
	defer w.Close()
	if err := w.Backfill(ctx); err != nil {
		t.Fatal("performance mode stranded valid source", g.output, err)
	}
	p, _, _ := s.GetEnrichmentProgress(ctx, o.ContentID())
	if !p.Complete || g.output > model.MaxCompletionTokens {
		t.Fatal(p, g.output)
	}
}

type admittedCompactor struct {
	compactingTestGenerator
	inputBytes      int
	budgetViolation bool
}

type inputCapacityCompactor struct{ compactingTestGenerator }

type minimumInputCompactor struct{ compactingTestGenerator }

func (*minimumInputCompactor) CountCompactedInputTokens(_ context.Context, in []derive.Evidence, _ int) (int, error) {
	return 1500 + 9*len(in[0].Text), nil
}

func TestCompactionMinimumAdmissionCompletesMixedUnicodeWithoutReplayingPrefix(t *testing.T) {
	ctx := context.Background()
	s := fairnessStore(t)
	o := source(t, s, "a😀b😀c😀tail")
	g := &minimumInputCompactor{compactingTestGenerator{allCaptureGenerator: allCaptureGenerator{inputs: map[string][]string{}}, last: map[string]string{}}}
	w, err := enrich.NewService(s, s, g, enrich.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	complete(t, w)
	p, found, err := s.GetEnrichmentProgress(ctx, o.ContentID())
	if err != nil || !found || !p.Complete || p.Next != len(o.Text()) {
		t.Fatal("minimum admission stranded a valid Unicode source", p, err)
	}
	page, err := s.ListSourceCompactions(ctx, o.ContentID(), 0, 32)
	if err != nil || page.HasMore || len(page.Blocks) == 0 {
		t.Fatal("minimum admission produced unreadable progress", err)
	}
	for _, block := range page.Blocks {
		if block.Next <= block.Start || len(o.Text()[block.Start:block.End]) > 4 {
			t.Fatal("minimum admission did not advance", block)
		}
	}
}

func (*inputCapacityCompactor) CountCompactedInputTokens(_ context.Context, in []derive.Evidence, _ int) (int, error) {
	return 300 + len(in[0].Text)/4, nil
}

func TestCompactionUsesAdmittedInputCapacityForLargeSources(t *testing.T) {
	ctx := context.Background()
	s := fairnessStore(t)
	o := source(t, s, strings.Repeat("Large project work records. ", 450))
	g := &inputCapacityCompactor{compactingTestGenerator{allCaptureGenerator: allCaptureGenerator{inputs: map[string][]string{}}, last: map[string]string{}}}
	w, err := enrich.NewService(s, s, g, enrich.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if err := w.Backfill(ctx); err != nil && !errors.Is(err, enrich.ErrMoreWork) {
		t.Fatal(err)
	}
	maxInput := 0
	for _, span := range g.inputs[o.ContentID()] {
		maxInput = max(maxInput, len(span))
	}
	if maxInput <= enrich.ChunkBytes {
		t.Fatal("compaction retained the unrelated legacy proposal chunk ceiling", maxInput)
	}
	complete(t, w)
	p, found, err := s.GetEnrichmentProgress(ctx, o.ContentID())
	if err != nil || !found || !p.Complete || p.Next != len(o.Text()) {
		t.Fatal("large source did not complete through admitted requests", p, err)
	}
}

func (g *admittedCompactor) CountCompactedInputTokens(_ context.Context, in []derive.Evidence, _ int) (int, error) {
	return 1100 + 6*len(in[0].Text), nil
}
func (g *admittedCompactor) GenerateCompacted(ctx context.Context, in []derive.Evidence, out int) ([]derive.Proposal, string, error) {
	if 1100+6*len(in[0].Text)+out > 2048 {
		g.budgetViolation = true
	}
	g.inputBytes += len(in[0].Text)
	return g.compactingTestGenerator.GenerateCompacted(ctx, in, out)
}

func TestCompactionShrinksRequestsInsteadOfDroppingDenseSourceTails(t *testing.T) {
	ctx := context.Background()
	s, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	text := strings.Repeat("<agent_α> build passed; ", 40) + "FINAL_END"
	o, err := capture.SanitizeObservation("terminal.app", "", text, capture.MaxObservationTextBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutObservation(ctx, o); err != nil {
		t.Fatal(err)
	}
	g := &admittedCompactor{compactingTestGenerator: compactingTestGenerator{allCaptureGenerator: allCaptureGenerator{inputs: map[string][]string{}}, last: map[string]string{}}}
	w, _ := enrich.NewService(s, s, g, enrich.Options{})
	defer w.Close()
	complete(t, w)
	if g.budgetViolation {
		t.Fatal("generated outside admitted token budget")
	}
	text = o.Text()
	covered := make([]bool, len(text))
	after := 0
	for {
		page, err := s.ListSourceCompactions(ctx, o.ContentID(), after, 32)
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range page.Blocks {
			span := g.inputs[o.ContentID()][b.Index]
			if len(span) > 64 || span != strings.TrimSpace(text[b.Start:b.End]) {
				t.Fatal("request changed or was not adapted", len(span))
			}
			for i := b.Start; i < b.End; i++ {
				covered[i] = true
			}
			after++
		}
		if !page.HasMore {
			break
		}
	}
	for i, r := range text {
		if !unicode.IsSpace(r) && !covered[i] {
			t.Fatal("dense source byte omitted", i)
		}
	}
}

func TestCompactionFailureDoesNotAdvanceCommittedSourcePrefix(t *testing.T) {
	ctx := context.Background()
	s, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	o, _ := capture.SanitizeObservation("terminal.app", "", strings.Repeat("Agent build decisions. ", 1000), capture.MaxObservationTextBytes)
	if err := s.PutObservation(ctx, o); err != nil {
		t.Fatal(err)
	}
	g := &compactingTestGenerator{allCaptureGenerator: allCaptureGenerator{inputs: map[string][]string{}}, last: map[string]string{}}
	w, _ := enrich.NewService(s, s, g, enrich.Options{})
	defer w.Close()
	if err := w.Backfill(ctx); !errors.Is(err, enrich.ErrMoreWork) {
		t.Fatal(err)
	}
	before, _, _ := s.GetEnrichmentProgress(ctx, o.ContentID())
	g.err = model.ErrAppleRefused
	if err := w.Backfill(ctx); err != nil {
		t.Fatal(err)
	}
	after, _, _ := s.GetEnrichmentProgress(ctx, o.ContentID())
	if after.Next != before.Next || after.CompactionParts != before.CompactionParts || after.Complete || !after.Declined {
		t.Fatal("failed model output advanced covered prefix", before, after)
	}
}

func (*compactingTestGenerator) Identity(context.Context) (string, error) {
	return "compaction-test-v1", nil
}
func (g *compactingTestGenerator) CountCompactedInputTokens(context.Context, []derive.Evidence, int) (int, error) {
	return 500, nil
}
func (g *compactingTestGenerator) GenerateCompacted(ctx context.Context, in []derive.Evidence, outputTokens int) ([]derive.Proposal, string, error) {
	if g.err != nil {
		return nil, "", g.err
	}
	id := in[0].SourceID
	if outputTokens < 1 {
		return nil, "", errors.New("no output admission")
	}
	_, _ = g.allCaptureGenerator.Generate(ctx, in)
	summary := fmt.Sprintf("Read %d spans for this source.", len(g.inputs[id]))
	g.last[id] = summary
	return nil, summary, nil
}

func TestQueuedCompactionCoversFullSourcesAcrossEncryptedReopen(t *testing.T) {
	ctx := context.Background()
	path, key := filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey()
	s, err := durable.OpenDurable(path, key, durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	g := &compactingTestGenerator{allCaptureGenerator: allCaptureGenerator{inputs: map[string][]string{}}, last: map[string]string{}}
	var ids []string
	for _, app := range []string{"com.apple.Terminal", "editor.app", "browser.app", "mail.app", "slack.app", "sh.blur.meetings"} {
		o, err := capture.SanitizeObservation(app, "", strings.Repeat("Start αβγ project decisions and agent status.\n", 100)+"FINAL TAIL", capture.MaxObservationTextBytes)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.PutObservationAt(ctx, o, time.Now()); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, o.ContentID())
	}
	w, _ := enrich.NewService(s, s, g, enrich.Options{})
	if err := w.Backfill(ctx); !errors.Is(err, enrich.ErrMoreWork) {
		t.Fatal(err)
	}
	w.Close()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = durable.OpenDurable(path, key, durable.OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	w, _ = enrich.NewService(s, s, g, enrich.Options{})
	defer w.Close()
	complete(t, w)
	for _, id := range ids {
		p, found, err := s.GetEnrichmentProgress(ctx, id)
		if err != nil || !found || !p.Complete || p.CompactionVersion != 1 || p.Compaction != "" || p.CompactionParts != len(g.inputs[id]) {
			t.Fatal("missing complete compaction", p, err)
		}
		after := 0
		for {
			page, err := s.ListSourceCompactions(ctx, id, after, 2)
			if err != nil {
				t.Fatal(err)
			}
			for _, b := range page.Blocks {
				if b.Index != after || b.Text != fmt.Sprintf("Read %d spans for this source.", after+1) {
					t.Fatal("compaction block lost", b)
				}
				after++
			}
			if !page.HasMore {
				break
			}
			if page.Next != after {
				t.Fatal("bad continuation")
			}
		}
		if after != p.CompactionParts {
			t.Fatal("source summaries dropped", after, p.CompactionParts)
		}
		o, _, _, _ := s.GetObservationWithCaptureTime(ctx, id)
		for _, marker := range []string{"Start αβγ", "FINAL TAIL"} {
			if !strings.Contains(strings.Join(g.inputs[id], ""), marker) {
				t.Fatal("source span skipped", marker)
			}
		}
		if p.Next != len(o.Text()) {
			t.Fatal("compaction falsely complete", p)
		}
	}
	before := len(g.inputs[ids[0]])
	w.NotifySource(ids[0])
	complete(t, w)
	if len(g.inputs[ids[0]]) != before {
		t.Fatal("completed source replayed")
	}
	// The source progress is also the deletion receipt for compacted content.
	if err := s.DeleteObservationRecord(ctx, ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.GetEnrichmentProgress(ctx, ids[0]); err != nil || found {
		t.Fatal("deleted source compaction retained", found, err)
	}
}
