package chronicle

import (
	"context"
	"errors"
	"testing"

	"github.com/hazcod/blur/internal/power"
)

type boundedWorkerModel struct{ *workerModel }

func (*boundedWorkerModel) ContextTokenLimit(context.Context) (int, error) { return 4096, nil }

func TestBackgroundRuntimeCapacityDoesNotBecomePowerWork(t *testing.T) {
	for _, test := range []struct {
		name   string
		tokens int
		policy power.Effective
		denied bool
	}{
		{"small balanced job", 512, balanced(), false},
		{"context overflow", 4000, performance(), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := &workerRepo{rows: map[string]Summary{}}
			g := &boundedWorkerModel{&workerModel{count: test.tokens}}
			w := NewWorker(repo, &workerPolicy{value: test.policy}, g, WorkerConfig{Enabled: true, Provider: power.LocalProvider})
			defer w.Close()
			packed, err := PackHour(workerHour(t))
			if err != nil {
				t.Fatal(err)
			}
			err = w.process(context.Background(), packed)
			if test.denied {
				if !errors.Is(err, ErrWorkerPolicy) || g.callCount() != 0 {
					t.Fatal("overflow reached generation", err, g.callCount())
				}
			} else if err != nil || g.callCount() != 1 {
				t.Fatal("small 4096-context job denied under Balanced", err, g.callCount())
			}
		})
	}
}
