package client

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/search"
)

func TestAskPreservesCompleteAuthoredLibraryFact(t *testing.T) {
	c, _, _ := todayTaskClient(t)
	ctx := context.Background()
	note := strings.Repeat("Project context. ", 100) + "Rollback owner is Mira."
	if _, err := c.AddKnowledge(ctx, knowledge.Project, "Atlas", note); err != nil {
		t.Fatal(err)
	}

	got, err := c.AskMemory(ctx, "What do you remember about Atlas?")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Sources) != 1 || !strings.Contains(got.Sources[0].Text, note) {
		t.Fatalf("authored fact disappeared from inspectable evidence: sources=%+v", got.Sources)
	}
	if !strings.Contains(got.Answer.Text, "Rollback owner is Mira.") && !got.CoverageLimited {
		t.Fatalf("fact tail missing from answer without excerpt coverage: answer=%q limited=%v", got.Answer.Text, got.CoverageLimited)
	}
}

func TestLibraryFactsUseAvailableContextBeforeExcerpting(t *testing.T) {
	c := NewClient(&testData{})
	defer c.Close()
	fact := strings.Repeat("Project context. ", 100) + "Rollback owner is Mira."
	for _, authored := range []bool{false, true} {
		t.Run(fmt.Sprintf("authored_%v", authored), func(t *testing.T) {
			source := knowledge.Source{UserAuthored: authored}
			if !authored {
				source.ID = strings.Repeat("a", 64)
			}
			entity := knowledge.Entity{ID: "atlas", Name: "Atlas", Kind: knowledge.Project, State: knowledge.Confirmed, Facts: []knowledge.Fact{{
				ID: "fact", Text: fact, State: knowledge.Confirmed, Sources: []knowledge.Source{source},
			}}}
			evidence, err := c.inferredEntitiesEvidence(context.Background(), assistant.Plan{}, []knowledge.Entity{entity}, false)
			if err != nil {
				t.Fatal(err)
			}
			packed, err := assistant.PackContext("What do you remember about Atlas?", evidence)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(packed.User, "Rollback owner is Mira.") || packed.Truncated != 0 {
				t.Fatalf("available context lost Library fact tail: truncated=%d context=%s", packed.Truncated, packed.User)
			}
		})
	}
}

func TestAskPreservesLargeStoredLibraryEntity(t *testing.T) {
	c, store, _ := todayTaskClient(t)
	ctx := context.Background()
	entity := knowledge.Entity{ID: "atlas", Name: "Atlas", Kind: knowledge.Project, State: knowledge.Confirmed}
	for i := 0; i < knowledge.MaxFacts; i++ {
		fact := fmt.Sprintf("Fact %03d: ", i) + strings.Repeat("a", 3880) + fmt.Sprintf(" tail %03d", i)
		loop := fmt.Sprintf("Loop %03d: ", i) + strings.Repeat("b", 3880) + fmt.Sprintf(" tail %03d", i)
		entity.Facts = append(entity.Facts, knowledge.Fact{ID: fmt.Sprintf("fact-%03d", i), Text: fact, State: knowledge.Confirmed, Sources: []knowledge.Source{{UserAuthored: true}}})
		entity.OpenLoops = append(entity.OpenLoops, knowledge.OpenLoop{ID: fmt.Sprintf("loop-%03d", i), Text: loop, State: knowledge.Confirmed, Sources: []knowledge.Source{{UserAuthored: true}}})
	}
	if err := store.PutKnowledgeEntity(ctx, entity); err != nil {
		t.Fatal(err)
	}
	got, err := c.AskMemory(ctx, "What do you remember about Atlas?")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Sources) != 1 || !got.CoverageLimited || got.Truncated == 0 {
		t.Fatalf("large entity failed normal excerpt coverage: sources=%d limited=%v truncated=%d", len(got.Sources), got.CoverageLimited, got.Truncated)
	}
	for i := range entity.Facts {
		if !strings.Contains(got.Sources[0].Text, entity.Facts[i].Text) || !strings.Contains(got.Sources[0].Text, entity.OpenLoops[i].Text) {
			t.Fatalf("inspectable evidence lost fact or open loop %d", i)
		}
	}
}

func TestLiteralAskReportsExcludedExactMatches(t *testing.T) {
	for _, matchCount := range []int{8, 9} {
		t.Run(fmt.Sprintf("%d_matches", matchCount), func(t *testing.T) {
			now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
			d := &testData{
				observations:  map[string]capture.SanitizedObservation{},
				observationAt: map[string]time.Time{},
			}
			for i := 0; i < matchCount; i++ {
				o, err := capture.SanitizeObservation("editor.app", "", fmt.Sprintf("launch_ready evidence %d", i), 4096)
				if err != nil {
					t.Fatal(err)
				}
				d.observations[o.ContentID()] = o
				d.observationAt[o.ContentID()] = now
				d.page.Hits = append(d.page.Hits, search.IndexHit{SourceID: o.ContentID(), Bytes: len(o.Text()) + len(o.AppID()), MatchedTerms: 1})
			}
			c := NewClient(d)
			defer c.Close()
			c.askNow = func() time.Time { return now }

			got, err := c.AskMemory(context.Background(), "Find launch_ready")
			if err != nil {
				t.Fatal(err)
			}
			if !got.Plan.Literal || len(got.Sources) != 8 {
				t.Fatalf("unexpected exact lookup: literal=%v sources=%d", got.Plan.Literal, len(got.Sources))
			}
			if want := matchCount > 8; got.CoverageLimited != want {
				t.Fatalf("excluded exact matches misreported: matches=%d sources=%d limited=%v want=%v", matchCount, len(got.Sources), got.CoverageLimited, want)
			}
		})
	}
}
