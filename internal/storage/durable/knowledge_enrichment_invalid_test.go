package durable

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/knowledge/derive"
	"github.com/hazcod/blur/internal/knowledge/enrich"
)

func TestInvalidEnrichmentReceiptRequiresExplicitRepairAndNeverPublishesClaims(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xdf), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	valid := enrichmentFixture(t, s, "work.example", "Atlas has an offline export workflow.", derive.Proposal{Kind: knowledge.Project, Name: "Atlas", Fact: "Atlas has an offline export workflow."})
	failed := valid
	failed.Declined, failed.FailureCode, failed.Proposals = true, enrich.FailureInvalidOutput, nil
	for range 2 {
		if err := s.ApplyEnrichment(ctx, failed); err != nil {
			t.Fatal("exact failed-outcome receipt must be idempotent", err)
		}
	}
	progress, found, err := s.GetEnrichmentProgress(ctx, failed.SourceID)
	if err != nil || !found || !progress.Declined || progress.FailureCode != enrich.FailureInvalidOutput || progress.Complete || progress.Next != 0 || len(progress.Entities) != 0 {
		t.Fatal("failed output authorized derived facts or claimed coverage", err)
	}
	if err := s.ApplyEnrichment(ctx, valid); !errors.Is(err, enrich.ErrStale) {
		t.Fatal("normal work overrode failed receipt without explicit repair", err)
	}
	wrong := failed
	wrong.FailureCode = ""
	if err := s.ApplyEnrichment(ctx, wrong); !errors.Is(err, enrich.ErrStale) {
		t.Fatal("failed outcome silently changed into a provider refusal", err)
	}
	wrong = valid
	wrong.FailureCode = enrich.FailureInvalidOutput
	if err := s.ApplyEnrichment(ctx, wrong); !errors.Is(err, enrich.ErrInvalid) {
		t.Fatal("failure code authorized valid-looking proposals", err)
	}
	valid.RetryInvalid = true
	if err := s.ApplyEnrichment(ctx, valid); err != nil {
		t.Fatal("explicit retry could not replace failed output with validated claims", err)
	}
	progress, found, err = s.GetEnrichmentProgress(ctx, valid.SourceID)
	if err != nil || !found || progress.Declined || progress.FailureCode != "" || !progress.Complete || len(progress.Entities) != 1 {
		t.Fatal("successful explicit retry retained failure or lost claims", err)
	}
	for _, code := range []string{"unknown", strings.Repeat("x", 4096)} {
		progress.Declined, progress.Complete, progress.FailureCode = true, false, code
		if enrich.ValidProgress(progress) {
			t.Fatal("unbounded or unknown outcome code accepted")
		}
	}
}
