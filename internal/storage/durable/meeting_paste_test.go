package durable

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/search"
)

func TestMeetingPasteFullStorageLimitPersistsEncryptedWhenModelBudgetIsExceeded(t *testing.T) {
	ctx := context.Background()
	path, key := durableTestPath(t), durableTestKey(t, 0xA7)
	store, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	extractor, err := notes.NewOllamaExtractor(server.URL, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	defer extractor.CloseIdleConnections()
	prefix := "Meeting: Platform planning\nAttendees: Niels, Maya\nTODO: Send the revised launch plan\n"
	tail := "\nFinal decision: tailmeetingdecision retains the complete source.\n"
	padding := capture.MaxObservationTextBytes - len(prefix) - len(tail)
	text := prefix + strings.Repeat("context ", padding/8) + strings.Repeat(" ", padding%8) + tail
	at := time.Date(2026, 11, 13, 15, 0, 0, 0, time.UTC)
	result, err := notes.NewService(store, extractor, func() time.Time { return at }).Add(ctx, text)
	if err != nil {
		t.Fatalf("complete %d-byte note rejected: %v", len(text), err)
	}
	if result.Processing != notes.ProcessingBudget || calls.Load() != 0 {
		t.Fatalf("oversized model input was sent or note save state lost: state=%s requests=%d", result.Processing, calls.Load())
	}
	if len(result.Proposals) != 1 || result.Proposals[0].Confirmed || result.Proposals[0].SourceID != result.SourceID {
		t.Fatalf("saved note proposals were activated or unlinked: %+v", result.Proposals)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	ciphertext, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte(prefix)) || bytes.Contains(ciphertext, []byte(tail)) {
		t.Fatal("meeting source appeared in plaintext in the encrypted store")
	}
	store, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	stored, found, err := store.GetObservation(ctx, result.SourceID)
	if err != nil || !found || stored.Text() != text || stored.Truncated() {
		t.Fatalf("reopened note was shortened: found=%t input=%d stored=%d truncated=%t err=%v", found, len(text), len(stored.Text()), stored.Truncated(), err)
	}
	date, _ := localDate(at)
	snapshot, found, err := store.GetTodaySnapshot(ctx, date)
	if err != nil || !found || len(snapshot.TodoCandidates) != 1 || snapshot.TodoCandidates[0].Confirmed || len(snapshot.ConfirmedTodos) != 0 || snapshot.TodoCandidates[0].SourceIDs[0] != result.SourceID {
		t.Fatalf("proposal did not persist as an unconfirmed source-linked candidate: found=%t snapshot=%+v err=%v", found, snapshot, err)
	}
	page, err := store.SearchExact(ctx, search.ExactQuery{Terms: []string{"tailmeetingdecision"}, TopK: 1, MaxRows: 10, MaxBytes: search.MaxIndexBytes})
	if err != nil || len(page.Hits) != 1 || page.Hits[0].SourceID != result.SourceID {
		t.Fatalf("large note's final decision was not indexed: page=%+v err=%v", page, err)
	}
}
