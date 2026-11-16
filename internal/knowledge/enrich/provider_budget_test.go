package enrich_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/hazcod/blur/internal/knowledge/derive"
	"github.com/hazcod/blur/internal/knowledge/enrich"
	"github.com/hazcod/blur/internal/power"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

type runtimeGenerator struct {
	generator
	tokens int
}

func (*runtimeGenerator) ContextTokenLimit(context.Context) (int, error) { return 4096, nil }
func (g *runtimeGenerator) CountInputTokens(context.Context, []derive.Evidence) (int, error) {
	return g.tokens, nil
}

func TestLibraryRuntimeCapacityDoesNotBecomePowerWork(t *testing.T) {
	for _, test := range []struct {
		name   string
		tokens int
		mode   power.Mode
		denied bool
	}{
		{"small balanced job", 512, power.Balanced, false},
		{"context overflow", 4000, power.Performance, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
			if err != nil {
				t.Fatal(err)
			}
			defer func(close func() error) { _ = close() }(s.Close)
			source(t, s, "TAIL-ATLAS: Atlas is a private local project.")
			g := &runtimeGenerator{tokens: test.tokens}
			worker, err := enrich.NewService(s, s, g, enrich.Options{Policy: policy{test.mode}})
			if err != nil {
				t.Fatal(err)
			}
			defer worker.Close()
			err = worker.Backfill(context.Background())
			if test.denied {
				if !errors.Is(err, enrich.ErrCapacity) || g.calls != 0 {
					t.Fatal("overflow reached generation", err, g.calls)
				}
			} else if err != nil || g.calls != 1 {
				t.Fatal("small 4096-context Library job denied under Balanced", err, g.calls)
			}
		})
	}
}
