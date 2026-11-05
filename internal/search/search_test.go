package search

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nielsgroen/blur/internal/capture"
	"github.com/nielsgroen/blur/internal/storage"
)

func observation(t *testing.T, app, domain, body string) capture.SanitizedObservation {
	t.Helper()
	o, err := capture.SanitizeObservation(app, domain, body, 4096)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestExactTermsAreCaseFoldedBoundedAndDeterministic(t *testing.T) {
	// Catches substring matching, OR matching, and insertion-order ranking.
	items := []capture.SanitizedObservation{
		observation(t, "omega.app", "example.test", "Café plan"),
		observation(t, "alpha.app", "example.test", "café planning"),
		observation(t, "beta.app", "example.test", "CAFÉ plan"),
	}
	request := Request{Query: "CAFÉ plan", ResultLimit: 2, SnippetBytes: 32, ResponseBytes: 4096, ScanLimit: 10, ScanBytes: MaxScanBytes}
	var first []string
	for run := 0; run < 2; run++ {
		store := storage.NewEphemeralMemoryStore()
		for i := range items {
			j := i
			if run == 1 {
				j = len(items) - 1 - i
			}
			if err := store.PutObservation(context.Background(), items[j]); err != nil {
				t.Fatal(err)
			}
		}
		got, err := New(store).Search(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		if got.Scanned != 3 || got.Omitted != 0 || len(got.Results) != 2 {
			t.Fatalf("unexpected bounded result: %+v", got)
		}
		ids := []string{got.Results[0].SourceID, got.Results[1].SourceID}
		for _, result := range got.Results {
			if result.ProvenanceKind != "raw_sanitized_observation" || !utf8.ValidString(result.Snippet) || len(result.Snippet) > 32 {
				t.Fatalf("invalid result: %+v", result)
			}
		}
		if run == 0 {
			first = ids
		} else if first[0] != ids[0] || first[1] != ids[1] {
			t.Fatalf("insertion order changed results: %v vs %v", first, ids)
		}
	}
}

func TestSearchReportsScanAndResponseOmissions(t *testing.T) {
	store := storage.NewEphemeralMemoryStore()
	for _, body := range []string{"alpha one", "alpha two", "alpha three"} {
		if err := store.PutObservation(context.Background(), observation(t, "demo.app", "example.test", body)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := New(store).Search(context.Background(), Request{Query: "alpha", ResultLimit: 1, SnippetBytes: 100, ResponseBytes: 4096, ScanLimit: 2, ScanBytes: MaxScanBytes})
	if err != nil {
		t.Fatal(err)
	}
	if got.Scanned != 2 || !got.Truncated || got.Omitted != 1 || len(got.Results) != 1 {
		t.Fatalf("bad truncation accounting: %+v", got)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 4096 {
		t.Fatalf("oversized response: %d", len(encoded))
	}
}

func TestSearchStopsAtRequestedByteBudgetAndReportsExactAccounting(t *testing.T) {
	store := storage.NewEphemeralMemoryStore()
	for _, body := range []string{"alpha one", "alpha two"} {
		if err := store.PutObservation(context.Background(), observation(t, "demo.app", "example.test", body)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := New(store).Search(context.Background(), Request{Query: "alpha", ResultLimit: 2, SnippetBytes: 32, ResponseBytes: 512, ScanLimit: 10, ScanBytes: 29})
	if err != nil {
		t.Fatal(err)
	}
	if got.Scanned != 1 || got.ScannedBytes != 29 || !got.Truncated || got.Omitted != 0 || len(got.Results) != 1 {
		t.Fatalf("byte-bound accounting is inaccurate: %+v", got)
	}
	encoded, err := json.Marshal(got)
	if err != nil || len(encoded) > 512 {
		t.Fatalf("response size=%d err=%v", len(encoded), err)
	}
}

func TestSearchByteCutoffCountsOnlyObservedResultOmissions(t *testing.T) {
	store := storage.NewEphemeralMemoryStore()
	for _, body := range []string{"alpha one", "alpha two", "alpha six"} {
		if err := store.PutObservation(context.Background(), observation(t, "demo.app", "example.test", body)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := New(store).Search(context.Background(), Request{Query: "alpha", ResultLimit: 1, SnippetBytes: 32, ResponseBytes: 512, ScanLimit: 3, ScanBytes: 58})
	if err != nil {
		t.Fatal(err)
	}
	if got.Scanned != 2 || got.ScannedBytes != 58 || !got.Truncated || got.Omitted != 1 || len(got.Results) != 1 {
		t.Fatalf("unknown matches were counted as omissions or byte-cutoff was hidden: %+v", got)
	}
}

type pageRequestRecord struct {
	request storage.ObservationPageRequest
	bytes   int
	hasMore bool
}

type recordingPageStore struct {
	storage.EncryptedStore
	requests []pageRequestRecord
}

func (s *recordingPageStore) ListObservationPage(ctx context.Context, request storage.ObservationPageRequest) (storage.ObservationPage, error) {
	page, err := s.EncryptedStore.ListObservationPage(ctx, request)
	s.requests = append(s.requests, pageRequestRecord{request: request, bytes: page.Bytes, hasMore: page.HasMore})
	return page, err
}

func TestSearchUsesByteAndRowBoundedPagesAndPropagatesHasMore(t *testing.T) {
	memory := storage.NewEphemeralMemoryStore()
	for i := 0; i < 12; i++ {
		body := fmt.Sprintf("alpha item %02d", i)
		if err := memory.PutObservation(context.Background(), observation(t, "demo.app", "example.test", body)); err != nil {
			t.Fatal(err)
		}
	}
	store := &recordingPageStore{EncryptedStore: memory}
	request := Request{Query: "alpha", ResultLimit: 20, SnippetBytes: 32, ResponseBytes: 4096, ScanLimit: 12, ScanBytes: 330}
	got, err := New(store).Search(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.requests) != 3 || store.requests[0].request.Limit != 4 || store.requests[0].bytes != 132 || !store.requests[0].hasMore || store.requests[1].request.Limit != 4 || store.requests[1].request.MaxBytes != 198 || store.requests[1].bytes != 132 || !store.requests[1].hasMore || store.requests[2].request.Limit != 4 || store.requests[2].request.MaxBytes != 66 || store.requests[2].bytes != 66 || !store.requests[2].hasMore {
		t.Fatalf("page requests or metadata were not bounded: %+v", store.requests)
	}
	if got.Scanned != 10 || got.ScannedBytes != 330 || !got.Truncated || got.Omitted != 0 || len(got.Results) != 10 {
		t.Fatalf("scan and omission accounting: %+v", got)
	}
	encoded, err := json.Marshal(got)
	if err != nil || len(encoded) > request.ResponseBytes {
		t.Fatalf("response size=%d err=%v", len(encoded), err)
	}
}

func TestSearchMarksResultOmissionAsTruncated(t *testing.T) {
	store := storage.NewEphemeralMemoryStore()
	for _, body := range []string{"alpha one", "alpha two"} {
		if err := store.PutObservation(context.Background(), observation(t, "demo.app", "example.test", body)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := New(store).Search(context.Background(), Request{Query: "alpha", ResultLimit: 1, SnippetBytes: 100, ResponseBytes: 4096, ScanLimit: 3, ScanBytes: MaxScanBytes})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Truncated || got.Omitted != 1 || got.Scanned != 2 {
		t.Fatalf("omission hidden: %+v", got)
	}
}

func TestSearchBoundsSerializedBytesAndUnicodeSnippet(t *testing.T) {
	store := storage.NewEphemeralMemoryStore()
	for _, body := range []string{"alpha éééééééé", "alpha éééééééé" + " extra"} {
		if err := store.PutObservation(context.Background(), observation(t, "demo.app", "example.test", body)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := New(store).Search(context.Background(), Request{Query: "alpha", ResultLimit: 2, SnippetBytes: 7, ResponseBytes: 128, ScanLimit: 3, ScanBytes: MaxScanBytes})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(got)
	if err != nil || len(encoded) > 128 {
		t.Fatalf("response size %d, %v", len(encoded), err)
	}
	if got.Omitted != 2 || !got.Truncated {
		t.Fatalf("byte omission not reported: %+v", got)
	}
	for _, result := range got.Results {
		if len(result.Snippet) > 7 || !utf8.ValidString(result.Snippet) {
			t.Fatalf("bad snippet %q", result.Snippet)
		}
	}
	got, err = New(store).Search(context.Background(), Request{Query: "alpha", ResultLimit: 2, SnippetBytes: 7, ResponseBytes: 4096, ScanLimit: 3, ScanBytes: MaxScanBytes})
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range got.Results {
		if result.Snippet != "alpha " || !utf8.ValidString(result.Snippet) {
			t.Fatalf("bad snippet %q", result.Snippet)
		}
	}
}

func TestSearchRejectsInvalidQueriesAndLimits(t *testing.T) {
	base := Request{Query: "alpha", ResultLimit: 1, SnippetBytes: 1, ResponseBytes: 1024, ScanLimit: 1, ScanBytes: MaxScanBytes}
	cases := []Request{base, base, base, base, base, base, base, base, base, base}
	cases[0].Query = "\xff"
	cases[1].Query = "a\x00b"
	cases[2].Query = strings.Repeat("a", 257)
	cases[3].Query = "---"
	cases[4].Query = "a b c d e f g h i j k l m n o p q"
	cases[5].ResultLimit = 101
	cases[6].SnippetBytes = 513
	cases[7].ScanLimit = MaxScan + 1
	cases[8].ScanBytes = MaxScanBytes + 1
	cases[9].ScanBytes = -1
	for i, request := range cases {
		if _, err := New(storage.NewEphemeralMemoryStore()).Search(context.Background(), request); err == nil {
			t.Fatalf("case %d accepted", i)
		}
	}
}

func TestSearchDefaultsOmittedAggregateByteLimitToHardMaximum(t *testing.T) {
	store := storage.NewEphemeralMemoryStore()
	if err := store.PutObservation(context.Background(), observation(t, "demo.app", "example.test", "alpha one")); err != nil {
		t.Fatal(err)
	}
	request := Request{Query: "alpha", ResultLimit: 1, SnippetBytes: 32, ResponseBytes: 512, ScanLimit: 1}
	got, err := New(store).Search(context.Background(), request)
	if err != nil {
		t.Fatalf("request without the new optional limit was rejected: %v", err)
	}
	if got.Scanned != 1 || got.ScannedBytes > MaxScanBytes {
		t.Fatalf("default scan byte limit was not applied: %+v", got)
	}
}

type overlongPageStore struct{ storage.EncryptedStore }

func (overlongPageStore) ListObservationPage(context.Context, storage.ObservationPageRequest) (storage.ObservationPage, error) {
	return storage.ObservationPage{Observations: make([]capture.SanitizedObservation, StorePage+1)}, nil
}

func TestSearchRejectsStorePageBeyondHardCap(t *testing.T) {
	store := overlongPageStore{storage.NewEphemeralMemoryStore()}
	_, err := New(store).Search(context.Background(), Request{Query: "alpha", ResultLimit: 1, SnippetBytes: 8, ResponseBytes: 1024, ScanLimit: StorePage, ScanBytes: MaxScanBytes})
	if err == nil {
		t.Fatal("overlong store page accepted")
	}
}

func TestSearchStopsAtAggregateBudgetForMaximumSizeObservations(t *testing.T) {
	store := storage.NewEphemeralMemoryStore()
	for i := 0; i < 5; i++ {
		suffix := fmt.Sprintf("%05d", i)
		body := "marker " + strings.Repeat("x", capture.MaxObservationTextBytes-len("marker ")-len(suffix)) + suffix
		o, err := capture.SanitizeObservation("demo.app", "example.test", body, capture.MaxObservationTextBytes)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.PutObservation(context.Background(), o); err != nil {
			t.Fatal(err)
		}
	}
	got, err := New(store).Search(context.Background(), Request{Query: "marker", ResultLimit: 2, SnippetBytes: 64, ResponseBytes: 4096, ScanLimit: 5, ScanBytes: MaxScanBytes})
	if err != nil {
		t.Fatal(err)
	}
	if got.Scanned != 1 || got.ScannedBytes != capture.MaxObservationTextBytes+len("demo.app")+len("example.test") || !got.Truncated || got.Omitted != 0 {
		t.Fatalf("maximum-size rows bypassed the aggregate work budget: %+v", got)
	}
	encoded, err := json.Marshal(got)
	if err != nil || len(encoded) > 4096 {
		t.Fatalf("bounded response size=%d err=%v", len(encoded), err)
	}
}
