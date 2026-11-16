package semantic_test

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/model"
	"github.com/hazcod/blur/internal/semantic"
)

type candidateEmbedder struct {
	identities atomic.Int32
	revised    atomic.Bool
	mu         sync.Mutex
	question   string
	batches    [][]string
	query      func(context.Context) error
	documents  func(context.Context, []string) ([][]float32, error)
}

func (e *candidateEmbedder) Identity(context.Context) (semantic.Model, error) {
	e.identities.Add(1)
	digest := "a"
	if e.revised.Load() {
		digest = "b"
	}
	return semantic.Model{Name: "fixture:latest", Digest: strings.Repeat(digest, 64), Dimensions: 2, Format: semantic.FormatPlain}, nil
}
func (e *candidateEmbedder) EmbedQuery(ctx context.Context, _ semantic.Model, question string) ([]float32, error) {
	e.mu.Lock()
	e.question = question
	e.mu.Unlock()
	if e.query != nil {
		if err := e.query(ctx); err != nil {
			return nil, err
		}
	}
	return []float32{1, 0}, nil
}
func (e *candidateEmbedder) EmbedDocuments(ctx context.Context, _ semantic.Model, texts []string) ([][]float32, error) {
	e.mu.Lock()
	e.batches = append(e.batches, append([]string(nil), texts...))
	e.mu.Unlock()
	if e.documents != nil {
		return e.documents(ctx, texts)
	}
	vectors := make([][]float32, len(texts))
	for i, text := range texts {
		vectors[i] = []float32{0, 1}
		if strings.Contains(text, "reservation") {
			vectors[i] = []float32{1, 0}
		}
		if text == "opposite" {
			vectors[i] = []float32{-1, 0}
		}
	}
	return vectors, nil
}
func candidateService(t *testing.T, e *candidateEmbedder, options semantic.Options) *semantic.Service {
	t.Helper()
	store := serviceStore(t)
	s, err := semantic.NewService(store, store, e, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.Backfill(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestServiceRankCandidatesKeepsInputOrderAndFullQuestion(t *testing.T) {
	e := &candidateEmbedder{}
	s := candidateService(t, e, semantic.Options{})
	question := "  Where should I go for a meal with my partner on Friday evening?  "
	scores, err := s.RankCandidates(context.Background(), question, []string{"unrelated", "a reservation for two", "opposite", "", " \n "})
	if err != nil || len(scores) != 5 {
		t.Fatalf("scores=%v err=%v", scores, err)
	}
	for i, want := range []float32{0, 1, -1, -1, -1} {
		if scores[i] != want {
			t.Errorf("candidate %d score=%g want=%g", i, scores[i], want)
		}
	}
	if e.question != question {
		t.Errorf("full phrase changed to %q", e.question)
	}
	if e.identities.Load() != 1 {
		t.Errorf("ranking independently resolved a model generation: %d", e.identities.Load())
	}
}

func TestServiceRankCandidatesCoversWholeNotesWithBoundedBatches(t *testing.T) {
	e := &candidateEmbedder{}
	s := candidateService(t, e, semantic.Options{})
	texts := make([]string, 10)
	for i := range texts {
		texts[i] = strings.Repeat("neutral café ", 1100) + "reservation"
	}
	scores, err := s.RankCandidates(context.Background(), "dinner plans", texts)
	if err != nil || len(scores) != len(texts) {
		t.Fatalf("scores=%v err=%v", scores, err)
	}
	for i, score := range scores {
		if score != 1 {
			t.Errorf("candidate %d tail was lost: %g", i, score)
		}
	}
	if len(e.batches) < 2 {
		t.Fatal("long notes were embedded in one unbounded request")
	}
	for _, batch := range e.batches {
		used := 0
		for _, text := range batch {
			used += len(text)
			if len(text) > semantic.ChunkBytes {
				t.Fatalf("oversized note fragment: %d bytes", len(text))
			}
		}
		if len(batch) > 8 || used > model.MaxEmbeddingInputBytes {
			t.Fatalf("unbounded batch: %d inputs, %d bytes", len(batch), used)
		}
	}
}

func TestServiceRankCandidatesRejectsInvalidInputBeforeEmbedding(t *testing.T) {
	e := &candidateEmbedder{}
	s := candidateService(t, e, semantic.Options{})
	cases := []struct {
		question string
		texts    []string
	}{
		{"", []string{"reservation"}},
		{strings.Repeat("q", 513), []string{"reservation"}},
		{"question\x00", []string{"reservation"}},
		{"question", []string{"bad\xff"}},
		{"question", []string{"bad\x00"}},
		{"question", []string{strings.Repeat("a", (16<<10)+1)}},
		{"question", make([]string, 257)},
	}
	oversizedTotal := make([]string, 65)
	for i := range oversizedTotal {
		oversizedTotal[i] = strings.Repeat("a", 16<<10)
	}
	cases = append(cases, struct {
		question string
		texts    []string
	}{"question", oversizedTotal})
	for _, test := range cases {
		if got, err := s.RankCandidates(context.Background(), test.question, test.texts); !errors.Is(err, semantic.ErrInvalid) || got != nil {
			t.Fatalf("invalid input produced scores=%v err=%v", got, err)
		}
	}
	if len(e.batches) != 0 || e.question != "" {
		t.Fatal("invalid input reached the embedding provider")
	}
}

func TestServiceRankCandidatesRejectsWrongProviderResultCount(t *testing.T) {
	e := &candidateEmbedder{documents: func(context.Context, []string) ([][]float32, error) {
		return [][]float32{{1, 0}}, nil
	}}
	s := candidateService(t, e, semantic.Options{})
	if got, err := s.RankCandidates(context.Background(), "dinner", []string{"first", "second"}); !errors.Is(err, semantic.ErrInvalid) || got != nil {
		t.Fatalf("partial scores were returned: scores=%v err=%v", got, err)
	}
}

func TestServiceRankCandidatesRejectsInvalidProviderVectors(t *testing.T) {
	for _, vector := range [][]float32{{1}, {0, 0}, {2, 0}, {float32(math.NaN()), 0}, {float32(math.Inf(1)), 0}} {
		t.Run("vector", func(t *testing.T) {
			e := &candidateEmbedder{documents: func(context.Context, []string) ([][]float32, error) { return [][]float32{vector}, nil }}
			s := candidateService(t, e, semantic.Options{})
			if got, err := s.RankCandidates(context.Background(), "dinner", []string{"reservation"}); !errors.Is(err, semantic.ErrInvalid) || got != nil {
				t.Fatalf("malformed provider vector accepted: scores=%v err=%v", got, err)
			}
		})
	}
}

func TestServiceRankCandidatesDropsLateScoresAfterInvalidation(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	e := &candidateEmbedder{documents: func(context.Context, []string) ([][]float32, error) {
		close(entered)
		<-release
		return [][]float32{{1, 0}}, nil
	}}
	s := candidateService(t, e, semantic.Options{})
	done := make(chan error, 1)
	go func() {
		scores, err := s.RankCandidates(context.Background(), "dinner", []string{"reservation"})
		if len(scores) != 0 {
			done <- errors.New("invalidated scores were published")
			return
		}
		done <- err
	}()
	<-entered
	s.Invalidate()
	close(release)
	if err := <-done; !errors.Is(err, semantic.ErrUnavailable) && !errors.Is(err, context.Canceled) {
		t.Fatalf("late scores err=%v", err)
	}
}

func TestServiceRankCandidatesDropsLateScoresAfterModelGenerationChanges(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	e := &candidateEmbedder{documents: func(context.Context, []string) ([][]float32, error) {
		close(entered)
		<-release
		return [][]float32{{1, 0}}, nil
	}}
	s := candidateService(t, e, semantic.Options{})
	done := make(chan error, 1)
	go func() {
		scores, err := s.RankCandidates(context.Background(), "dinner", []string{"reservation"})
		if len(scores) != 0 {
			done <- errors.New("superseded generation scores were published")
			return
		}
		done <- err
	}()
	<-entered
	e.revised.Store(true)
	if err := s.Backfill(context.Background()); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	if err := <-done; !errors.Is(err, semantic.ErrUnavailable) {
		t.Fatalf("superseded generation scores err=%v", err)
	}
}

func TestServiceRankCandidatesChecksReadGateAfterEmbedding(t *testing.T) {
	var readable atomic.Bool
	readable.Store(true)
	e := &candidateEmbedder{documents: func(context.Context, []string) ([][]float32, error) {
		readable.Store(false)
		return [][]float32{{1, 0}}, nil
	}}
	s := candidateService(t, e, semantic.Options{CanRead: readable.Load})
	if got, err := s.RankCandidates(context.Background(), "dinner", []string{"reservation"}); !errors.Is(err, semantic.ErrUnavailable) || got != nil {
		t.Fatalf("read gate allowed scores=%v err=%v", got, err)
	}
}

func TestServiceCloseCancelsAndDrainsCandidateRanking(t *testing.T) {
	entered := make(chan struct{})
	e := &candidateEmbedder{query: func(ctx context.Context) error { close(entered); <-ctx.Done(); return ctx.Err() }}
	s := candidateService(t, e, semantic.Options{})
	done := make(chan error, 1)
	go func() {
		_, err := s.RankCandidates(context.Background(), "dinner", []string{"reservation"})
		done <- err
	}()
	<-entered
	s.Close()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ranking did not cancel: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close returned before candidate ranking drained")
	}
}
