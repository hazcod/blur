package durable

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nielsgroen/blur/internal/capture"
	"github.com/nielsgroen/blur/internal/platform"
	"github.com/nielsgroen/blur/internal/securestore"
	"github.com/nielsgroen/blur/internal/storage"
	"github.com/nielsgroen/blur/internal/storage/physical/bolt"
)

func TestOpenDurableRequiresExplicitCreateOrOpenMode(t *testing.T) {
	key := durableTestKey(t, 0x21)
	path := durableTestPath(t)

	if _, err := OpenDurable(path, key, OpenExisting); !errors.Is(err, ErrMissing) {
		t.Fatalf("OpenDurable(missing, OpenExisting) error = %v, want ErrMissing", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("OpenExisting(missing) created a database: %v", err)
	}

	created, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatalf("OpenDurable(CreateNew) error = %v", err)
	}
	if err := created.Close(); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenDurable(path, key, CreateNew); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("OpenDurable(existing, CreateNew) error = %v, want ErrAlreadyExists", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, original) {
		t.Fatalf("CreateNew(existing) changed the database: err=%v", err)
	}

	opened, err := OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatalf("OpenDurable(OpenExisting) error = %v", err)
	}
	if err := opened.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenDurable(path, key, OpenMode(255)); !errors.Is(err, ErrOpenMode) {
		t.Fatalf("OpenDurable(invalid mode) error = %v, want ErrOpenMode", err)
	}
}

func TestOpenDurableRejectsMissingZeroKeyWithoutCreating(t *testing.T) {
	path := durableTestPath(t)
	if _, err := OpenDurable(path, securestore.Key{}, CreateNew); !errors.Is(err, ErrWrongKey) {
		t.Fatalf("OpenDurable(zero key) error = %v, want ErrWrongKey", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("zero-key open created a store: %v", err)
	}
}

func TestOpenDurableWrongKeyAndCorruptFilesDoNotChangeBytes(t *testing.T) {
	path := durableTestPath(t)
	key := durableTestKey(t, 0x31)
	store, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := OpenDurable(path, durableTestKey(t, 0x32), OpenExisting); !errors.Is(err, ErrWrongKey) {
		t.Fatalf("OpenDurable(wrong key) error = %v, want ErrWrongKey", err)
	}
	afterWrongKey, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(afterWrongKey, original) {
		t.Fatalf("wrong-key open changed database bytes: %v", err)
	}

	corruptPath := durableTestPath(t)
	const sentinel = "SENTINEL_MALFORMED_DURABLE_DATABASE"
	if err := os.WriteFile(corruptPath, []byte(sentinel), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenDurable(corruptPath, key, OpenExisting); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("OpenDurable(malformed) error = %v, want ErrCorrupt", err)
	}
	got, err := os.ReadFile(corruptPath)
	if err != nil || string(got) != sentinel {
		t.Fatalf("malformed bytes changed: got=%q err=%v", got, err)
	}
}

func TestOpenDurablePersistsEncryptedObservationsWithoutPlaintextArtifacts(t *testing.T) {
	const sentinel = "SENTINEL_DURABLE_PRIVATE_VALUE_7391"
	const logicalID = "SENTINEL_DURABLE_LOGICAL_ID_7391"
	ctx := context.Background()
	var logs bytes.Buffer
	oldLogWriter := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(oldLogWriter)
	path := durableTestPath(t)
	key := durableTestKey(t, 0x41)
	store, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := capture.SanitizeObservation("example.app", "example.com", sentinel, 256)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservation(ctx, observation); err != nil {
		t.Fatalf("PutObservation() error = %v", err)
	}
	duplicateErr := store.PutObservation(ctx, observation)
	if !errors.As(duplicateErr, new(*storage.StoreError)) || strings.Contains(duplicateErr.Error(), sentinel) || strings.Contains(duplicateErr.Error(), logicalID) {
		t.Fatalf("duplicate error disclosed record contents: %v", duplicateErr)
	}
	if err := store.Update(ctx, func(tx *Tx) error {
		return tx.PutIndexRecord("sentinel", logicalID, []byte(sentinel))
	}); err != nil {
		t.Fatalf("PutIndexRecord() error = %v", err)
	}
	id := observation.ContentID()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatalf("OpenDurable(reopen) error = %v", err)
	}
	defer reopened.Close()
	got, found, err := reopened.GetObservation(ctx, id)
	if err != nil || !found || got.ContentID() != id || got.Text() != sentinel {
		t.Fatalf("GetObservation() = (%q, %t, %v), want recovered encrypted value", got.Text(), found, err)
	}
	page, err := reopened.ListObservationPage(ctx, storage.ObservationPageRequest{Limit: 1, MaxBytes: storage.MaxObservationPageBytes})
	if err != nil || len(page.Observations) != 1 || page.Observations[0].ContentID() != id {
		t.Fatalf("ListObservationPage() = (%d, %v), want persisted observation", len(page.Observations), err)
	}

	needles := [][]byte{[]byte(sentinel), []byte(logicalID), []byte(id), []byte("example.app"), []byte("example.com")}
	err = filepath.WalkDir(filepath.Dir(path), func(filePath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		artifact, err := os.ReadFile(filePath)
		if err != nil {
			return err
		}
		for _, needle := range needles {
			if bytes.Contains(artifact, needle) {
				return fmt.Errorf("plaintext sentinel found in physical artifact")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("physical artifact scan: %v", err)
	}
	for _, needle := range needles {
		if bytes.Contains(logs.Bytes(), needle) {
			t.Fatal("plaintext sentinel found in durable logs")
		}
	}
}

func TestOpenDurableRejectsUnsupportedSchemaWithoutOverwrite(t *testing.T) {
	path := durableTestPath(t)
	key := durableTestKey(t, 0x51)
	writeSchemaVersionFixture(t, path, key, OpenSchemaVersion+1)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenDurable(path, key, OpenExisting); !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("OpenDurable(unsupported schema) error = %v, want ErrUnsupportedSchema", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, original) {
		t.Fatalf("unsupported schema open changed bytes: %v", err)
	}
}

func TestDurableDetectsRecordSwapAfterManifestConsistentRewrite(t *testing.T) {
	path := durableTestPath(t)
	key := durableTestKey(t, 0x52)
	store, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	first, err := capture.SanitizeObservation("first.app", "first.example", "password: first secret", 128)
	if err != nil {
		t.Fatal(err)
	}
	second, err := capture.SanitizeObservation("second.app", "second.example", "password: second secret", 128)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservation(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservation(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	firstKey := store.logicalKey(groupRecord, kindObservation, "", first.ContentID())
	secondKey := store.logicalKey(groupRecord, kindObservation, "", second.ContentID())
	err = store.state.db.Update(context.Background(), func(writer bolt.Writer) error {
		tx := &Tx{store: store, writer: writer, active: true}
		if err := tx.loadManifestLocked(); err != nil {
			return err
		}
		firstEnvelope, firstFound, err := writer.Get(firstKey)
		if err != nil || !firstFound {
			return ErrCorrupt
		}
		secondEnvelope, secondFound, err := writer.Get(secondKey)
		if err != nil || !secondFound {
			clear(firstEnvelope)
			return ErrCorrupt
		}
		if err := tx.writeEntryLocked(firstKey, secondEnvelope, true); err != nil {
			clear(firstEnvelope)
			clear(secondEnvelope)
			return err
		}
		if err := tx.writeEntryLocked(secondKey, firstEnvelope, true); err != nil {
			clear(firstEnvelope)
			clear(secondEnvelope)
			return err
		}
		clear(firstEnvelope)
		clear(secondEnvelope)
		return tx.flushManifestLocked()
	})
	clear(firstKey)
	clear(secondKey)
	if err != nil {
		t.Fatalf("test record swap transaction: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatalf("manifest-consistent swap should pass set verification before record auth: %v", err)
	}
	defer reopened.Close()
	if _, _, err := reopened.GetObservation(context.Background(), first.ContentID()); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("GetObservation(swapped record) error = %v, want ErrCorrupt", err)
	}
}

func TestDurableManifestDetectsUnauthorizedInsertionDeletionAndTamper(t *testing.T) {
	for _, scenario := range []string{"insert", "delete", "manifest-tamper", "schema-tamper"} {
		t.Run(scenario, func(t *testing.T) {
			path := durableTestPath(t)
			key := durableTestKey(t, 0x53)
			store, err := OpenDurable(path, key, CreateNew)
			if err != nil {
				t.Fatal(err)
			}
			observation, err := capture.SanitizeObservation("manifest.app", "manifest.example", "password: manifest secret", 128)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.PutObservation(context.Background(), observation); err != nil {
				t.Fatal(err)
			}
			recordKey := store.logicalKey(groupRecord, kindObservation, "", observation.ContentID())
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			physical, err := bolt.OpenExisting(path)
			if err != nil {
				t.Fatal(err)
			}
			err = physical.Update(context.Background(), func(writer bolt.Writer) error {
				switch scenario {
				case "insert":
					return writer.Put([]byte("unauthorized-physical-key"), []byte("unauthorized-value"))
				case "delete":
					return writer.Delete(recordKey)
				case "manifest-tamper", "schema-tamper":
					key := manifestKey
					if scenario == "schema-tamper" {
						key = schemaKey
					}
					envelope, found, err := writer.Get(key)
					if err != nil || !found || len(envelope) == 0 {
						return ErrCorrupt
					}
					envelope[len(envelope)-1] ^= 0x80
					return writer.Put(key, envelope)
				default:
					return ErrCorrupt
				}
			})
			clear(recordKey)
			if err != nil {
				t.Fatalf("raw %s mutation: %v", scenario, err)
			}
			if err := physical.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := OpenDurable(path, key, OpenExisting); !errors.Is(err, ErrCorrupt) {
				if err != nil && strings.Contains(err.Error(), "unauthorized") {
					t.Fatal("open error disclosed a physical sentinel")
				}
				t.Fatalf("OpenDurable(after %s) error = %v, want ErrCorrupt", scenario, err)
			}
		})
	}
}

func TestDurableUpdateAtomicallyRollsBackObservationAndIndexExtension(t *testing.T) {
	path := durableTestPath(t)
	store, err := OpenDurable(path, durableTestKey(t, 0x57), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	observation, err := capture.SanitizeObservation("atomic.app", "atomic.example", "password: atomic rollback", 128)
	if err != nil {
		t.Fatal(err)
	}
	writeErr := store.Update(context.Background(), func(tx *Tx) error {
		if err := tx.PutObservation(observation); err != nil {
			return err
		}
		if err := tx.PutIndexRecord("terms", "atomic-index-id", []byte("synthetic-index-value")); err != nil {
			return err
		}
		return errors.New("callback failure containing sentinel must be hidden")
	})
	if !errors.Is(writeErr, ErrTransaction) {
		t.Fatalf("Update(callback failure) error = %v, want ErrTransaction", writeErr)
	}
	if strings.Contains(writeErr.Error(), "sentinel") {
		t.Fatal("transaction error disclosed callback text")
	}
	const customErrorCode = "SENTINEL_PRIVATE_ERROR_CODE"
	writeErr = store.Update(context.Background(), func(*Tx) error {
		return &storage.StoreError{Code: storage.ErrorCode(customErrorCode)}
	})
	if !errors.Is(writeErr, ErrTransaction) || strings.Contains(writeErr.Error(), customErrorCode) {
		t.Fatalf("transaction error preserved custom code: %v", writeErr)
	}
	const wrappedErrorText = "SENTINEL_WRAPPED_CORRUPTION_CONTEXT"
	writeErr = store.Update(context.Background(), func(*Tx) error {
		return fmt.Errorf("%s: %w", wrappedErrorText, ErrCorrupt)
	})
	if !errors.Is(writeErr, ErrCorrupt) || strings.Contains(writeErr.Error(), wrappedErrorText) {
		t.Fatalf("wrapped corruption error disclosed callback text: %v", writeErr)
	}
	if _, found, err := store.GetObservation(context.Background(), observation.ContentID()); err != nil || found {
		t.Fatalf("rolled-back observation found=%t err=%v", found, err)
	}
	indexKey := store.logicalKey(groupRecord, kindIndex, "terms", "atomic-index-id")
	defer clear(indexKey)
	err = store.view(context.Background(), func(reader bolt.Reader) error {
		found, err := reader.Has(indexKey)
		if err != nil {
			return err
		}
		if found {
			return ErrCorrupt
		}
		return nil
	})
	if err != nil {
		t.Fatalf("rolled-back extension remained: %v", err)
	}
}

func TestDurablePageSkipsDecryptingOverBudgetRecord(t *testing.T) {
	path := durableTestPath(t)
	store, err := OpenDurable(path, durableTestKey(t, 0x58), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	observation, err := capture.SanitizeObservation("page.app", "page.example", "password: page has more bytes than one", 128)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservation(context.Background(), observation); err != nil {
		t.Fatal(err)
	}
	key := store.logicalKey(groupRecord, kindObservation, "", observation.ContentID())
	defer clear(key)
	if err := store.state.db.Update(context.Background(), func(writer bolt.Writer) error {
		envelope, found, err := writer.Get(key)
		if err != nil || !found || len(envelope) == 0 {
			return ErrCorrupt
		}
		envelope[len(envelope)-1] ^= 0x01
		return writer.Put(key, envelope)
	}); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListObservationPage(context.Background(), storage.ObservationPageRequest{Limit: 1, MaxBytes: 1})
	if err != nil || len(page.Observations) != 0 || page.Bytes != 0 || !page.HasMore {
		t.Fatalf("over-budget tampered record was read/decrypted: page=(%d,%d,%t), err=%v", len(page.Observations), page.Bytes, page.HasMore, err)
	}
}

func TestDurableSchemaMigrationIsAtomicAndIdempotent(t *testing.T) {
	path := durableTestPath(t)
	key := durableTestKey(t, 0x54)
	writeSchemaVersionFixture(t, path, key, 0)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openDurableWithHook(path, key, OpenExisting, func() error { return errors.New("sentinel migration failure") }); !errors.Is(err, ErrMigration) {
		t.Fatalf("interrupted migration error = %v, want ErrMigration", err)
	}
	afterFailure, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(afterFailure, before) {
		t.Fatalf("failed migration changed durable bytes: err=%v", err)
	}
	store, err := OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatalf("OpenDurable(after interrupted migration) error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	afterMigration, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	store, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatalf("OpenDurable(repeated open) error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	afterRepeatedOpen, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(afterRepeatedOpen, afterMigration) {
		t.Fatalf("repeated open changed migrated bytes: err=%v", err)
	}
}

func TestDurableCrashAfterCommittedWriteCanReopen(t *testing.T) {
	if os.Getenv("BLUR_DURABLE_CRASH_HELPER") == "1" {
		path := os.Getenv("BLUR_DURABLE_CRASH_PATH")
		store, err := OpenDurable(path, durableTestKey(t, 0x55), OpenExisting)
		if err != nil {
			t.Fatalf("helper open: %v", err)
		}
		observation, err := capture.SanitizeObservation("crash.app", "crash.example", "password: committed before exit", 128)
		if err != nil {
			t.Fatalf("helper observation: %v", err)
		}
		if err := store.PutObservation(context.Background(), observation); err != nil {
			t.Fatalf("helper write: %v", err)
		}
		os.Exit(0)
	}
	path := durableTestPath(t)
	store, err := OpenDurable(path, durableTestKey(t, 0x55), CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestDurableCrashAfterCommittedWriteCanReopen$")
	command.Env = append(os.Environ(), "BLUR_DURABLE_CRASH_HELPER=1", "BLUR_DURABLE_CRASH_PATH="+path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("crash helper failed: %v; output=%s", err, output)
	}
	reopened, err := OpenDurable(path, durableTestKey(t, 0x55), OpenExisting)
	if err != nil {
		t.Fatalf("OpenDurable(after process exit) error = %v", err)
	}
	defer reopened.Close()
	want, err := capture.SanitizeObservation("crash.app", "crash.example", "password: committed before exit", 128)
	if err != nil {
		t.Fatal(err)
	}
	got, found, err := reopened.GetObservation(context.Background(), want.ContentID())
	if err != nil || !found || got.ContentID() != want.ContentID() {
		t.Fatalf("GetObservation(after process exit) found=%t err=%v", found, err)
	}
}

func TestDurable100kTinyExtensionRecords(t *testing.T) {
	if testing.Short() {
		t.Skip("100k scale evidence is covered by the explicit long test run")
	}
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	path := durableTestPath(t)
	key := durableTestKey(t, 0x56)
	store, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	const total = 100_000
	const batchSize = 300
	value := []byte("x")
	for start := 0; start < total; start += batchSize {
		end := min(start+batchSize, total)
		if err := store.Update(ctx, func(tx *Tx) error {
			for id := start; id < end; id++ {
				if err := tx.PutIndexRecord("scale", fmt.Sprintf("record-%06d", id), value); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatalf("insert records %d..%d: %v", start, end, err)
		}
	}
	count := 0
	var after []byte
	defer func() { clear(after) }()
	for {
		var entries []bolt.Entry
		var more bool
		err = store.view(ctx, func(reader bolt.Reader) error {
			var scanErr error
			entries, more, scanErr = reader.Scan([]byte{groupRecord, kindIndex}, after, 256, bolt.MaxTransactionBytes-bolt.MaxKeyBytes)
			return mapStorePhysicalError(scanErr)
		})
		if err != nil {
			t.Fatalf("count extension records: %v", err)
		}
		if len(entries) == 0 {
			clearBoltEntries(entries)
			if more {
				t.Fatal("empty scan claimed more records")
			}
			break
		}
		count += len(entries)
		clear(after)
		after = bytes.Clone(entries[len(entries)-1].Key)
		clearBoltEntries(entries)
		if !more {
			break
		}
	}
	if count != total {
		t.Fatalf("stored extension record count = %d; want %d", count, total)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenDurable(path, key, OpenExisting)
	if err != nil {
		t.Fatalf("OpenDurable(100k manifest verification): %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Minute {
		t.Fatalf("100k insert + manifest verification exceeded 3 minute bound: %v", elapsed)
	}
}

func durableTestPath(t *testing.T) string {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "private")
	if err := bolt.CreatePrivateDirectory(directory); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(directory, "store.db")
}

func writeSchemaVersionFixture(t *testing.T, path string, key securestore.Key, version uint32) {
	t.Helper()
	store, err := OpenDurable(path, key, CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	err = store.state.db.Update(context.Background(), func(writer bolt.Writer) error {
		tx := &Tx{store: store, writer: writer, active: true}
		if err := tx.loadManifestLocked(); err != nil {
			return err
		}
		plain := encodeSchema(version)
		envelope, err := store.seal(schemaKey, "schema", plain)
		clear(plain)
		if err != nil {
			return err
		}
		defer clear(envelope)
		if err := tx.writeEntryLocked(schemaKey, envelope, true); err != nil {
			return err
		}
		return tx.flushManifestLocked()
	})
	if err != nil {
		_ = store.Close()
		t.Fatalf("write schema fixture: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func durableTestKey(t *testing.T, seed byte) securestore.Key {
	t.Helper()
	backend := &testKeyBackend{}
	key, err := securestore.ResolveKey(context.Background(), backend, "test", bytes.NewReader(bytes.Repeat([]byte{seed}, 32)))
	if err != nil {
		t.Fatalf("ResolveKey() error = %v", err)
	}
	return key
}

type testKeyBackend struct{ value []byte }

func (*testKeyBackend) Capability(context.Context) (platform.CapabilityReport, error) {
	return platform.CapabilityReport{ID: "secret_store", State: platform.Available, Reason: "ready", Evidence: "test"}, nil
}
func (b *testKeyBackend) Get(context.Context, string, int) ([]byte, error) {
	if b.value == nil {
		return nil, &securestore.SecretError{State: securestore.NotFound}
	}
	return bytes.Clone(b.value), nil
}
func (b *testKeyBackend) Put(_ context.Context, _ string, value []byte) error {
	b.value = bytes.Clone(value)
	return nil
}
func (b *testKeyBackend) Delete(context.Context, string) error { b.value = nil; return nil }
