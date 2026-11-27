package browsercapture

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/ingest"
	"github.com/hazcod/blur/internal/localapi"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/durable"
)

type documentStore struct {
	*durable.Store
	published               []capture.CaptureSummary
	discards                int
	failAppend, failPublish bool
}

func (s *documentStore) AppendCaptureParts(ctx context.Context, header capture.CaptureHeader, parts []capture.CapturePart) error {
	err := s.Store.AppendCaptureParts(ctx, header, parts)
	if err == nil && s.failAppend {
		s.failAppend = false
		return errors.New("retry committed append")
	}
	return err
}

func (s *documentStore) PublishDocumentCapture(ctx context.Context, summary capture.CaptureSummary) (ingest.DocumentPublication, error) {
	if s.failPublish {
		s.failPublish = false
		return ingest.DocumentPublication{}, errors.New("retry publication")
	}
	receipt, err := s.PublishCaptureWithSourceReceipt(ctx, summary)
	if receipt.Committed {
		s.published = append(s.published, summary)
	}
	return ingest.DocumentPublication{Committed: receipt.Committed, New: receipt.New, SourceID: receipt.SourceID}, err
}

func changeDocumentWire(t *testing.T, grant localapi.IssuedGrant, now time.Time, wire []byte, mutate func(map[string]any)) []byte {
	t.Helper()
	var envelope testEnvelope
	if err := json.Unmarshal(wire, &envelope); err != nil {
		t.Fatal(err)
	}
	var page map[string]any
	if err := json.Unmarshal(envelope.Payload, &page); err != nil {
		t.Fatal(err)
	}
	mutate(page)
	payload, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	return signedPageWire(t, grant, now, envelope.Version, payload)
}
func (s *documentStore) DiscardCaptureStage(ctx context.Context, id string) error {
	s.discards++
	return s.Store.DiscardCaptureStage(ctx, id)
}

func documentReceiver(t *testing.T) (*Receiver, localapi.IssuedGrant, *documentStore, time.Time) {
	t.Helper()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "safari.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC()
	grants, err := localapi.NewGrantStore(func() time.Time { return now }, nil, 1000)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := grants.Issue("safari-extension", []localapi.Scope{localapi.ScopeCaptureIngest}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	sink := &documentStore{Store: store}
	r, err := NewReceiver(grants, sink, func() bool { return true }, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Pause)
	return r, grant, sink, now
}

func documentWire(t *testing.T, grant localapi.IssuedGrant, now time.Time, revision, sequence int, text string, complete, limited bool) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"browser": SafariBundleID, "url": "https://example.test", "title": "Project", "text": text,
		"active": true, "privacy_known": true, "private": false, "captured_at": now,
		"document_id": strings.Repeat("a", 32), "source_key": strings.Repeat("b", 64),
		"revision": revision, "sequence": sequence, "complete": complete, "structure_limited": limited,
	})
	if err != nil {
		t.Fatal(err)
	}
	return signedPageWire(t, grant, now, 2, payload)
}

func signedPageWire(t *testing.T, grant localapi.IssuedGrant, now time.Time, version int, payload []byte) []byte {
	t.Helper()
	proof, err := localapi.NewProof(grant, localapi.ScopeCaptureIngest, sha256.Sum256(payload), now, nil)
	if err != nil {
		t.Fatal(err)
	}
	e := testEnvelope{Version: version, Payload: payload}
	e.Proof.GrantID, e.Proof.ClientID, e.Proof.Scope, e.Proof.Timestamp = grant.ID, grant.ClientID, string(localapi.ScopeCaptureIngest), now
	e.Proof.Nonce, e.Proof.MAC = base64.RawURLEncoding.EncodeToString(proof.Nonce), hex.EncodeToString(proof.MAC[:])
	wire, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return wire
}

func TestLegacySnapshotWithoutCoverageCannotClaimComplete(t *testing.T) {
	r, grant, store, now := documentReceiver(t)
	payload, err := json.Marshal(map[string]any{"browser": SafariBundleID, "url": "https://example.test", "text": "A captured prefix with enough text to keep.", "active": true, "privacy_known": true, "private": false, "captured_at": now})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Accept(context.Background(), signedPageWire(t, grant, now, 1, payload)); err == nil {
		t.Fatal("legacy prefix without coverage was accepted as complete")
	}
	page, err := store.ListObservationPage(context.Background(), storage.ObservationPageRequest{Limit: 5, MaxBytes: storage.MaxObservationPageBytes})
	if err != nil || len(page.Observations) != 0 {
		t.Fatalf("unverified prefix reached observations: %+v %v", page, err)
	}
}

func TestDOMDocumentContinuesToIndexedTailAndRetriesWithoutDuplicateParts(t *testing.T) {
	r, grant, store, now := documentReceiver(t)
	ctx := context.Background()
	text := strings.Repeat("Compiler evidence. ", 20000) + " RollbackownerMira"
	sequence := 0
	for at := 0; at < len(text); sequence++ {
		next := min(at+32768, len(text))
		complete := next == len(text)
		for retry := 0; retry < 2; retry++ {
			if err := r.Accept(ctx, documentWire(t, grant, now, 1, sequence, text[at:next], complete, false)); err != nil {
				t.Fatalf("batch %d retry %d: %v", sequence, retry, err)
			}
		}
		if !complete {
			page, err := store.ListCaptures(ctx, "", 8)
			if err != nil || len(page.Editions) != 0 {
				t.Fatalf("unfinished document became public: %+v %v", page, err)
			}
		}
		at = next
	}
	if len(store.published) != 1 || store.published[0].Coverage() != capture.CaptureComplete {
		t.Fatalf("publication retries duplicated or changed coverage: %+v", store.published)
	}
	for range 128 {
		more, err := store.IndexPendingCaptures(ctx, 16)
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
	}
	page, err := store.SearchExact(ctx, search.ExactQuery{Terms: []string{"rollbackownermira"}, TopK: 8, MaxRows: 1000, MaxBytes: search.MaxIndexBytes})
	if err != nil || len(page.Hits) != 1 {
		t.Fatalf("complete Safari tail was not indexed: %+v %v", page, err)
	}
	evidence, found, err := store.GetSourceEvidence(ctx, page.Hits[0].SourceID)
	if err != nil || !found || !strings.Contains(evidence.Text(), "RollbackownerMira") || evidence.Truncated() {
		t.Fatalf("tail lost complete multipart provenance: %+v %v %v", evidence, found, err)
	}
}

func TestDOMDocumentRejectsSkippedChangedAndSupersededBatches(t *testing.T) {
	r, grant, store, now := documentReceiver(t)
	ctx := context.Background()
	if err := r.Accept(ctx, documentWire(t, grant, now, 1, 0, "Original stable prefix with useful evidence. ", false, false)); err != nil {
		t.Fatal(err)
	}
	for _, wire := range [][]byte{
		documentWire(t, grant, now, 1, 0, "A changed retry cannot replace accepted text.", false, false),
		documentWire(t, grant, now, 1, 2, "A skipped batch cannot be published.", true, false),
	} {
		if err := r.Accept(ctx, wire); err == nil {
			t.Fatal("accepted a changed or skipped batch")
		}
	}
	if err := r.Accept(ctx, documentWire(t, grant, now, 2, 0, "Replacement document with fresh stable facts.", true, false)); err != nil {
		t.Fatal(err)
	}
	if err := r.Accept(ctx, documentWire(t, grant, now, 1, 1, "Old revision must never resume into replacement.", true, false)); err == nil {
		t.Fatal("resumed superseded revision")
	}
	if len(store.published) != 1 || store.discards == 0 {
		t.Fatalf("superseded stage was published or not cleaned: publications=%d discards=%d", len(store.published), store.discards)
	}
}

func TestDOMDocumentPublishesInterruptedPrefixAsStructureLimited(t *testing.T) {
	r, grant, store, now := documentReceiver(t)
	ctx := context.Background()
	if err := r.Accept(ctx, documentWire(t, grant, now, 1, 0, "Stable captured prefix before the document changed. ", false, false)); err != nil {
		t.Fatal(err)
	}
	if err := r.Accept(ctx, documentWire(t, grant, now, 1, 1, "", true, true)); err != nil {
		t.Fatal(err)
	}
	if len(store.published) != 1 || store.published[0].Coverage() != capture.CaptureStructureLimited {
		t.Fatalf("interrupted prefix claimed complete: %+v", store.published)
	}
}

func TestDOMDocumentRetriesCommittedAppendAndFailedPublication(t *testing.T) {
	r, grant, store, now := documentReceiver(t)
	ctx := context.Background()
	text := strings.Repeat("a", 32768)
	if err := r.Accept(ctx, documentWire(t, grant, now, 1, 0, text, false, false)); err != nil {
		t.Fatal(err)
	}
	store.failAppend = true
	if err := r.Accept(ctx, documentWire(t, grant, now, 1, 1, text, false, false)); err == nil {
		t.Fatal("expected ambiguous append failure")
	}
	if err := r.Accept(ctx, documentWire(t, grant, now, 1, 1, text, false, false)); err != nil {
		t.Fatal("append retry lost builder position", err)
	}
	store.failPublish = true
	if err := r.Accept(ctx, documentWire(t, grant, now, 1, 2, "", true, false)); err == nil {
		t.Fatal("expected publication failure")
	}
	if err := r.Accept(ctx, documentWire(t, grant, now, 1, 2, "", true, false)); err != nil {
		t.Fatal("publication retry rewrote finished builder", err)
	}
	if len(store.published) != 1 || store.published[0].Bytes() != int64(len("Project\n")+2*len(text)) {
		t.Fatalf("retry lost or duplicated text: %+v", store.published)
	}
}

func TestDOMDocumentCallbacksRunAfterPolicyAndOncePerCommittedVisit(t *testing.T) {
	r, grant, store, now := documentReceiver(t)
	ctx := context.Background()
	active := false
	r.policy = func(ctx context.Context, _ capture.SanitizedObservation, write func(context.Context) error) error {
		active = true
		defer func() { active = false }()
		return write(ctx)
	}
	var fresh []bool
	r.SetNotifications(nil, func(summary capture.CaptureSummary, isNew bool) {
		if active {
			t.Error("notification escaped policy admission")
		}
		if summary.ValidateForAdmission() != nil {
			t.Error("untrusted document notification")
		}
		fresh = append(fresh, isNew)
	})
	for _, revision := range []int{1, 2} {
		for range 2 {
			if err := r.Accept(ctx, documentWire(t, grant, now, revision, 0, "A complete Safari document with stable useful facts.", true, false)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(fresh) != 2 || !fresh[0] || fresh[1] {
		t.Fatalf("new content/duplicate visit notifications incorrect: %v", fresh)
	}
	page, err := store.ListCaptures(ctx, "", 8)
	if err != nil || len(page.Editions) != 1 {
		t.Fatalf("duplicate visit created another edition: %+v %v", page, err)
	}
}

func TestDOMDocumentEvictionAndPauseDiscardPrivateStages(t *testing.T) {
	r, grant, store, now := documentReceiver(t)
	ctx := context.Background()
	for i := 1; i <= 5; i++ {
		wire := documentWire(t, grant, now, 1, 0, "A private staged document prefix with enough useful text.", false, false)
		wire = changeDocumentWire(t, grant, now, wire, func(page map[string]any) { page["source_key"] = fmt.Sprintf("%064x", i) })
		if err := r.Accept(ctx, wire); err != nil {
			t.Fatal(err)
		}
	}
	if len(r.documents) != 4 || store.discards == 0 {
		t.Fatalf("unbounded jobs or missing eviction cleanup: jobs=%d discards=%d", len(r.documents), store.discards)
	}
	r.Pause()
	if len(r.documents) != 0 || store.discards < 5 {
		t.Fatalf("paused document stages retained: jobs=%d discards=%d", len(r.documents), store.discards)
	}
	if err := r.Accept(ctx, documentWire(t, grant, now, 1, 1, "", true, false)); !errors.Is(err, ErrReceiverPaused) {
		t.Fatal("paused bridge accepted a final document", err)
	}
	page, err := store.ListCaptures(ctx, "", 8)
	if err != nil || len(page.Editions) != 0 {
		t.Fatalf("unfinished stages became visible: %+v %v", page, err)
	}
}

func TestDOMDocumentRechecksNativeAuthorityAndProtocolRanges(t *testing.T) {
	for _, mutate := range []func(map[string]any){
		func(p map[string]any) { p["private"] = true }, func(p map[string]any) { p["active"] = false }, func(p map[string]any) { p["privacy_known"] = false },
		func(p map[string]any) { p["browser"] = "forged.app" }, func(p map[string]any) { p["source_key"] = "raw/path" },
		func(p map[string]any) { p["url"] = "https://example.test/private/path?secret=hidden" },
		func(p map[string]any) { p["sequence"] = int64(1 << 53) }, func(p map[string]any) { p["revision"] = int64(1 << 53) },
		func(p map[string]any) { p["truncated"] = true }, func(p map[string]any) { p["text"] = strings.Repeat("x", 32769) },
	} {
		r, grant, store, now := documentReceiver(t)
		ctx := context.Background()
		if err := r.Accept(ctx, documentWire(t, grant, now, 1, 0, "A stable permitted prefix before native state changes.", false, false)); err != nil {
			t.Fatal(err)
		}
		wire := changeDocumentWire(t, grant, now, documentWire(t, grant, now, 1, 1, "", true, false), mutate)
		if err := r.Accept(ctx, wire); err == nil {
			t.Fatal("accepted invalid authority or resume range")
		}
		if len(store.published) != 0 {
			t.Fatal("invalid final frame published a document")
		}
	}
}

func TestDOMDocumentTitleBadgeChangesDoNotStarveStableBody(t *testing.T) {
	r, grant, store, now := documentReceiver(t)
	ctx := context.Background()
	if err := r.Accept(ctx, documentWire(t, grant, now, 1, 0, "Stable body text before later appended messages. ", false, false)); err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"Project (1 new message)", "Project (2 new messages)"} {
		wire := changeDocumentWire(t, grant, now, documentWire(t, grant, now, 1, 1, "Initial snapshot tail.", true, false), func(p map[string]any) { p["title"] = title })
		if err := r.Accept(ctx, wire); err != nil {
			t.Fatal("native title badge change restarted a stable fenced body", err)
		}
	}
	if len(store.published) != 1 {
		t.Fatalf("title update duplicated revision: %d", len(store.published))
	}
}

func TestDOMDocumentFirstBatchRetryKeepsInitialTitle(t *testing.T) {
	r, grant, store, now := documentReceiver(t)
	ctx := context.Background()
	text := strings.Repeat("Body evidence. ", 2300)
	text = text[:MaxDOMBatchBytes]
	first := documentWire(t, grant, now, 1, 0, text, false, false)
	store.failAppend = true
	if err := r.Accept(ctx, first); err == nil {
		t.Fatal("expected ambiguous first append failure")
	}
	retry := changeDocumentWire(t, grant, now, first, func(p map[string]any) { p["title"] = "Project (1 new message)" })
	if err := r.Accept(ctx, retry); err != nil {
		t.Fatal("title badge changed an already staged first batch", err)
	}
	if err := r.Accept(ctx, documentWire(t, grant, now, 1, 1, "Retained final tail.", true, false)); err != nil {
		t.Fatal(err)
	}
	if len(store.published) != 1 || store.published[0].Bytes() != int64(len("Project\n")+len(text)+len("Retained final tail.")) {
		t.Fatalf("first retry lost or duplicated document text: %+v", store.published)
	}
}

func TestDOMDocumentTailChangesCreateSearchableHistoricalEditions(t *testing.T) {
	r, grant, store, now := documentReceiver(t)
	ctx := context.Background()
	prefix := strings.Repeat("Stable document prefix. ", 15000)
	for revision, marker := range []string{"PreviousownerNora", "CurrentownerMira"} {
		text := prefix + marker
		sequence := 0
		for at := 0; at < len(text); sequence++ {
			next := min(at+MaxDOMBatchBytes, len(text))
			if err := r.Accept(ctx, documentWire(t, grant, now, revision+1, sequence, text[at:next], next == len(text), false)); err != nil {
				t.Fatal(err)
			}
			at = next
		}
	}
	if len(store.published) != 2 || store.published[0].Header().FamilyID() != store.published[1].Header().FamilyID() || store.published[0].ContentID() == store.published[1].ContentID() {
		t.Fatalf("tail-only change lost edition identity: %+v", store.published)
	}
	for range 128 {
		more, err := store.IndexPendingCaptures(ctx, 16)
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
	}
	for _, marker := range []string{"previousownernora", "currentownermira"} {
		page, err := store.SearchExact(ctx, search.ExactQuery{Terms: []string{marker}, TopK: 8, MaxRows: 1000, MaxBytes: search.MaxIndexBytes})
		if err != nil || len(page.Hits) != 1 {
			t.Fatalf("tail edition was not independently indexed: %s %+v %v", marker, page, err)
		}
	}
}

func TestDOMDocumentRedactsSecretsAcrossTransportBatches(t *testing.T) {
	r, grant, store, now := documentReceiver(t)
	ctx := context.Background()
	for sequence, text := range []string{"Useful stable document evidence. password:\"private-", "split-secret\"\nRetained nonsecret final tail."} {
		if err := r.Accept(ctx, documentWire(t, grant, now, 1, sequence, text, sequence == 1, false)); err != nil {
			t.Fatal(err)
		}
	}
	if len(store.published) != 1 {
		t.Fatal("document did not publish")
	}
	page, err := store.ReadCaptureParts(ctx, store.published[0].ContentID(), -1, 8)
	if err != nil || page.HasMore {
		t.Fatal("read small published capture", err)
	}
	var joined strings.Builder
	for _, part := range page.Parts {
		joined.WriteString(part.Text())
	}
	if strings.Contains(joined.String(), "private-") || strings.Contains(joined.String(), "split-secret") || !strings.Contains(joined.String(), "[REDACTED:SECRET]") || !strings.Contains(joined.String(), "Retained nonsecret final tail.") {
		t.Fatalf("transport batches bypassed streaming redaction: %q", joined.String())
	}
}

func TestDOMDocumentPolicyChangeBlocksFinalPublication(t *testing.T) {
	r, grant, store, now := documentReceiver(t)
	ctx := context.Background()
	allowed, notifications := true, 0
	r.policy = func(ctx context.Context, _ capture.SanitizedObservation, write func(context.Context) error) error {
		if !allowed {
			return ErrReceiverPaused
		}
		return write(ctx)
	}
	r.SetNotifications(nil, func(capture.CaptureSummary, bool) { notifications++ })
	if err := r.Accept(ctx, documentWire(t, grant, now, 1, 0, "Previously permitted private staging prefix. ", false, false)); err != nil {
		t.Fatal(err)
	}
	allowed = false
	if err := r.Accept(ctx, documentWire(t, grant, now, 1, 1, "Final text after the user disabled this scope.", true, false)); !errors.Is(err, ErrReceiverPaused) {
		t.Fatal("final publication bypassed current policy", err)
	}
	if len(store.published) != 0 || notifications != 0 {
		t.Fatal("policy-denied final batch became public")
	}
	allowed = true
	if err := r.Accept(ctx, documentWire(t, grant, now, 2, 0, "Fresh snapshot after capture was permitted again.", true, false)); err != nil {
		t.Fatal(err)
	}
	if len(store.published) != 1 || notifications != 1 || store.discards < 2 {
		t.Fatal("new permitted revision did not replace and clean abandoned staging")
	}
}

func TestLegacyCompleteSnapshotNotifiesAfterPolicyAdmission(t *testing.T) {
	r, grant, store, now := documentReceiver(t)
	ctx := context.Background()
	active, notifications := false, 0
	r.policy = func(ctx context.Context, _ capture.SanitizedObservation, write func(context.Context) error) error {
		active = true
		defer func() { active = false }()
		return write(ctx)
	}
	r.SetNotifications(func(capture.SanitizedObservation, time.Time) {
		if active {
			t.Error("legacy notification ran inside preference admission")
		}
		notifications++
	}, nil)
	payload, err := json.Marshal(Snapshot{Browser: SafariBundleID, URL: "https://example.test", Text: "A complete legacy snapshot with useful retained evidence.", Active: true, PrivacyKnown: true, CapturedAt: now, Complete: true})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := r.Accept(ctx, signedPageWire(t, grant, now, 1, payload)); err != nil {
			t.Fatal(err)
		}
	}
	page, err := store.ListObservationPage(ctx, storage.ObservationPageRequest{Limit: 5, MaxBytes: storage.MaxObservationPageBytes})
	if err != nil || len(page.Observations) != 1 || notifications != 2 {
		t.Fatalf("complete legacy visits lost shared notification/dedup: observations=%d callbacks=%d error=%v", len(page.Observations), notifications, err)
	}
}
