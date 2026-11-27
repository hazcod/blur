package client

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/localapi"
	"github.com/hazcod/blur/internal/preferences"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

func TestSafariDocumentReceiverNotifiesPublishedSourcesAfterPolicyAdmission(t *testing.T) {
	ctx := context.Background()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "safari.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	prefs, err := preferences.NewStore(filepath.Join(t.TempDir(), "preferences.json"))
	if err != nil {
		t.Fatal(err)
	}
	config := preferences.DefaultConfig()
	config.CaptureEnabled = true
	config.SafariPageCaptureEnabled = true
	if err := prefs.Save(ctx, config); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	grants, err := localapi.NewGrantStore(func() time.Time { return now }, nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := grants.Issue("safari-extension", []localapi.Scope{localapi.ScopeCaptureIngest}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	documents, observations := 0, 0
	receiver, err := newSafariPageReceiver(grants, store, prefs, func() bool { return true }, func() time.Time { return now },
		func(capture.SanitizedObservation, time.Time) { observations++ },
		func(summary capture.CaptureSummary, fresh bool) {
			documents++
			check, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			if err := prefs.WithConfig(check, func(preferences.Config) error { return nil }); err != nil {
				t.Error("document callback ran while capture policy remained locked", err)
			}
			if _, found, err := store.GetCapture(ctx, summary.ContentID()); err != nil || !found || !fresh {
				t.Error("notification preceded durable publication", found, fresh, err)
			}
		})
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Pause()
	text := strings.Repeat("Compiler evidence. ", 18000) + " RollbackownerMira"
	sequence := 0
	for at := 0; at < len(text); sequence++ {
		next := min(at+32768, len(text))
		payload, _ := json.Marshal(map[string]any{"browser": "com.apple.safari", "url": "https://example.test", "title": "Project", "text": text[at:next], "active": true, "privacy_known": true, "private": false, "captured_at": now, "complete": next == len(text), "document_id": strings.Repeat("a", 32), "source_key": strings.Repeat("b", 64), "revision": 1, "sequence": sequence, "structure_limited": false})
		proof, err := localapi.NewProof(grant, localapi.ScopeCaptureIngest, sha256.Sum256(payload), now, nil)
		if err != nil {
			t.Fatal(err)
		}
		wire, _ := json.Marshal(map[string]any{"version": 2, "payload": json.RawMessage(payload), "proof": map[string]any{"grant_id": grant.ID, "client_id": grant.ClientID, "scope": localapi.ScopeCaptureIngest, "timestamp": now, "nonce": base64.RawURLEncoding.EncodeToString(proof.Nonce), "mac": hex.EncodeToString(proof.MAC[:])}})
		if err := receiver.Accept(ctx, wire); err != nil {
			t.Fatal(err)
		}
		at = next
	}
	if documents != 1 || observations != 0 {
		t.Fatalf("multipart publication bypassed shared document notification: documents=%d observations=%d", documents, observations)
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
	hits, err := store.SearchExact(ctx, search.ExactQuery{Terms: []string{"rollbackownermira"}, TopK: 8, MaxRows: 1000, MaxBytes: search.MaxIndexBytes})
	if err != nil || len(hits.Hits) != 1 {
		t.Fatalf("Safari receiver discarded indexed tail: %+v %v", hits, err)
	}
}
