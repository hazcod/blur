package durable

import (
	"bytes"
	"context"
	"crypto/sha256"
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
	"github.com/nielsgroen/blur/internal/storage/physical/bolt"
)

const durableScaleBenchOptIn = "BLUR_RUN_SCALE_BENCH"
const durableRankedBenchOptIn = "BLUR_RUN_RANKED_BENCH"
const durableBranchCascadeBenchOptIn = "BLUR_RUN_BRANCH_CASCADE_BENCH"

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

func BenchmarkDurableIndexPackedBranchSplitCascade(b *testing.B) {
	if os.Getenv(durableBranchCascadeBenchOptIn) != "1" {
		b.Skip("set BLUR_RUN_BRANCH_CASCADE_BENCH=1 to measure a packed non-root branch split")
	}
	const samples = durableScaleQuerySamples
	path := filepath.Join(b.TempDir(), "branch-cascade.db")
	key, err := securestore.ResolveKey(context.Background(), &testKeyBackend{}, "durable-branch-cascade-benchmark", bytes.NewReader(bytes.Repeat([]byte{0x9e}, 32)))
	if err != nil {
		b.Fatal(err)
	}
	store, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()

	const kind = searchIndexTerm
	digest := store.searchLookupDigest(kind, "branch-cascade-benchmark")
	defer clear(digest[:])
	var root [sha256.Size]byte
	branchID, err := newSearchIndexTreeNodeID()
	if err != nil {
		b.Fatal(err)
	}
	orderFor := func(n int) [searchIndexTreeOrderBytes]byte {
		b.Helper()
		encoded, err := searchIndexOrderKey(kind, 1, fmt.Sprintf("%064x", n))
		if err != nil {
			b.Fatal(err)
		}
		var ordered [searchIndexTreeOrderBytes]byte
		copy(ordered[:], encoded)
		clear(encoded)
		return ordered
	}
	children := make([]searchIndexTreeChild, searchIndexTreeMaxChildren)
	for i := range children {
		if i == len(children)-1 {
			children[i].upper = searchIndexTreeInfinity
		} else {
			children[i].upper = orderFor(searchIndexPageRows + i)
		}
		children[i].id = store.searchIndexTreeChildID(kind, digest, branchID, children[i].upper, searchIndexTreeLeaf, byte(i&1))
	}
	firstLeafID := children[0].id
	branch := searchIndexTreeNode{children: children}
	firstPostings := make([]searchIndexPosting, searchIndexPageRows)
	for i := range firstPostings {
		firstPostings[i] = searchIndexPosting{sourceID: fmt.Sprintf("%064x", i+1), fields: 1}
	}
	if err := store.Update(context.Background(), func(tx *Tx) error {
		if err := tx.putSearchIndexTreeNodeLocked(kind, digest, firstLeafID, searchIndexTreeNode{leaf: true, postings: firstPostings}, nil); err != nil {
			return err
		}
		if err := tx.putSearchIndexTreeNodeLocked(kind, digest, branchID, branch, nil); err != nil {
			return err
		}
		return tx.putSearchIndexTreeNodeLocked(kind, digest, root, searchIndexTreeNode{
			children: []searchIndexTreeChild{{upper: searchIndexTreeInfinity, id: branchID}},
		}, nil)
	}); err != nil {
		b.Fatalf("install maximum packed branch fixture: %v", err)
	}
	newID := fmt.Sprintf("%064x", 0)
	newOrder, err := searchIndexOrderKey(kind, 1, newID)
	if err != nil {
		b.Fatal(err)
	}
	defer clear(newOrder)
	manifestBytes, err := readManifestEnvelopeBytes(store)
	if err != nil {
		b.Fatal(err)
	}
	durations := make([]time.Duration, 0, samples)
	lastOperations, lastBytes := 0, 0
	b.ResetTimer()
	for sample := 0; sample < samples; sample++ {
		if sample > 0 {
			b.StopTimer()
			if err := resetDurableBranchCascadeFixture(store, kind, digest, branchID, firstLeafID, branch, firstPostings); err != nil {
				b.Fatalf("reset branch cascade fixture %d: %v", sample, err)
			}
			manifestBytes, err = readManifestEnvelopeBytes(store)
			if err != nil {
				b.Fatal(err)
			}
			b.StartTimer()
		}
		var counting *operationCountingWriter
		started := time.Now()
		err := store.Update(context.Background(), func(tx *Tx) error {
			counting = &operationCountingWriter{Writer: tx.writer}
			tx.writer = counting
			return tx.insertSearchIndexTreeLocked(kind, digest, newOrder, searchIndexPosting{sourceID: newID, fields: 1})
		})
		durations = append(durations, time.Since(started))
		if err != nil {
			b.Fatalf("packed branch split sample %d: %v", sample, err)
		}
		lastOperations = counting.operations + 1
		lastBytes = counting.bytes + len(manifestKey) + manifestBytes
		if lastOperations >= bolt.MaxTransactionOps || lastBytes >= bolt.MaxTransactionBytes {
			b.Fatalf("branch split exceeded transaction caps: ops=%d bytes=%d", lastOperations, lastBytes)
		}
	}
	b.StopTimer()
	if err := store.view(context.Background(), func(reader bolt.Reader) error {
		left, found, err := readSearchIndexTreeNodeFromReader(reader, store, kind, digest, branchID)
		if err != nil || !found || len(left.children) != searchIndexTreeMaxChildren/2 || left.rightSibling == ([sha256.Size]byte{}) {
			return fmt.Errorf("measured cascade did not leave one deferred split: found=%t children=%d link=%x err=%v", found, len(left.children), left.rightSibling[:4], err)
		}
		return nil
	}); err != nil {
		b.Fatal(err)
	}
	reportScaleLatency(b, "branch-split", durations)
	b.ReportMetric(float64(samples), "samples")
	b.ReportMetric(float64(lastOperations), "writer-ops")
	b.ReportMetric(float64(lastBytes), "tx-bytes")
}

func resetDurableBranchCascadeFixture(store *Store, kind byte, digest [sha256.Size]byte, branchID, firstLeafID [sha256.Size]byte, branch searchIndexTreeNode, postings []searchIndexPosting) error {
	return store.Update(context.Background(), func(tx *Tx) error {
		left, leftEnvelope, found, err := tx.readSearchIndexTreeNodeLocked(kind, digest, branchID)
		if err != nil || !found || left.rightSibling == ([sha256.Size]byte{}) || len(left.children) < 2 {
			clear(leftEnvelope)
			if err != nil {
				return err
			}
			return ErrCorrupt
		}
		defer clear(leftEnvelope)
		rightID, splitLeafID := left.rightSibling, left.children[1].id
		for _, nodeID := range [][sha256.Size]byte{rightID, splitLeafID} {
			_, envelope, found, err := tx.readSearchIndexTreeNodeLocked(kind, digest, nodeID)
			if err != nil || !found {
				clear(envelope)
				if err != nil {
					return err
				}
				return ErrCorrupt
			}
			key := store.searchIndexTreeKey(kind, digest, nodeID)
			if err := tx.deleteKnownEntryLocked(key, envelope); err != nil {
				clear(key)
				clear(envelope)
				return err
			}
			clear(key)
			clear(envelope)
		}
		_, leafEnvelope, found, err := tx.readSearchIndexTreeNodeLocked(kind, digest, firstLeafID)
		if err != nil || !found {
			clear(leafEnvelope)
			if err != nil {
				return err
			}
			return ErrCorrupt
		}
		defer clear(leafEnvelope)
		if err := tx.putSearchIndexTreeNodeLocked(kind, digest, firstLeafID, searchIndexTreeNode{leaf: true, postings: postings}, leafEnvelope); err != nil {
			return err
		}
		return tx.putSearchIndexTreeNodeLocked(kind, digest, branchID, branch, leftEnvelope)
	})
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
