package durable

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/nielsgroen/blur/internal/capture"
	"github.com/nielsgroen/blur/internal/search"
	"github.com/nielsgroen/blur/internal/securestore"
)

const durableScaleBenchOptIn = "BLUR_RUN_SCALE_BENCH"
const durableRankedBenchOptIn = "BLUR_RUN_RANKED_BENCH"

const durableScaleQuerySamples = 20
const durableScaleReopenSamples = 5

func BenchmarkDurableExactIndexScale(b *testing.B) {
	if os.Getenv(durableScaleBenchOptIn) != "1" {
		b.Skip("set BLUR_RUN_SCALE_BENCH=1 to build the 100k/1M durable synthetic corpora")
	}
	for _, size := range []int{100_000, 1_000_000} {
		b.Run(fmt.Sprintf("records_%d", size), func(b *testing.B) {
			path := filepath.Join(b.TempDir(), "scale.db")
			key, err := securestore.ResolveKey(context.Background(), &testKeyBackend{}, "durable-scale-benchmark", bytes.NewReader(bytes.Repeat([]byte{0x9d}, 32)))
			if err != nil {
				b.Fatal(err)
			}
			store, err := OpenDurable(path, key, CreateNew)
			if err != nil {
				b.Fatal(err)
			}
			defer func() {
				if store != nil {
					_ = store.Close()
				}
			}()

			setupStart := time.Now()
			ingestDurations, err := seedDurableScaleCorpus(b, store, size)
			if err != nil {
				b.Fatal(err)
			}
			setupDuration := time.Since(setupStart)
			fileInfo, err := os.Stat(path)
			if err != nil {
				b.Fatal(err)
			}
			b.Logf("synthetic corpus records=%d setup=%s database_bytes=%d", size, setupDuration, fileInfo.Size())
			runtime.GC()
			var memory runtime.MemStats
			runtime.ReadMemStats(&memory)
			heapInuse := memory.HeapInuse

			query := search.ExactQuery{Terms: []string{"benchmark"}, TopK: 100, MaxRows: 1000, MaxBytes: search.MaxIndexBytes}
			request := search.Request{
				Query: "benchmark", ResultLimit: 100, SnippetBytes: 128,
				ResponseBytes: search.MaxResponseBytes, ScanLimit: 1,
			}
			durations := make([]time.Duration, 0, durableScaleQuerySamples)
			searchDurations := make([]time.Duration, 0, durableScaleQuerySamples)
			engine := search.New(store)
			b.ResetTimer()
			for i := 0; i < durableScaleQuerySamples; i++ {
				started := time.Now()
				page, err := store.SearchExact(context.Background(), query)
				durations = append(durations, time.Since(started))
				if err != nil {
					b.Fatal(err)
				}
				if page.Rows > query.MaxRows || len(page.Hits) != query.TopK || !page.HasMore {
					b.Fatalf("bounded common-term page rows=%d hits=%d has_more=%t", page.Rows, len(page.Hits), page.HasMore)
				}
			}
			for i := 0; i < durableScaleQuerySamples; i++ {
				started := time.Now()
				response, err := engine.Search(context.Background(), request)
				searchDurations = append(searchDurations, time.Since(started))
				if err != nil {
					b.Fatal(err)
				}
				if len(response.Results) != request.ResultLimit || !response.Truncated {
					b.Fatalf("bounded end-to-end search results=%d truncated=%t", len(response.Results), response.Truncated)
				}
			}
			b.StopTimer()
			sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
			middle := len(durations) / 2
			p50 := durations[middle]
			if len(durations)%2 == 0 {
				p50 = (durations[middle-1] + durations[middle]) / 2
			}
			p95 := int((len(durations) - 1) * 95 / 100)

			if err := store.Close(); err != nil {
				b.Fatal(err)
			}
			store = nil
			reopenDurations := make([]time.Duration, 0, durableScaleReopenSamples)
			for i := 0; i < durableScaleReopenSamples; i++ {
				started := time.Now()
				opened, err := OpenDurable(path, key, OpenExisting)
				reopenDurations = append(reopenDurations, time.Since(started))
				if err != nil {
					b.Fatal(err)
				}
				store = opened
				if err := store.Close(); err != nil {
					b.Fatal(err)
				}
				store = nil
			}
			b.ReportMetric(float64(size), "records")
			b.ReportMetric(float64(fileInfo.Size())/float64(1<<20), "database-MiB")
			b.ReportMetric(float64(setupDuration.Milliseconds()), "ingest-total-ms")
			reportScaleLatency(b, "ingest-batch", ingestDurations)
			b.ReportMetric(float64(heapInuse)/float64(1<<20), "heap-inuse-MiB")
			b.ReportMetric(float64(p50.Nanoseconds()), "index-p50-ns")
			b.ReportMetric(float64(durations[p95].Nanoseconds()), "index-p95-ns")
			reportScaleLatency(b, "search", searchDurations)
			b.ReportMetric(float64(len(durations)), "index-queries")
			b.ReportMetric(float64(len(searchDurations)), "search-queries")
			reportScaleLatency(b, "reopen", reopenDurations)
			b.ReportMetric(float64(len(reopenDurations)), "reopens")
		})
	}
}

func BenchmarkDurableExactIndexRankedOverCap(b *testing.B) {
	if os.Getenv(durableRankedBenchOptIn) != "1" {
		b.Skip("set BLUR_RUN_RANKED_BENCH=1 to build an over-cap ranked synthetic corpus")
	}
	const ordinaryCount = 1_024
	const rankedCount = 12
	path := filepath.Join(b.TempDir(), "ranked.db")
	key, err := securestore.ResolveKey(context.Background(), &testKeyBackend{}, "durable-ranked-benchmark", bytes.NewReader(bytes.Repeat([]byte{0x9c}, 32)))
	if err != nil {
		b.Fatal(err)
	}
	store, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()

	ids := make([]string, 0, rankedCount)
	observations := make([]capture.SanitizedObservation, 0, 4)
	writeBatches := 0
	writeBatch := func() {
		b.Helper()
		if len(observations) == 0 {
			return
		}
		writeBatches++
		batch := append([]capture.SanitizedObservation(nil), observations...)
		if err := store.Update(context.Background(), func(tx *Tx) error {
			for _, observation := range batch {
				if err := tx.PutObservation(observation); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			b.Fatalf("ranked corpus setup batch %d: %v", writeBatches, err)
		}
		observations = observations[:0]
	}
	for i := 0; i < ordinaryCount; i++ {
		observation, err := capture.SanitizeObservation("editor.app", "docs.example", fmt.Sprintf("alpha ordinary-%04d", i), 64)
		if err != nil {
			b.Fatal(err)
		}
		observations = append(observations, observation)
		if len(observations) == cap(observations) {
			writeBatch()
		}
	}
	writeBatch()
	for i := 0; i < rankedCount; i++ {
		observation, err := capture.SanitizeObservation("alpha.app", "docs.example", fmt.Sprintf("ranked item-%02d", i), 64)
		if err != nil {
			b.Fatal(err)
		}
		ids = append(ids, observation.ContentID())
		observations = append(observations, observation)
		if len(observations) == cap(observations) {
			writeBatch()
		}
	}
	writeBatch()
	sort.Strings(ids)
	query := search.ExactQuery{Terms: []string{"alpha"}, TopK: 10, MaxRows: search.MaxIndexRows, MaxBytes: search.MaxIndexBytes}
	durations := make([]time.Duration, 0, durableScaleQuerySamples)
	b.ResetTimer()
	for i := 0; i < durableScaleQuerySamples; i++ {
		started := time.Now()
		page, err := store.SearchExact(context.Background(), query)
		durations = append(durations, time.Since(started))
		if err != nil {
			b.Fatal(err)
		}
		if len(page.Hits) != query.TopK || !page.HasMore || page.Rows > query.MaxRows {
			b.Fatalf("bounded ranked page rows=%d hits=%d has_more=%t", page.Rows, len(page.Hits), page.HasMore)
		}
		for j, hit := range page.Hits {
			if hit.SourceID != ids[j] || hit.Score != 3 {
				b.Fatalf("ranked hit %d = %+v, want source %s score 3", j, hit, ids[j])
			}
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(ordinaryCount+rankedCount), "records")
	reportScaleLatency(b, "ranked-index", durations)
}

func seedDurableScaleCorpus(b *testing.B, store *Store, count int) ([]time.Duration, error) {
	const batchSize = 32 // measured below 1024 physical transaction operations
	const alphabet = "!.-_"
	durations := make([]time.Duration, 0, (count+batchSize-1)/batchSize)
	for offset := 0; offset < count; offset += batchSize {
		end := min(offset+batchSize, count)
		observations := make([]capture.SanitizedObservation, end-offset)
		for i := offset; i < end; i++ {
			value := i
			var suffix [10]byte // 4^10 distinct punctuation suffixes, none add a term
			for j := len(suffix) - 1; j >= 0; j-- {
				suffix[j] = alphabet[value&3]
				value >>= 2
			}
			observation, err := capture.SanitizeObservation("benchmark", "benchmark", "benchmark"+string(suffix[:]), 64)
			if err != nil {
				return nil, err
			}
			observations[i-offset] = observation
		}
		started := time.Now()
		if err := store.Update(context.Background(), func(tx *Tx) error {
			for _, observation := range observations {
				if err := tx.PutObservation(observation); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return nil, err
		}
		durations = append(durations, time.Since(started))
	}
	return durations, nil
}

func reportScaleLatency(b *testing.B, name string, samples []time.Duration) {
	b.Helper()
	if len(samples) == 0 {
		b.Fatal("scale benchmark has no latency samples")
	}
	ordered := append([]time.Duration(nil), samples...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	middle := len(ordered) / 2
	p50 := ordered[middle]
	if len(ordered)%2 == 0 {
		p50 = (ordered[middle-1] + ordered[middle]) / 2
	}
	p95 := (len(ordered)*95+99)/100 - 1 // nearest-rank percentile
	b.ReportMetric(float64(p50.Nanoseconds()), name+"-p50-ns")
	b.ReportMetric(float64(ordered[p95].Nanoseconds()), name+"-p95-ns")
}
