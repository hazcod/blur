package client

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/preferences"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

func TestNativeCaptureCommitUsesEncryptedSearchableStoreWithoutPlaintext(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "memory.db")
	key := securestore.OfflineFixtureKey()
	store, err := durable.OpenDurable(path, key, durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	prefs, err := preferences.NewStore(filepath.Join(dir, "preferences.json"))
	if err != nil {
		t.Fatal(err)
	}
	config := preferences.DefaultConfig()
	config.CaptureEnabled = true
	if err := prefs.Save(ctx, config); err != nil {
		t.Fatal(err)
	}
	sink := &nativeCaptureCommitSink{store: store, preferences: prefs}
	marker := "Synthetic opaque canvas Zephyr tail evidence"
	observation, err := capture.SanitizeObservation("dev.herdr.app", "", strings.Repeat("Synthetic visible paragraph. ", 100)+marker+"\napi_key=synthetic-private-value", 256<<10)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(observation.Text(), "synthetic-private-value") {
		t.Fatal("source was not redacted")
	}
	at := time.Now().UTC()
	if err := sink.PutObservationAt(ctx, observation, at); err != nil {
		t.Fatal(err)
	}
	client := NewClient(store)
	hits, err := client.SearchMemoryPageContext(ctx, "Zephyr")
	client.Close()
	if err != nil || len(hits.Hits) != 1 || hits.Hits[0].SourceID != observation.ContentID() {
		t.Fatal("saved visual text not immediately searchable", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	storedBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(storedBytes, []byte(marker)) || bytes.Contains(storedBytes, []byte("synthetic-private-value")) {
		t.Fatal("plaintext leaked into encrypted store")
	}
	store, err = durable.OpenDurable(path, key, durable.OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	got, found, err := store.GetObservation(ctx, observation.ContentID())
	if err != nil || !found || got.Text() != observation.Text() || got.AppID() != observation.AppID() {
		t.Fatal("encrypted source changed on reopen", err)
	}
}
