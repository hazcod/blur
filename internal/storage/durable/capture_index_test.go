package durable

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/search"
)

type captureWorkForTest interface {
	CaptureIndexPending(context.Context) (bool, error)
	IndexPendingCaptures(context.Context, int) (bool, error)
}

func captureWorkerForTest(t *testing.T, s *Store) captureWorkForTest {
	t.Helper()
	worker, ok := any(s).(captureWorkForTest)
	if !ok {
		t.Fatal("published captures have no resumable part index worker")
	}
	return worker
}
func drainCaptureIndex(t *testing.T, s *Store) {
	t.Helper()
	worker := captureWorkerForTest(t, s)
	for i := 0; i < 256; i++ {
		more, err := worker.IndexPendingCaptures(context.Background(), 16)
		if err != nil {
			t.Fatal("capture indexing failed", err)
		}
		if !more {
			return
		}
	}
	t.Fatal("bounded fixture did not finish capture indexing")
}
func publishIndexedFixture(t *testing.T, s *Store, text string, at time.Time) capture.CaptureSummary {
	t.Helper()
	h, p, summary := makeStoredCapture(t, text, at)
	stageStoredCapture(t, s, h, p)
	if _, err := s.PublishCapture(context.Background(), summary); err != nil {
		t.Fatal(err)
	}
	return summary
}
func partIndexCiphertext(t *testing.T, s *Store) map[string][]byte {
	t.Helper()
	rows := scopeCiphertext(t, s, 0x41)
	// New visits legitimately alter the separate time tree (kind 0x13).
	for key := range rows {
		if len(key) > 1 && key[1] == 0x13 {
			delete(rows, key)
		}
	}
	return rows
}
func equalCiphertext(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if !bytes.Equal(value, b[key]) {
			return false
		}
	}
	return true
}

func TestPublishedCaptureIndexQueuesOnlyCommittedEditionsAndResumesTailAfterRestart(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0xd1)
	s, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	worker := captureWorkerForTest(t, s)
	input := strings.Repeat("Ordered Unicode 日本語🙂 evidence.\n", 40000) + "password:\"private-index-fixture\"\nTAIL_FACT: rollback owner is Mira."
	h, p, summary := makeStoredCapture(t, input, time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC))
	stageStoredCapture(t, s, h, p)
	if pending, err := worker.CaptureIndexPending(ctx); err != nil || pending {
		t.Fatal("private staging queued searchable work", pending, err)
	}
	if _, err := s.publishCaptureWithHook(ctx, summary, func() error { return context.Canceled }); !errors.Is(err, context.Canceled) {
		t.Fatal("publication did not roll back", err)
	}
	if pending, err := worker.CaptureIndexPending(ctx); err != nil || pending {
		t.Fatal("aborted publication leaked work", pending, err)
	}
	if _, err := s.PublishCapture(ctx, summary); err != nil {
		t.Fatal(err)
	}
	if pending, err := worker.CaptureIndexPending(ctx); err != nil || !pending {
		t.Fatal("published capture has no durable work receipt", pending, err)
	}
	more, err := worker.IndexPendingCaptures(ctx, 1)
	if err != nil || !more {
		t.Fatal("one bounded round claimed the whole large capture", more, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal("partial-index manifest failed to reopen", err)
	}
	drainCaptureIndex(t, s)
	page, err := s.capturePartIndex().SearchExact(ctx, scopedQuery("mira"))
	if err != nil || len(page.Hits) != 1 || page.Hits[0].Score != 1 {
		t.Fatal("complete saved capture tail was not indexed", page, err)
	}
	if len(page.Hits[0].SourceID) != 64 || page.Hits[0].SourceID == summary.ContentID() {
		t.Fatal("part identity was confused with edition identity")
	}
	disk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(disk, []byte("rollback owner")) || bytes.Contains(disk, []byte("private-index-fixture")) || bytes.Contains(disk, []byte(strings.Repeat("d", 64))) {
		t.Fatal("capture index persisted plaintext fixture or locator")
	}
}

func TestPublishedCaptureIndexDuplicateVisitsReusePartCiphertext(t *testing.T) {
	ctx := context.Background()
	s := openReplicaTestStore(t)
	worker := captureWorkerForTest(t, s)
	text := "Mira owns rollback. A saved capture is revisited."
	at := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	first := publishIndexedFixture(t, s, text, at)
	drainCaptureIndex(t, s)
	before := partIndexCiphertext(t, s)
	if len(before) < 3 {
		t.Fatal("fixture did not create a part index")
	}
	second := publishIndexedFixture(t, s, text, at.Add(25*time.Hour))
	if first.ContentID() != second.ContentID() {
		t.Fatal("unchanged content changed edition identity")
	}
	if pending, err := worker.CaptureIndexPending(ctx); err != nil || pending {
		t.Fatal("unchanged text was queued again", pending, err)
	}
	if !equalCiphertext(before, partIndexCiphertext(t, s)) {
		t.Fatal("new visit rewrote content index records")
	}
	visits, err := s.ListCaptureVisits(ctx, first.ContentID(), "", 64)
	if err != nil || len(visits.Visits) != 2 || !visits.Visits[1].CapturedAt.Equal(at.Add(25*time.Hour)) {
		t.Fatal("new visit was lost", visits, err)
	}
}

func TestPublishedCaptureIndexDensePartUsesPagedTermsAndDeletesSourcePostingsAndMembership(t *testing.T) {
	ctx := context.Background()
	s := openReplicaTestStore(t)
	worker := captureWorkerForTest(t, s)
	var text strings.Builder
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&text, "token%03d ", i)
	}
	summary := publishIndexedFixture(t, s, text.String(), time.Now().UTC())
	if more, err := worker.IndexPendingCaptures(ctx, 1); err != nil || !more {
		t.Fatal("dense part ignored bounded term work", more, err)
	}
	drainCaptureIndex(t, s)
	page, err := s.capturePartIndex().SearchExact(ctx, scopedQuery("token399"))
	if err != nil || len(page.Hits) != 1 {
		t.Fatal("term past inline threshold is missing", page, err)
	}
	sourceID := page.Hits[0].SourceID
	if err := s.DeleteObservationRecord(ctx, summary.ContentID()); err != nil {
		t.Fatal("generic source deletion skipped indexed capture", err)
	}
	page, err = s.capturePartIndex().SearchExact(ctx, scopedQuery("token399"))
	if err != nil || len(page.Hits) != 0 {
		t.Fatal("deleted capture retained postings", page, err)
	}
	rows := scopeCiphertext(t, s, 0x41)

	for key, encoded := range rows {
		if len(key) == 2 && key[1] == 0 {
			continue
		}
		kind := key[1]
		if kind != searchIndexTerm && kind != searchIndexApp && kind != searchIndexDomain && kind != captureIndexVisit {
			t.Fatal("deleted capture retained a source reverse/proof/work row")
		}
		scoped := s.capturePartIndex()
		plain, err := scoped.open([]byte(key), scoped.exactNamespace("search-index-tree-v4"), encoded)
		if err != nil {
			t.Fatal(err)
		}
		node, err := decodeSearchIndexTreeNode(plain, kind)
		if err != nil || len(node.postings) != 0 {
			t.Fatal("deleted capture retained searchable source postings", err)
		}
	}
	if _, found, err := s.readIndexValue(ctx, captureNS, "index-part:"+sourceID, 512); err != nil || found {
		t.Fatal("deleted capture retained part membership", found, err)
	}
	if pending, err := worker.CaptureIndexPending(ctx); err != nil || pending {
		t.Fatal("deleted capture left resumable work", pending, err)
	}
}

func TestPublishedCaptureIndexRequiresWriterSeven(t *testing.T) {
	s := openReplicaTestStore(t)
	_ = captureWorkerForTest(t, s)
	if OpenSchemaVersion != 7 {
		t.Fatal("capture index erasure has no newer writer boundary")
	}
	if err := s.readIdentityWithSchemaLimit(context.Background(), 6); !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatal("old schema6 writer accepted new index data", err)
	}
}

func TestPublishedCaptureIndexRejectsInvalidWorkBudgetsAndCancellation(t *testing.T) {
	s := openReplicaTestStore(t)
	w := captureWorkerForTest(t, s)
	for _, n := range []int{-1, 0, 1025} {
		if _, err := w.IndexPendingCaptures(context.Background(), n); !errors.Is(err, search.ErrInvalidRequest) {
			t.Fatal("invalid work round budget admitted", n, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := w.IndexPendingCaptures(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled worker ran", err)
	}
}
