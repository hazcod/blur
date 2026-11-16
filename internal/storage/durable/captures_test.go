package durable

import (
	"context"
	"errors"
	"fmt"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func makeStoredCapture(t *testing.T, text string, at time.Time) (capture.CaptureHeader, []capture.CapturePart, capture.CaptureSummary) {
	t.Helper()
	b, err := capture.NewCaptureBuilder("com.example.reader", "example.test", strings.Repeat("d", 64), at)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Abort()
	var parts []capture.CapturePart
	sink := func(_ context.Context, p []capture.CapturePart) error { parts = append(parts, p...); return nil }
	for len(text) > 0 {
		n := min(len(text), 4096)
		for n > 0 && n < len(text) && !utf8.RuneStart(text[n]) {
			n--
		}
		if err := b.Write(context.Background(), text[:n], sink); err != nil {
			t.Fatal(err)
		}
		text = text[n:]
	}
	s, err := b.Finish(context.Background(), capture.CaptureComplete, sink)
	if err != nil {
		t.Fatal(err)
	}
	return b.Header(), parts, s
}
func stageStoredCapture(t *testing.T, s *Store, h capture.CaptureHeader, p []capture.CapturePart) {
	t.Helper()
	if err := s.BeginCapture(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendCaptureParts(context.Background(), h, p); err != nil {
		t.Fatal(err)
	}
}
func TestCapturePublicationKeepsEncryptedCompleteOrderedTail(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0xc1)
	s, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	input := strings.Repeat("Capture ordered 日本語🙂 paragraph.\n", 40000) + "password:\"private-doc-secret\"\nTAIL_FACT: rollback owner is Mira."
	h, parts, summary := makeStoredCapture(t, input, time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC))
	stageStoredCapture(t, s, h, parts)
	if _, found, err := s.GetCapture(ctx, summary.ContentID()); err != nil || found {
		t.Fatal("unfinished stage appeared as evidence", found, err)
	}
	if page, err := s.ListCaptures(ctx, "", 32); err != nil || len(page.Editions) != 0 {
		t.Fatal("stage leaked into catalog", err)
	}
	edition, err := s.PublishCapture(ctx, summary)
	if err != nil || edition.Summary.ContentID() != summary.ContentID() || edition.Visits != 1 {
		t.Fatal("checked publication failed", err)
	}
	var text strings.Builder
	after := int64(-1)
	for {
		page, err := s.ReadCaptureParts(ctx, summary.ContentID(), after, 8)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range page.Parts {
			if p.ValidateForAdmission() == nil {
				t.Fatal("read part granted write admission")
			}
			text.WriteString(p.Text())
		}
		if !page.HasMore {
			break
		}
		if page.Next <= after {
			t.Fatal("ordered cursor stalled")
		}
		after = page.Next
	}
	if !strings.HasSuffix(text.String(), "TAIL_FACT: rollback owner is Mira.") || strings.Contains(text.String(), "private-doc-secret") || text.Len() <= 1<<20 {
		t.Fatal("encrypted edition lost tail or split-secret protection")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "TAIL_FACT") || strings.Contains(string(encoded), "example.test") {
		t.Fatal("capture plaintext leaked on disk")
	}
	s, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	if _, found, err := s.GetCapture(ctx, summary.ContentID()); err != nil || !found {
		t.Fatal("published capture did not reopen", found, err)
	}
}

func TestReadCapturePartsPageBindsPublicationAndPagesInOrder(t *testing.T) {
	ctx := context.Background()
	s, err := OpenDurable(durableTestPath(t), durableTestKey(t, 0xd9), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	h, parts, summary := makeStoredCapture(t, strings.Repeat("ordered document passage. ", 7000), time.Now().UTC())
	stageStoredCapture(t, s, h, parts)
	_, err = s.PublishCapture(ctx, summary)
	if err != nil {
		t.Fatal(err)
	}
	var publication string
	if err := s.view(ctx, func(reader bolt.Reader) error {
		record, _, found, err := s.captureEditionReader(reader, summary.ContentID())
		if err != nil {
			return err
		}
		if !found {
			t.Fatal("published edition missing")
		}
		publication = record.Publication
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var got []capture.CapturePart
	after := int64(-1)
	for {
		page, err := s.ReadCapturePartsPage(ctx, storage.CapturePartsPageRequest{EditionID: summary.ContentID(), Publication: publication, After: after, Limit: 5})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Parts) > 5 || page.Next != after+int64(len(page.Parts)) {
			t.Fatal("part page exceeded its bound or returned an invalid cursor", page)
		}
		got = append(got, page.Parts...)
		if !page.HasMore {
			break
		}
		if page.Next <= after {
			t.Fatal("part cursor did not advance", page)
		}
		after = page.Next
	}
	if len(got) != len(parts) {
		t.Fatalf("read %d parts, want %d", len(got), len(parts))
	}
	for i := range parts {
		if got[i].Index() != int64(i) || got[i].Text() != parts[i].Text() {
			t.Fatalf("part %d lost order or text", i)
		}
	}
	if _, err := s.ReadCapturePartsPage(ctx, storage.CapturePartsPageRequest{EditionID: summary.ContentID(), Publication: strings.Repeat("e", 64), After: -1, Limit: 5}); !errors.Is(err, storage.ErrCaptureCursorChanged) {
		t.Fatalf("changed publication was accepted: %v", err)
	}
	if _, err := s.ReadCapturePartsPage(ctx, storage.CapturePartsPageRequest{EditionID: strings.Repeat("f", 64), Publication: publication, After: -1, Limit: 5}); !errors.Is(err, ErrMissing) {
		t.Fatalf("missing edition remained readable: %v", err)
	}
	if _, err := s.ReadCapturePartsPage(ctx, storage.CapturePartsPageRequest{EditionID: summary.ContentID(), Publication: publication, After: -1, Limit: 9}); err == nil {
		t.Fatal("unbounded part page was accepted")
	}
}

func TestCaptureIdempotentProgressPublicationAndExactVisits(t *testing.T) {
	s := openReplicaTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	var id string
	for i := 0; i < 70; i++ {
		h, parts, summary := makeStoredCapture(t, "Shared document tail fact", at.Add(time.Duration(i)*time.Second))
		stageStoredCapture(t, s, h, parts)
		if err := s.AppendCaptureParts(ctx, h, parts); err != nil {
			t.Fatal("identical append was not idempotent", err)
		}
		edition, err := s.PublishCapture(ctx, summary)
		if err != nil {
			t.Fatal(err)
		}
		if id != "" && id != summary.ContentID() {
			t.Fatal("visit changed edition identity")
		}
		id = summary.ContentID()
		if edition.Visits != int64(i+1) || !edition.LastSeen.Equal(h.CapturedAt()) {
			t.Fatal("visit receipt lost exact time/count", edition.Visits, edition.LastSeen)
		}
		again, err := s.PublishCapture(ctx, summary)
		if err != nil || again.Visits != edition.Visits {
			t.Fatal("publication retry duplicated visit", err)
		}
	}
	var visits []CaptureVisit
	after := ""
	for {
		page, err := s.ListCaptureVisits(ctx, id, after, 17)
		if err != nil {
			t.Fatal(err)
		}
		visits = append(visits, page.Visits...)
		if !page.HasMore {
			break
		}
		if page.Next == after {
			t.Fatal("visit cursor stalled")
		}
		after = page.Next
	}
	if len(visits) != 70 {
		t.Fatal("bounded visit pages lost history", len(visits))
	}
	for i, v := range visits {
		if !v.CapturedAt.Equal(at.Add(time.Duration(i) * time.Second)) {
			t.Fatal("visit time approximated", i, v)
		}
	}
	page, err := s.ListCaptures(ctx, "", 32)
	if err != nil || len(page.Editions) != 1 {
		t.Fatal("unchanged editions were duplicated", len(page.Editions), err)
	}
}
func TestCaptureStageRejectsGapsMixedCapturesAndReadOnlyValues(t *testing.T) {
	s := openReplicaTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	h, parts, summary := makeStoredCapture(t, strings.Repeat("ordinary ", 5000), at)
	if err := s.BeginCapture(ctx, h); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendCaptureParts(ctx, h, parts[1:]); !errors.Is(err, ErrCaptureConflict) {
		t.Fatal("nonconsecutive stage admitted", err)
	}
	h2, p2, _ := makeStoredCapture(t, strings.Repeat("different ", 5000), at)
	if err := s.AppendCaptureParts(ctx, h, p2); !errors.Is(err, ErrCaptureConflict) {
		t.Fatal("mixed capture nonce admitted", err)
	}
	if err := s.AppendCaptureParts(ctx, h2, parts); !errors.Is(err, ErrCaptureConflict) {
		t.Fatal("missing stage admitted", err)
	}
	if _, err := s.PublishCapture(ctx, summary); !errors.Is(err, ErrCaptureConflict) {
		t.Fatal("incomplete summary published", err)
	}
	encoded, _ := capture.MarshalAuthenticatedCapturePart(parts[0])
	restored, _ := capture.RestoreAuthenticatedCapturePart(encoded)
	if err := s.AppendCaptureParts(ctx, h, []capture.CapturePart{restored}); err == nil {
		t.Fatal("authenticated read restoration bypassed write admission")
	}
	stageStoredCapture(t, s, h, parts)
	if _, err := s.PublishCapture(ctx, summary); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendCaptureParts(ctx, h, parts); !errors.Is(err, ErrCaptureClosed) {
		t.Fatal("published capture accepted append", err)
	}
}
func TestCaptureChangedSourceMapsAndBoundedPaging(t *testing.T) {
	s := openReplicaTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	var ids []string
	for _, text := range []string{"password:\"a\" same tail", "password:\"a much longer secret\" same tail"} {
		h, p, summary := makeStoredCapture(t, text, at)
		stageStoredCapture(t, s, h, p)
		if _, err := s.PublishCapture(ctx, summary); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, summary.ContentID())
	}
	if ids[0] == ids[1] {
		t.Fatal("changed original positions deduplicated incorrectly")
	}
	page, err := s.ListCaptures(ctx, "", 1)
	if err != nil || len(page.Editions) != 1 || !page.HasMore {
		t.Fatal("bounded capture catalog failed", err)
	}
	next, err := s.ListCaptures(ctx, page.Next, 1)
	if err != nil || len(next.Editions) != 1 || next.HasMore || next.Editions[0].Summary.ContentID() == page.Editions[0].Summary.ContentID() {
		t.Fatal("catalog cursor did not preserve order", err)
	}
	if _, err := s.ReadCaptureParts(ctx, ids[0], -1, 9); err == nil {
		t.Fatal("unbounded part read admitted")
	}
	if _, err := s.ListCaptures(ctx, strings.Repeat("f", 64), 1); !errors.Is(err, ErrCaptureCursorChanged) {
		t.Fatal("unknown continuation looked like complete empty results", err)
	}
}

func TestCaptureEmptyAppendStillChecksItsStoreAndStage(t *testing.T) {
	h, _, _ := makeStoredCapture(t, "ordinary tail", time.Now())
	var absent *Store
	if err := absent.AppendCaptureParts(context.Background(), h, nil); err == nil {
		t.Fatal("nil store accepted an empty append")
	}
	s := openReplicaTestStore(t)
	if err := s.AppendCaptureParts(context.Background(), h, nil); !errors.Is(err, ErrCaptureConflict) {
		t.Fatal("empty append skipped the missing stage", err)
	}
}

func TestCaptureCorruptMembershipFailsBeforeReturningText(t *testing.T) {
	s := openReplicaTestStore(t)
	ctx := context.Background()
	h, parts, summary := makeStoredCapture(t, strings.Repeat("ordered text ", 4000), time.Now())
	stageStoredCapture(t, s, h, parts)
	if _, err := s.PublishCapture(ctx, summary); err != nil {
		t.Fatal(err)
	}
	encoded, found, err := s.readIndexValue(ctx, captureNS, capturePartKey(h.CaptureID(), 1), captureStoredPartBytes)
	if err != nil || !found {
		t.Fatal("missing test part", err)
	}
	if err := s.Update(ctx, func(tx *Tx) error { return tx.PutIndexRecord(captureNS, capturePartKey(h.CaptureID(), 0), encoded) }); err != nil {
		t.Fatal(err)
	}
	page, err := s.ReadCaptureParts(ctx, summary.ContentID(), -1, 8)
	if !errors.Is(err, ErrCorrupt) || len(page.Parts) != 0 {
		t.Fatal("corrupt membership returned text", err, len(page.Parts))
	}
}

func TestCapturePrivatePrefixRetryRejectsConflictAndCanResume(t *testing.T) {
	s := openReplicaTestStore(t)
	ctx := context.Background()
	b, err := capture.NewCaptureBuilder("com.example.reader", "example.test", strings.Repeat("d", 64), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Abort()
	h := b.Header()
	if err := s.BeginCapture(ctx, h); err != nil {
		t.Fatal(err)
	}
	first := "first " + strings.Repeat("x", 33<<10)
	second := "other " + strings.Repeat("x", 33<<10)
	stageAndFail := func(ctx context.Context, p []capture.CapturePart) error {
		if err := s.AppendCaptureParts(ctx, h, p); err != nil {
			return err
		}
		return errors.New("private stage interrupted")
	}
	if err := b.Write(ctx, first, stageAndFail); !errors.Is(err, capture.ErrCaptureOutput) {
		t.Fatal("expected private-prefix interruption", err)
	}
	var conflicting []capture.CapturePart
	if err := b.Write(ctx, second, func(_ context.Context, p []capture.CapturePart) error {
		conflicting = append(conflicting, p...)
		return errors.New("retry alternative")
	}); !errors.Is(err, capture.ErrCaptureOutput) {
		t.Fatal(err)
	}
	if err := s.AppendCaptureParts(ctx, h, conflicting); !errors.Is(err, ErrCaptureConflict) {
		t.Fatal("different retry overwrote accepted private prefix", err)
	}
	accept := func(ctx context.Context, p []capture.CapturePart) error { return s.AppendCaptureParts(ctx, h, p) }
	if err := b.Write(ctx, first, accept); err != nil {
		t.Fatal("identical retry could not resume", err)
	}
	summary, err := b.Finish(ctx, capture.CaptureComplete, accept)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishCapture(ctx, summary); err != nil {
		t.Fatal("resumed capture failed publication", err)
	}
}

func TestCaptureCatalogCursorRejectsDeletedAndRecreatedIncarnation(t *testing.T) {
	for _, reuseCapture := range []bool{false, true} {
		t.Run(fmt.Sprint(reuseCapture), func(t *testing.T) {
			ctx := context.Background()
			s := openReplicaTestStore(t)
			at := time.Now().UTC().Add(-time.Hour)
			h, p, summary := makeStoredCapture(t, "First document A", at)
			stageStoredCapture(t, s, h, p)
			if _, err := s.PublishCapture(ctx, summary); err != nil {
				t.Fatal(err)
			}
			bh, bp, bs := makeStoredCapture(t, "Second document B", at)
			stageStoredCapture(t, s, bh, bp)
			if _, err := s.PublishCapture(ctx, bs); err != nil {
				t.Fatal(err)
			}
			page, err := s.ListCaptures(ctx, "", 1)
			if err != nil || !page.HasMore || len(page.Editions) != 1 {
				t.Fatal("missing initial catalog page", err)
			}
			if !reuseCapture {
				if err := s.RecordTombstone(ctx, Tombstone{SourceID: summary.ContentID(), At: at.Add(time.Second)}); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.DeleteObservationRecord(ctx, summary.ContentID()); err != nil {
				t.Fatal(err)
			}
			if !reuseCapture {
				h, p, summary = makeStoredCapture(t, "First document A", at.Add(time.Minute))
			}
			stageStoredCapture(t, s, h, p)
			if _, err := s.PublishCapture(ctx, summary); err != nil {
				t.Fatal(err)
			}
			if next, err := s.ListCaptures(ctx, page.Next, 1); !errors.Is(err, ErrCaptureCursorChanged) {
				t.Fatal("stale cursor silently skipped B", len(next.Editions), next.HasMore, err)
			}
		})
	}
}

func TestCaptureDuplicatePublicationDrainsPayloadDuringOpenSession(t *testing.T) {
	ctx := context.Background()
	s := openReplicaTestStore(t)
	at := time.Now().UTC()
	text := strings.Repeat("A repeated complete large document. ", 32000)
	h, p, summary := makeStoredCapture(t, text, at)
	stageStoredCapture(t, s, h, p)
	if _, err := s.PublishCapture(ctx, summary); err != nil {
		t.Fatal(err)
	}
	dh, dp, ds := makeStoredCapture(t, text, at.Add(time.Second))
	stageStoredCapture(t, s, dh, dp)
	if _, err := s.PublishCapture(ctx, ds); err != nil {
		t.Fatal(err)
	}
	assertCapturePayloadAbsent(t, s, dh, len(dp))
	if got, found, err := s.GetCapture(ctx, summary.ContentID()); err != nil || !found || got.Visits != 2 {
		t.Fatal("dedup cleanup damaged canonical edition", found, err)
	}
	if page, err := s.ReadCaptureParts(ctx, summary.ContentID(), -1, 8); err != nil || len(page.Parts) == 0 {
		t.Fatal("dedup cleanup erased canonical parts", err)
	}
}

func TestCaptureCommittedPublicationCleanupRetryKeepsOneVisit(t *testing.T) {
	ctx := context.Background()
	s := openReplicaTestStore(t)
	at := time.Now().UTC()
	h, p, summary := makeStoredCapture(t, "Published receipt survives interrupted cleanup", at)
	stageStoredCapture(t, s, h, p)
	if _, err := s.PublishCapture(ctx, summary); err != nil {
		t.Fatal(err)
	}
	dh, dp, ds := makeStoredCapture(t, "Published receipt survives interrupted cleanup", at.Add(time.Second))
	stageStoredCapture(t, s, dh, dp)
	canceled, cancel := context.WithCancel(ctx)
	defer cancel()
	committed, err := s.publishCaptureAndCleanup(canceled, ds, cancel)
	if !errors.Is(err, context.Canceled) || committed.Summary.ContentID() != summary.ContentID() || committed.Visits != 2 {
		t.Fatal("post-commit cleanup lost committed result", committed.Visits, err)
	}
	if retried, err := s.PublishCapture(ctx, ds); err != nil || retried.Visits != 2 {
		t.Fatal("receipt retry duplicated visit or failed cleanup", retried.Visits, err)
	}
	assertCapturePayloadAbsent(t, s, dh, len(dp))
}

func TestCaptureLargeEOFPartialAppendRetriesAcceptedPrefix(t *testing.T) {
	ctx := context.Background()
	s := openReplicaTestStore(t)
	b, err := capture.NewCaptureBuilder("com.example.reader", "example.test", strings.Repeat("d", 64), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Abort()
	h := b.Header()
	if err := s.BeginCapture(ctx, h); err != nil {
		t.Fatal(err)
	}
	input := "https://example.test/" + strings.Repeat("path", 15000)
	if err := b.Write(ctx, input, func(context.Context, []capture.CapturePart) error {
		t.Fatal("greedy URL drained before EOF")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, foreign, _ := makeStoredCapture(t, "Different capture nonce", time.Now().UTC())
	var failed []capture.CapturePart
	if _, err := b.Finish(ctx, capture.CaptureComplete, func(ctx context.Context, parts []capture.CapturePart) error {
		if len(parts) < 2 {
			t.Fatal("EOF fixture did not emit a multi-part batch")
		}
		failed = append([]capture.CapturePart(nil), parts...)
		batch := append([]capture.CapturePart(nil), parts...)
		batch[1] = foreign[0]
		return s.AppendCaptureParts(ctx, h, batch)
	}); !errors.Is(err, capture.ErrCaptureOutput) {
		t.Fatal("expected partial multi-part sink failure", err)
	}
	if err := s.view(ctx, func(r bolt.Reader) error {
		stage, _, found, err := s.captureStageReader(r, h.CaptureID())
		if err != nil {
			return err
		}
		if !found || stage.Parts != 1 {
			t.Fatal("failure did not retain exactly one accepted part", stage.Parts)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	summary, err := b.Finish(ctx, capture.CaptureComplete, func(ctx context.Context, parts []capture.CapturePart) error {
		if len(parts) != len(failed) {
			t.Fatal("EOF retry changed batch length")
		}
		for i := range parts {
			if parts[i].Digest() != failed[i].Digest() {
				t.Fatal("EOF retry changed canonical part")
			}
		}
		return s.AppendCaptureParts(ctx, h, parts)
	})
	if err != nil {
		t.Fatal("EOF prefix retry failed", err)
	}
	if _, err := s.PublishCapture(ctx, summary); err != nil {
		t.Fatal(err)
	}
	page, err := s.ReadCaptureParts(ctx, summary.ContentID(), -1, 8)
	if err != nil || page.HasMore {
		t.Fatal("EOF result lost membership", err)
	}
	var got strings.Builder
	for _, part := range page.Parts {
		got.WriteString(part.Text())
	}
	if got.String() != input {
		t.Fatal("EOF partial append retry lost or duplicated text")
	}
}

func TestCaptureConcurrentPublicationKeepsCatalogAndVisitReceiptsConsistent(t *testing.T) {
	s := openReplicaTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 13, 11, 0, 0, 0, time.UTC)
	type input struct {
		header  capture.CaptureHeader
		parts   []capture.CapturePart
		summary capture.CaptureSummary
	}
	var jobs []input
	for i := 0; i < 12; i++ {
		h, p, summary := makeStoredCapture(t, "same complete document", at.Add(time.Duration(i)*time.Second))
		jobs = append(jobs, input{h, p, summary})
	}
	var wg sync.WaitGroup
	results := make(chan error, len(jobs))
	for _, job := range jobs {
		wg.Add(1)
		go func(job input) {
			defer wg.Done()
			if err := s.BeginCapture(ctx, job.header); err != nil {
				results <- err
				return
			}
			if err := s.AppendCaptureParts(ctx, job.header, job.parts); err != nil {
				results <- err
				return
			}
			if _, err := s.PublishCapture(ctx, job.summary); err != nil {
				results <- err
				return
			}
			_, err := s.PublishCapture(ctx, job.summary)
			results <- err
		}(job)
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal("concurrent publication lost atomic catalog state", err)
		}
	}
	page, err := s.ListCaptures(ctx, "", 32)
	if err != nil || len(page.Editions) != 1 || page.Editions[0].Visits != int64(len(jobs)) || !page.Editions[0].LastSeen.Equal(at.Add(11*time.Second)) {
		t.Fatal("concurrent publication duplicated editions or lost visits", err)
	}
	visits, err := s.ListCaptureVisits(ctx, jobs[0].summary.ContentID(), "", 64)
	if err != nil || len(visits.Visits) != len(jobs) {
		t.Fatal("visit receipts were not exact", err, len(visits.Visits))
	}
}

func TestCapturePublicationRollbackIncludesDescriptorCatalogReceiptAndVisit(t *testing.T) {
	s := openReplicaTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 13, 11, 0, 0, 0, time.UTC)
	h, parts, summary := makeStoredCapture(t, "atomic publication tail", at)
	stageStoredCapture(t, s, h, parts)
	called := false
	_, err := s.publishCaptureWithHook(ctx, summary, func() error { called = true; return ErrTransaction })
	if !called || !errors.Is(err, ErrTransaction) {
		t.Fatal("publication did not exercise rollback after private writes", called, err)
	}
	if _, found, err := s.GetCapture(ctx, summary.ContentID()); err != nil || found {
		t.Fatal("failed publication leaked descriptor", err)
	}
	page, err := s.ListCaptures(ctx, "", 32)
	if err != nil || len(page.Editions) != 0 {
		t.Fatal("failed publication leaked catalog membership", err)
	}
	first, err := s.PublishCapture(ctx, summary)
	if err != nil || first.Visits != 1 {
		t.Fatal("publication rollback lost retry or duplicated receipt", err)
	}
	h, parts, summary = makeStoredCapture(t, "atomic publication tail", at.Add(time.Second))
	stageStoredCapture(t, s, h, parts)
	if _, err := s.publishCaptureWithHook(ctx, summary, func() error { return ErrTransaction }); !errors.Is(err, ErrTransaction) {
		t.Fatal(err)
	}
	unchanged, found, err := s.GetCapture(ctx, summary.ContentID())
	if err != nil || !found || unchanged.Visits != 1 || !unchanged.LastSeen.Equal(at) {
		t.Fatal("failed dedup publication committed visit", err, unchanged.Visits)
	}
	final, err := s.PublishCapture(ctx, summary)
	if err != nil || final.Visits != 2 {
		t.Fatal("dedup rollback was not retryable", err)
	}
}
