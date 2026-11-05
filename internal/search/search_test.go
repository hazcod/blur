package search

import (
	"context"
	"encoding/json"
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
	request := Request{Query: "CAFÉ plan", ResultLimit: 2, SnippetBytes: 32, ResponseBytes: 4096, ScanLimit: 10}
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
	got, err := New(store).Search(context.Background(), Request{Query: "alpha", ResultLimit: 1, SnippetBytes: 100, ResponseBytes: 4096, ScanLimit: 2})
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

func TestSearchMarksResultOmissionAsTruncated(t *testing.T) {
	store := storage.NewEphemeralMemoryStore()
	for _, body := range []string{"alpha one", "alpha two"} {
		if err := store.PutObservation(context.Background(), observation(t, "demo.app", "example.test", body)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := New(store).Search(context.Background(), Request{Query: "alpha", ResultLimit: 1, SnippetBytes: 100, ResponseBytes: 4096, ScanLimit: 3})
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
	got, err := New(store).Search(context.Background(), Request{Query: "alpha", ResultLimit: 2, SnippetBytes: 7, ResponseBytes: 128, ScanLimit: 3})
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
	got, err = New(store).Search(context.Background(), Request{Query: "alpha", ResultLimit: 2, SnippetBytes: 7, ResponseBytes: 4096, ScanLimit: 3})
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
	base := Request{Query: "alpha", ResultLimit: 1, SnippetBytes: 1, ResponseBytes: 1024, ScanLimit: 1}
	cases := []Request{base, base, base, base, base, base, base, base}
	cases[0].Query = "\xff"
	cases[1].Query = "a\x00b"
	cases[2].Query = strings.Repeat("a", 257)
	cases[3].Query = "---"
	cases[4].Query = "a b c d e f g h i j k l m n o p q"
	cases[5].ResultLimit = 101
	cases[6].SnippetBytes = 513
	cases[7].ScanLimit = 10001
	for i, request := range cases {
		if _, err := New(storage.NewEphemeralMemoryStore()).Search(context.Background(), request); err == nil {
			t.Fatalf("case %d accepted", i)
		}
	}
}

type overlongPageStore struct{ storage.EncryptedStore }

func (overlongPageStore) ListObservations(context.Context, int, string) ([]capture.SanitizedObservation, error) {
	return make([]capture.SanitizedObservation, StorePage+1), nil
}

func TestSearchRejectsStorePageBeyondHardCap(t *testing.T) {
	store := overlongPageStore{storage.NewEphemeralMemoryStore()}
	_, err := New(store).Search(context.Background(), Request{Query: "alpha", ResultLimit: 1, SnippetBytes: 8, ResponseBytes: 1024, ScanLimit: StorePage})
	if err == nil {
		t.Fatal("overlong store page accepted")
	}
}
