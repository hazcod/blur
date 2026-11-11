package storage_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/platform"
	"github.com/hazcod/blur/internal/privacy"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/durable"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

type storeOpener struct {
	name string
	open func(*testing.T) storage.EncryptedStore
}

func storeOpeners() []storeOpener {
	return []storeOpener{
		{name: "ephemeral", open: func(*testing.T) storage.EncryptedStore { return storage.NewEphemeralMemoryStore() }},
		{name: "durable", open: func(t *testing.T) storage.EncryptedStore {
			t.Helper()
			directory := filepath.Join(t.TempDir(), "private")
			if err := bolt.CreatePrivateDirectory(directory); err != nil {
				t.Fatal(err)
			}
			store, err := durable.OpenDurable(filepath.Join(directory, "store.db"), contractKey(t), durable.CreateNew)
			if err != nil {
				t.Fatalf("OpenDurable() error = %v", err)
			}
			return store
		}},
	}
}

func TestEncryptedStoreContractObservationCopiesAndDuplicates(t *testing.T) {
	for _, opener := range storeOpeners() {
		t.Run(opener.name, func(t *testing.T) {
			store := opener.open(t)
			t.Cleanup(func() { _ = store.Close() })
			ctx := context.Background()
			observation := contractObservation(t, "contract observation")
			if err := store.PutObservation(ctx, observation); err != nil {
				t.Fatalf("PutObservation() error = %v", err)
			}
			if err := store.PutObservation(ctx, observation); !contractStoreError(err, storage.Duplicate) {
				t.Fatalf("duplicate PutObservation() error = %v, want Duplicate", err)
			}
			counts := observation.RedactionCounts()
			if len(counts) == 0 {
				t.Fatal("fixture must carry sanitized redaction metadata")
			}
			counts[0].Rule = "MUTATED"
			got, found, err := store.GetObservation(ctx, observation.ContentID())
			if err != nil || !found || got.ContentID() != observation.ContentID() || got.RedactionCounts()[0].Rule == "MUTATED" {
				t.Fatalf("GetObservation() did not return an independent copy: found=%t err=%v", found, err)
			}
		})
	}
}

func TestEncryptedStoreContractPreservesCaptureTimesWithoutChangingSourceID(t *testing.T) {
	for _, opener := range storeOpeners() {
		t.Run(opener.name, func(t *testing.T) {
			store := opener.open(t)
			t.Cleanup(func() { _ = store.Close() })
			observation := contractObservation(t, "timestamped contract observation")
			capturedAt := time.Date(2026, 10, 2, 3, 4, 5, 0, time.UTC)
			if err := store.PutObservationAt(context.Background(), observation, time.Time{}); !contractStoreError(err, storage.Invalid) {
				t.Fatalf("zero capture time error = %v, want Invalid", err)
			}
			if err := store.PutObservationAt(context.Background(), observation, capturedAt); err != nil {
				t.Fatal(err)
			}
			page, err := store.ListObservationPage(context.Background(), storage.ObservationPageRequest{Limit: 2, MaxBytes: storage.MaxObservationPageBytes})
			if err != nil || len(page.Observations) != 1 || len(page.CapturedAt) != 1 || !page.CapturedAt[0].Equal(capturedAt) {
				t.Fatalf("timestamped page = (%d observations, %v, %v), want capture time %s", len(page.Observations), page.CapturedAt, err, capturedAt)
			}
			if page.Observations[0].ContentID() != observation.ContentID() {
				t.Fatal("capture metadata changed the stable content ID")
			}
		})
	}
}

func TestEncryptedStoreContractDeletesSourceRecordIdempotently(t *testing.T) {
	for _, opener := range storeOpeners() {
		t.Run(opener.name, func(t *testing.T) {
			store := opener.open(t)
			t.Cleanup(func() { _ = store.Close() })
			ctx := context.Background()
			observation := contractObservation(t, "delete me")
			if err := store.PutObservation(ctx, observation); err != nil {
				t.Fatal(err)
			}
			if err := store.DeleteObservationRecord(ctx, "invalid"); !contractStoreError(err, storage.Invalid) {
				t.Fatalf("invalid deletion ID error = %v", err)
			}
			cancelled, cancel := context.WithCancel(ctx)
			cancel()
			if err := store.DeleteObservationRecord(cancelled, observation.ContentID()); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled deletion error = %v", err)
			}
			if _, found, err := store.GetObservation(ctx, observation.ContentID()); err != nil || !found {
				t.Fatalf("cancelled deletion changed source: found=%t err=%v", found, err)
			}
			if err := store.DeleteObservationRecord(ctx, observation.ContentID()); err != nil {
				t.Fatalf("DeleteObservationRecord(): %v", err)
			}
			if _, found, err := store.GetObservation(ctx, observation.ContentID()); err != nil || found {
				t.Fatalf("deleted source still retrievable: found=%t err=%v", found, err)
			}
			page, err := store.ListObservationPage(ctx, storage.ObservationPageRequest{Limit: 1, MaxBytes: storage.MaxObservationPageBytes})
			if err != nil || len(page.Observations) != 0 || page.HasMore {
				t.Fatalf("deleted source still listed: %+v err=%v", page, err)
			}
			if err := store.DeleteObservationRecord(ctx, observation.ContentID()); err != nil {
				t.Fatalf("repeat deletion: %v", err)
			}
			if err := store.PutObservation(ctx, observation); err != nil {
				t.Fatalf("reinsert after deletion: %v", err)
			}
		})
	}
}

func TestEncryptedStoreContractObservationPagesAreStableAndOpaqueOrdered(t *testing.T) {
	for _, opener := range storeOpeners() {
		t.Run(opener.name, func(t *testing.T) {
			store := opener.open(t)
			t.Cleanup(func() { _ = store.Close() })
			ctx := context.Background()
			for _, body := range []string{"page-a", "page-b", "page-c", "page-d"} {
				if err := store.PutObservation(ctx, contractObservation(t, body)); err != nil {
					t.Fatal(err)
				}
			}

			first, err := store.ListObservationPage(ctx, storage.ObservationPageRequest{Limit: 1, MaxBytes: storage.MaxObservationPageBytes})
			if err != nil || len(first.Observations) != 1 || !first.HasMore {
				t.Fatalf("first page = (%d, %t, %v), want one row and more", len(first.Observations), first.HasMore, err)
			}
			repeated, err := store.ListObservationPage(ctx, storage.ObservationPageRequest{Limit: 1, MaxBytes: storage.MaxObservationPageBytes})
			if err != nil || len(repeated.Observations) != 1 || repeated.Observations[0].ContentID() != first.Observations[0].ContentID() {
				t.Fatalf("repeated first page changed order: %v", err)
			}

			seen := make(map[string]bool)
			after := ""
			for {
				page, err := store.ListObservationPage(ctx, storage.ObservationPageRequest{Limit: 1, MaxBytes: storage.MaxObservationPageBytes, After: after})
				if err != nil {
					t.Fatal(err)
				}
				if len(page.Observations) == 0 {
					if page.HasMore {
						t.Fatal("empty page claimed more rows without a cursor advance")
					}
					break
				}
				id := page.Observations[0].ContentID()
				if seen[id] {
					t.Fatalf("pagination repeated %s", id)
				}
				seen[id] = true
				after = id
			}
			if len(seen) != 4 {
				t.Fatalf("paged IDs count = %d, want 4", len(seen))
			}
		})
	}
}

func TestEncryptedStoreContractRejectsOversizeObservationBeforeCopy(t *testing.T) {
	for _, opener := range storeOpeners() {
		t.Run(opener.name, func(t *testing.T) {
			store := opener.open(t)
			t.Cleanup(func() { _ = store.Close() })
			large, err := capture.SanitizeObservation("example.app", "example.com", strings.Repeat("x", 512<<10), capture.MaxObservationTextBytes)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.PutObservation(context.Background(), large); err != nil {
				t.Fatal(err)
			}
			request := storage.ObservationPageRequest{Limit: 4, MaxBytes: 64}
			page, err := store.ListObservationPage(context.Background(), request)
			if err != nil || len(page.Observations) != 0 || page.Bytes != 0 || !page.HasMore {
				t.Fatalf("oversize page = (%d rows, %d bytes, more=%t, err=%v)", len(page.Observations), page.Bytes, page.HasMore, err)
			}
			result := testing.Benchmark(func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					if _, err := store.ListObservationPage(context.Background(), request); err != nil {
						b.Fatal(err)
					}
				}
			})
			if result.AllocedBytesPerOp() > 32<<10 {
				t.Fatalf("over-budget observation was copied or decrypted: %d bytes/op", result.AllocedBytesPerOp())
			}
		})
	}
}

func TestEncryptedStoreContractDerivationsGenerationsAndActivation(t *testing.T) {
	for _, opener := range storeOpeners() {
		t.Run(opener.name, func(t *testing.T) {
			store := opener.open(t)
			t.Cleanup(func() { _ = store.Close() })
			ctx := context.Background()
			source := contractObservation(t, "source record")
			if err := store.PutObservation(ctx, source); err != nil {
				t.Fatal(err)
			}
			generation := storage.Generation{ID: "generation-a", DerivationIDs: []string{"derivation-a"}, SchemaVersion: 1, RedactionVersion: privacy.Version}
			if err := store.PutGeneration(ctx, generation); err != nil {
				t.Fatal(err)
			}
			if err := store.PutGeneration(ctx, generation); !contractStoreError(err, storage.Duplicate) {
				t.Fatalf("duplicate PutGeneration() error = %v", err)
			}
			if err := store.ActivateGeneration(ctx, generation.ID); !contractStoreError(err, storage.Incomplete) {
				t.Fatalf("ActivateGeneration(incomplete) error = %v", err)
			}
			derivation := contractDerivation(source.ContentID(), "derivation-a", generation.ID, storage.Active)
			if err := store.PutDerivation(ctx, derivation); err != nil {
				t.Fatal(err)
			}
			if err := store.PutDerivation(ctx, derivation); !contractStoreError(err, storage.Duplicate) {
				t.Fatalf("duplicate PutDerivation() error = %v", err)
			}
			derivation.SourceIDs[0] = "mutated"
			generation.DerivationIDs[0] = "mutated"
			if err := store.ActivateGeneration(ctx, "generation-a"); err != nil {
				t.Fatalf("ActivateGeneration(complete) error = %v", err)
			}
			active, found, err := store.ActiveGeneration(ctx)
			if err != nil || !found || active.ID != "generation-a" || active.DerivationIDs[0] != "derivation-a" {
				t.Fatalf("ActiveGeneration() = (%+v, %t, %v)", active, found, err)
			}
			active.DerivationIDs[0] = "mutated"
			stored, found, err := store.GetGeneration(ctx, "generation-a")
			if err != nil || !found || stored.DerivationIDs[0] != "derivation-a" {
				t.Fatalf("generation was not independently copied: %+v %t %v", stored, found, err)
			}
			got, found, err := store.GetDerivation(ctx, "derivation-a")
			if err != nil || !found || got.SourceIDs[0] != source.ContentID() {
				t.Fatalf("derivation was not independently copied: %+v %t %v", got, found, err)
			}
		})
	}
}

func TestEncryptedStoreContractDerivationAndGenerationPagesAreStable(t *testing.T) {
	for _, opener := range storeOpeners() {
		t.Run(opener.name, func(t *testing.T) {
			store := opener.open(t)
			t.Cleanup(func() { _ = store.Close() })
			ctx := context.Background()
			source := contractObservation(t, "ordered source")
			if err := store.PutObservation(ctx, source); err != nil {
				t.Fatal(err)
			}
			for _, suffix := range []string{"a", "b", "c", "d"} {
				generationID := "page-generation-" + suffix
				derivationID := "page-derivation-" + suffix
				derivation := contractDerivation(source.ContentID(), derivationID, generationID, storage.Active)
				if err := store.PutDerivation(ctx, derivation); err != nil {
					t.Fatalf("PutDerivation(%s): %v", suffix, err)
				}
				generation := storage.Generation{ID: generationID, DerivationIDs: []string{derivationID}, SchemaVersion: 1, RedactionVersion: privacy.Version}
				if err := store.PutGeneration(ctx, generation); err != nil {
					t.Fatalf("PutGeneration(%s): %v", suffix, err)
				}
			}

			derivationFirst, err := store.ListDerivations(ctx, 1, "")
			if err != nil || len(derivationFirst) != 1 {
				t.Fatalf("first derivation page = (%d, %v), want one row", len(derivationFirst), err)
			}
			derivationRepeated, err := store.ListDerivations(ctx, 1, "")
			if err != nil || len(derivationRepeated) != 1 || derivationRepeated[0].ID != derivationFirst[0].ID {
				t.Fatalf("repeated first derivation page changed order: %v", err)
			}
			derivationSeen := make(map[string]bool)
			after := ""
			for {
				page, err := store.ListDerivations(ctx, 1, after)
				if err != nil {
					t.Fatal(err)
				}
				if len(page) == 0 {
					break
				}
				if derivationSeen[page[0].ID] {
					t.Fatalf("derivation pagination repeated %q", page[0].ID)
				}
				derivationSeen[page[0].ID] = true
				after = page[0].ID
			}
			if len(derivationSeen) != 4 {
				t.Fatalf("paged derivation count = %d, want 4", len(derivationSeen))
			}

			generationFirst, err := store.ListGenerations(ctx, 1, "")
			if err != nil || len(generationFirst) != 1 {
				t.Fatalf("first generation page = (%d, %v), want one row", len(generationFirst), err)
			}
			generationRepeated, err := store.ListGenerations(ctx, 1, "")
			if err != nil || len(generationRepeated) != 1 || generationRepeated[0].ID != generationFirst[0].ID {
				t.Fatalf("repeated first generation page changed order: %v", err)
			}
			generationSeen := make(map[string]bool)
			after = ""
			for {
				page, err := store.ListGenerations(ctx, 1, after)
				if err != nil {
					t.Fatal(err)
				}
				if len(page) == 0 {
					break
				}
				if generationSeen[page[0].ID] {
					t.Fatalf("generation pagination repeated %q", page[0].ID)
				}
				generationSeen[page[0].ID] = true
				after = page[0].ID
			}
			if len(generationSeen) != 4 {
				t.Fatalf("paged generation count = %d, want 4", len(generationSeen))
			}
		})
	}
}

func TestEncryptedStoreContractCancellationAndClose(t *testing.T) {
	for _, opener := range storeOpeners() {
		t.Run(opener.name, func(t *testing.T) {
			store := opener.open(t)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := store.PutObservation(ctx, contractObservation(t, "cancelled")); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled PutObservation() error = %v", err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			if _, _, err := store.GetObservation(context.Background(), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); !contractStoreError(err, storage.Closed) {
				t.Fatalf("GetObservation(after close) error = %v, want Closed", err)
			}
		})
	}
}

func TestEncryptedStoreContractConcurrentReaders(t *testing.T) {
	for _, opener := range storeOpeners() {
		t.Run(opener.name, func(t *testing.T) {
			store := opener.open(t)
			t.Cleanup(func() { _ = store.Close() })
			observation := contractObservation(t, "concurrent readers")
			if err := store.PutObservation(context.Background(), observation); err != nil {
				t.Fatal(err)
			}
			var group sync.WaitGroup
			errs := make(chan error, 24)
			for i := 0; i < cap(errs); i++ {
				group.Add(1)
				go func() {
					defer group.Done()
					for n := 0; n < 4; n++ {
						got, found, err := store.GetObservation(context.Background(), observation.ContentID())
						if err != nil || !found || got.ContentID() != observation.ContentID() {
							errs <- fmt.Errorf("read found=%t error=%v", found, err)
							return
						}
					}
				}()
			}
			group.Wait()
			close(errs)
			for err := range errs {
				t.Error(err)
			}
		})
	}
}

func contractObservation(t *testing.T, value string) capture.SanitizedObservation {
	t.Helper()
	observation, err := capture.SanitizeObservation("example.app", "example.com", "password: synthetic\n"+value, 1024)
	if err != nil {
		t.Fatal(err)
	}
	return observation
}

func contractDerivation(sourceID, id, generationID string, state storage.DerivationState) storage.Derivation {
	return storage.Derivation{
		ID: id, GenerationID: generationID, State: state,
		SourceIDs: []string{sourceID}, SourceHashes: []string{fmt.Sprintf("%064x", 17)},
		ProcessorVersion: 1, SchemaVersion: 1, PromptVersion: 1, RedactionVersion: privacy.Version,
		ProviderModelDigest: fmt.Sprintf("%064x", 18), OutputHash: fmt.Sprintf("%064x", 19),
		CoveredSources: 1, InputTokens: 12, OutputTokens: 4, Latency: time.Millisecond,
	}
}

func contractStoreError(err error, code storage.ErrorCode) bool {
	var storeError *storage.StoreError
	return errors.As(err, &storeError) && storeError.Code == code
}

func contractKey(t *testing.T) securestore.Key {
	t.Helper()
	backend := &contractKeyBackend{}
	key, err := securestore.ResolveKey(context.Background(), backend, "contract", bytes.NewReader(bytes.Repeat([]byte{0x71}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	return key
}

type contractKeyBackend struct{ value []byte }

func (*contractKeyBackend) Capability(context.Context) (platform.CapabilityReport, error) {
	return platform.CapabilityReport{ID: "secret_store", State: platform.Available, Reason: "ready", Evidence: "test"}, nil
}
func (b *contractKeyBackend) Get(context.Context, string, int) ([]byte, error) {
	if b.value == nil {
		return nil, &securestore.SecretError{State: securestore.NotFound}
	}
	return bytes.Clone(b.value), nil
}
func (b *contractKeyBackend) Put(_ context.Context, _ string, value []byte) error {
	b.value = bytes.Clone(value)
	return nil
}
func (b *contractKeyBackend) Delete(context.Context, string) error { b.value = nil; return nil }
