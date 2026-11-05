package bolt

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nielsgroen/blur/internal/storage/recordcodec"
)

func databasePath(t *testing.T) string {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "private")
	if err := CreatePrivateDirectory(directory); err != nil {
		t.Fatalf("CreatePrivateDirectory() error = %v", err)
	}
	return filepath.Join(directory, "store.db")
}

func openTestDB(t *testing.T) (*DB, string) {
	t.Helper()
	path := databasePath(t)
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, path
}

func TestOpenCreatesPrivateRegularFile(t *testing.T) {
	db, path := openTestDB(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("Lstat(database) error = %v", err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("database mode = %v, want regular file", info.Mode())
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("database permissions = %04o, want 0600", info.Mode().Perm())
	}
}

func TestExplicitOpenModesDoNotCreateOrOverwrite(t *testing.T) {
	t.Run("open existing missing", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "missing.db")
		if _, err := OpenExisting(path); !errors.Is(err, ErrNotFound) {
			t.Fatalf("OpenExisting(missing) error = %v, want ErrNotFound", err)
		}
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("OpenExisting(missing) created a file: Lstat error = %v", err)
		}
	})

	t.Run("create new existing", func(t *testing.T) {
		path := databasePath(t)
		original := []byte("SENTINEL_EXISTING_DATABASE_MUST_SURVIVE")
		if err := os.WriteFile(path, original, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := CreateNew(path); !errors.Is(err, ErrExists) {
			t.Fatalf("CreateNew(existing) error = %v, want ErrExists", err)
		}
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, original) {
			t.Fatalf("CreateNew(existing) changed bytes: got=%q err=%v", got, err)
		}
	})

	t.Run("create then open existing", func(t *testing.T) {
		path := databasePath(t)
		created, err := CreateNew(path)
		if err != nil {
			t.Fatalf("CreateNew() error = %v", err)
		}
		if err := created.Close(); err != nil {
			t.Fatal(err)
		}
		existing, err := OpenExisting(path)
		if err != nil {
			t.Fatalf("OpenExisting(created) error = %v", err)
		}
		if err := existing.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestOpenRefusesUnsafeDirectoryMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose POSIX directory permission bits")
	}
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "store.db")
	if _, err := Open(path); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Open(insecure directory) error = %v, want ErrInvalid", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("database path after refusal: Lstat error = %v, want no file", err)
	}
}

func TestOpenRefusesUnsafeDatabaseMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose POSIX file permission bits")
	}
	path := databasePath(t)
	if err := os.WriteFile(path, make([]byte, 0), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Open(insecure database mode) error = %v, want ErrInvalid", err)
	}
}

func TestOpenRefusesSymlinksAndNonRegularFiles(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Windows symlink creation requires runner-specific privilege")
		}
		target := filepath.Join(t.TempDir(), "target.db")
		if err := os.WriteFile(target, []byte("unchanged"), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(t.TempDir(), "linked.db")
		if err := os.Symlink(target, link); err != nil {
			t.Fatalf("Symlink fixture unavailable: %v", err)
		}
		if _, err := Open(link); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Open(symlink) error = %v, want ErrInvalid", err)
		}
		got, err := os.ReadFile(target)
		if err != nil || string(got) != "unchanged" {
			t.Fatalf("symlink target = (%q, %v), want unchanged file", got, err)
		}
	})

	t.Run("directory at database path", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "store.db")
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(path); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Open(directory) error = %v, want ErrInvalid", err)
		}
	})
}

func TestOpenWithDirectoryHookPinsDirectoryAgainstPathSwap(t *testing.T) {
	root := t.TempDir()
	checkedDirectory := filepath.Join(root, "checked")
	if err := CreatePrivateDirectory(checkedDirectory); err != nil {
		t.Fatal(err)
	}
	pinnedDirectory := filepath.Join(root, "pinned")
	replacementDirectory := checkedDirectory
	databasePath := filepath.Join(checkedDirectory, "store.db")
	db, err := openWithDirectoryHook(databasePath, func() error {
		if err := os.Rename(checkedDirectory, pinnedDirectory); err != nil {
			return err
		}
		return CreatePrivateDirectory(replacementDirectory)
	})
	if err != nil {
		t.Fatalf("openWithDirectoryHook() error = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(replacementDirectory, "store.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("database was redirected into replacement directory: Lstat error = %v", err)
	}
	if info, err := os.Lstat(filepath.Join(pinnedDirectory, "store.db")); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("pinned database = (%v, %v), want regular DB under opened directory", info, err)
	}
}

func TestOpaqueTransactionRoundTripAndCopyBoundaries(t *testing.T) {
	db, _ := openTestDB(t)
	ctx := context.Background()
	key := []byte("opaque-key-001")
	value := []byte{0x01, 0x02, 0xff, 0x80}
	if err := db.Update(ctx, func(tx Writer) error {
		return tx.Put(key, value)
	}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	key[0] = 'X'
	value[0] = 0xff

	var got []byte
	if err := db.View(ctx, func(tx Reader) error {
		var found bool
		var err error
		got, found, err = tx.Get([]byte("opaque-key-001"))
		if err != nil {
			return err
		}
		if !found {
			return errors.New("missing opaque key")
		}
		return nil
	}); err != nil {
		t.Fatalf("View() error = %v", err)
	}
	if !bytes.Equal(got, []byte{0x01, 0x02, 0xff, 0x80}) {
		t.Fatalf("Get() = %v, want stored bytes", got)
	}
	got[0] = 0xee
	if err := db.View(ctx, func(tx Reader) error {
		value, found, err := tx.Get([]byte("opaque-key-001"))
		if err != nil || !found || !bytes.Equal(value, []byte{0x01, 0x02, 0xff, 0x80}) {
			return fmt.Errorf("second Get() = (%v, %t, %v), want independent stored bytes", value, found, err)
		}
		return nil
	}); err != nil {
		t.Fatalf("View() after returned-slice mutation: %v", err)
	}
	if err := db.Update(ctx, func(tx Writer) error { return tx.Put([]byte("empty-value-key"), []byte{}) }); err != nil {
		t.Fatalf("Update(empty value) error = %v", err)
	}
	if err := db.View(ctx, func(tx Reader) error {
		value, found, err := tx.Get([]byte("empty-value-key"))
		if err != nil || !found || len(value) != 0 {
			return fmt.Errorf("Get(empty value) = (%v, %t, %v), want present empty value", value, found, err)
		}
		return nil
	}); err != nil {
		t.Fatalf("View(empty value): %v", err)
	}
}

func TestOpaqueHasChecksPresenceWithoutCopyingValue(t *testing.T) {
	db, _ := openTestDB(t)
	if err := db.Update(context.Background(), func(tx Writer) error {
		return tx.Put([]byte("presence-key"), bytes.Repeat([]byte{0x63}, 1<<20))
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		key  string
		want bool
	}{{"presence-key", true}, {"missing-key", false}} {
		err := db.View(context.Background(), func(tx Reader) error {
			found, err := tx.Has([]byte(tc.key))
			if err != nil || found != tc.want {
				return fmt.Errorf("Has(%q) = (%t, %v), want %t", tc.key, found, err, tc.want)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	result := testing.Benchmark(func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if err := db.View(context.Background(), func(tx Reader) error {
				_, err := tx.Has([]byte("presence-key"))
				return err
			}); err != nil {
				b.Fatal(err)
			}
		}
	})
	if result.AllocedBytesPerOp() > 32<<10 {
		t.Fatalf("Has() copied the stored value: %d bytes/op", result.AllocedBytesPerOp())
	}
}

func TestOpaquePrefixScanOrdersPagesAndReturnsCopies(t *testing.T) {
	db, _ := openTestDB(t)
	ctx := context.Background()
	fixtures := map[string]string{
		"other/a": "outside",
		"p/c":     "charlie",
		"p/a":     "alpha",
		"p/b":     "bravo",
	}
	if err := db.Update(ctx, func(tx Writer) error {
		for key, value := range fixtures {
			if err := tx.Put([]byte(key), []byte(value)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	entries, more, err := scanDB(t, db, []byte("p/"), nil, 2, 1024)
	if err != nil || !more || len(entries) != 2 || string(entries[0].Key) != "p/a" || string(entries[1].Key) != "p/b" {
		t.Fatalf("first Scan() = (%v, %t, %v), want ordered p/a,p/b and more", entries, more, err)
	}
	entries[0].Key[0] = 'x'
	entries[0].Value[0] = 'X'
	next, more, err := scanDB(t, db, []byte("p/"), []byte("p/b"), 2, 1024)
	if err != nil || more || len(next) != 1 || string(next[0].Key) != "p/c" || string(next[0].Value) != "charlie" {
		t.Fatalf("second Scan() = (%v, %t, %v), want independent p/c entry", next, more, err)
	}
}

func TestOpaquePrefixScanStopsBeforeCopyingEntryOverByteLimit(t *testing.T) {
	db, _ := openTestDB(t)
	ctx := context.Background()
	if err := db.Update(ctx, func(tx Writer) error {
		if err := tx.Put([]byte("scan/a"), []byte("123")); err != nil {
			return err
		}
		return tx.Put([]byte("scan/b"), []byte("12345"))
	}); err != nil {
		t.Fatal(err)
	}
	limit := len("scan/a") + len("123")
	entries, more, err := scanDB(t, db, []byte("scan/"), nil, 5, limit)
	if err != nil || !more || len(entries) != 1 || string(entries[0].Key) != "scan/a" {
		t.Fatalf("Scan(byte limit) = (%v, %t, %v), want first row and more", entries, more, err)
	}
	result := testing.Benchmark(func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, _, err := scanDB(t, db, []byte("scan/"), nil, 5, limit); err != nil {
				b.Fatal(err)
			}
		}
	})
	if result.AllocedBytesPerOp() > 32<<10 {
		t.Fatalf("Scan copied an over-limit value: %d bytes/op", result.AllocedBytesPerOp())
	}
}

func TestOpaquePrefixScanValidatesBoundsAndCancellation(t *testing.T) {
	db, _ := openTestDB(t)
	if err := db.Update(context.Background(), func(tx Writer) error {
		if err := tx.Put([]byte("prefix/a"), []byte("one")); err != nil {
			return err
		}
		return tx.Put([]byte("prefix/b"), []byte("two"))
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		limit int
		max   int
		ctx   context.Context
		want  error
	}{
		{name: "zero rows", limit: 0, max: 1, ctx: context.Background(), want: ErrInvalidTx},
		{name: "oversize byte cap", limit: 1, max: MaxTransactionBytes + 1, ctx: context.Background(), want: ErrInvalidTx},
		{name: "cancelled", limit: 1, max: 1, ctx: cancelledContext(), want: context.Canceled},
		{name: "cancelled after first copied row", limit: 2, max: 1024, ctx: cancelAfterChecksContext(3), want: context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := db.View(tc.ctx, func(tx Reader) error {
				entries, _, err := tx.Scan([]byte("prefix"), nil, tc.limit, tc.max)
				if tc.name == "cancelled after first copied row" && len(entries) != 0 {
					return fmt.Errorf("cancelled Scan() returned partial copies: %d", len(entries))
				}
				return err
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("Scan() error = %v, want %v", err, tc.want)
			}
		})
	}
}

func scanDB(t *testing.T, db *DB, prefix, after []byte, limit, maxBytes int) ([]Entry, bool, error) {
	t.Helper()
	var entries []Entry
	var more bool
	err := db.View(context.Background(), func(tx Reader) error {
		var err error
		entries, more, err = tx.Scan(prefix, after, limit, maxBytes)
		return err
	})
	return entries, more, err
}

func cancelledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

type cancelAfterChecks struct {
	context.Context
	remaining int
}

func cancelAfterChecksContext(allowedChecks int) context.Context {
	return &cancelAfterChecks{Context: context.Background(), remaining: allowedChecks}
}

func (ctx *cancelAfterChecks) Err() error {
	if ctx.remaining == 0 {
		return context.Canceled
	}
	ctx.remaining--
	return nil
}

func TestCloseAndReopenPreservesCommittedOpaqueBytes(t *testing.T) {
	path := databasePath(t)
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Update(context.Background(), func(tx Writer) error {
		return tx.Put([]byte("reopen-key"), []byte{0x00, 0x80, 0xff})
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("Open() after Close() error = %v", err)
	}
	defer reopened.Close()
	if err := reopened.View(context.Background(), func(tx Reader) error {
		value, found, err := tx.Get([]byte("reopen-key"))
		if err != nil || !found || !bytes.Equal(value, []byte{0x00, 0x80, 0xff}) {
			return fmt.Errorf("reopened Get() = (%v, %t, %v), want committed opaque bytes", value, found, err)
		}
		return nil
	}); err != nil {
		t.Fatalf("View() after reopen: %v", err)
	}
}

func TestUpdateRollsBackOnCallbackErrorWithoutReturningItsText(t *testing.T) {
	db, _ := openTestDB(t)
	const sentinel = "SENTINEL_PRIVATE_CALLBACK_ERROR_4102"
	err := db.Update(context.Background(), func(tx Writer) error {
		if err := tx.Put([]byte("opaque-key"), []byte("opaque-value")); err != nil {
			return err
		}
		return errors.New(sentinel)
	})
	if !errors.Is(err, ErrTransaction) || strings.Contains(err.Error(), sentinel) {
		t.Fatalf("Update() error = %v, want content-free ErrTransaction", err)
	}
	if err := db.View(context.Background(), func(tx Reader) error {
		if _, found, err := tx.Get([]byte("opaque-key")); err != nil || found {
			return fmt.Errorf("rolled-back key found=%t error=%v", found, err)
		}
		return nil
	}); err != nil {
		t.Fatalf("View() after rollback: %v", err)
	}
}

func TestTransactionsValidateAndBoundOpaqueInputs(t *testing.T) {
	db, _ := openTestDB(t)
	ctx := context.Background()
	if err := db.Update(ctx, func(tx Writer) error {
		if err := tx.Put(nil, []byte("value")); !errors.Is(err, ErrInvalidTx) {
			return fmt.Errorf("Put(empty key) error = %v, want ErrInvalidTx", err)
		}
		if err := tx.Put(make([]byte, MaxKeyBytes+1), []byte("value")); !errors.Is(err, ErrInvalidTx) {
			return fmt.Errorf("Put(oversize key) error = %v, want ErrInvalidTx", err)
		}
		if err := tx.Put([]byte("key"), make([]byte, MaxValueBytes+1)); !errors.Is(err, ErrInvalidTx) {
			return fmt.Errorf("Put(oversize value) error = %v, want ErrInvalidTx", err)
		}
		return nil
	}); !errors.Is(err, ErrInvalidTx) {
		t.Fatalf("Update(ignored invalid operations) error = %v, want poisoned ErrInvalidTx", err)
	}

	if err := db.Update(ctx, func(tx Writer) error {
		value := bytes.Repeat([]byte{0x33}, MaxValueBytes)
		for i := 0; i < MaxTransactionBytes/MaxValueBytes+1; i++ {
			if err := tx.Put([]byte(fmt.Sprintf("key-%d", i)), value); err != nil {
				return err
			}
		}
		return nil
	}); !errors.Is(err, ErrInvalidTx) {
		t.Fatalf("Update(transaction bytes exceeded) error = %v, want ErrInvalidTx", err)
	}
	if err := db.View(ctx, func(tx Reader) error {
		for i := 0; i < MaxTransactionOps+1; i++ {
			if _, _, err := tx.Get([]byte("missing")); err != nil {
				return err
			}
		}
		return nil
	}); !errors.Is(err, ErrInvalidTx) {
		t.Fatalf("View(operation count exceeded) error = %v, want ErrInvalidTx", err)
	}
}

func TestTransactionsRejectNilContextAndClosedDatabase(t *testing.T) {
	db, _ := openTestDB(t)
	if err := db.View(nil, func(Reader) error { return nil }); !errors.Is(err, ErrInvalid) {
		t.Fatalf("View(nil context) error = %v, want ErrInvalid", err)
	}
	if err := db.Update(context.Background(), nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Update(nil callback) error = %v, want ErrInvalid", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.View(context.Background(), func(Reader) error { return nil }); !errors.Is(err, ErrClosed) {
		t.Fatalf("View(after close) error = %v, want ErrClosed", err)
	}
}

func TestConcurrentReadersAndSingleWriter(t *testing.T) {
	db, _ := openTestDB(t)
	ctx := context.Background()
	if err := db.Update(ctx, func(tx Writer) error { return tx.Put([]byte("read-key"), []byte("read-value")) }); err != nil {
		t.Fatal(err)
	}

	const readers = 4
	readerEntered := make(chan struct{}, readers)
	readerRelease := make(chan struct{})
	readerErrors := make(chan error, readers)
	for i := 0; i < readers; i++ {
		go func() {
			readerErrors <- db.View(ctx, func(tx Reader) error {
				if _, found, err := tx.Get([]byte("read-key")); err != nil || !found {
					return fmt.Errorf("reader Get found=%t error=%v", found, err)
				}
				readerEntered <- struct{}{}
				<-readerRelease
				return nil
			})
		}()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-readerEntered:
		case <-time.After(2 * time.Second):
			close(readerRelease)
			t.Fatal("independent read-only transactions did not overlap")
		}
	}
	close(readerRelease)
	for i := 0; i < readers; i++ {
		if err := <-readerErrors; err != nil {
			t.Fatalf("reader transaction: %v", err)
		}
	}

	var active atomic.Int32
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondEntered := make(chan struct{})
	writerErrors := make(chan error, 2)
	go func() {
		writerErrors <- db.Update(ctx, func(tx Writer) error {
			if active.Add(1) != 1 {
				return errors.New("writer callbacks overlapped")
			}
			defer active.Add(-1)
			close(firstEntered)
			<-releaseFirst
			return tx.Put([]byte("writer-one"), []byte("committed"))
		})
	}()
	<-firstEntered
	secondStarted := make(chan struct{})
	go func() {
		close(secondStarted)
		writerErrors <- db.Update(ctx, func(tx Writer) error {
			close(secondEntered)
			if active.Add(1) != 1 {
				return errors.New("writer callbacks overlapped")
			}
			defer active.Add(-1)
			return tx.Put([]byte("writer-two"), []byte("committed"))
		})
	}()
	<-secondStarted
	select {
	case <-secondEntered:
		close(releaseFirst)
		t.Fatal("second writer callback entered before first transaction ended")
	case <-time.After(30 * time.Millisecond):
	}
	close(releaseFirst)
	for i := 0; i < 2; i++ {
		if err := <-writerErrors; err != nil {
			t.Fatalf("writer transaction: %v", err)
		}
	}
	select {
	case <-secondEntered:
	default:
		t.Fatal("second writer callback never ran")
	}
}

func TestCrashRecoveryReturnsOnlyStateBeforeOrAfterCommit(t *testing.T) {
	for _, phase := range []string{"before", "after"} {
		t.Run(phase, func(t *testing.T) {
			path := databasePath(t)
			db, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}

			command := exec.Command(os.Args[0], "-test.run=^TestCrashWorker$")
			command.Env = append(os.Environ(), "BLUR_BOLT_CRASH_PATH="+path, "BLUR_BOLT_CRASH_PHASE="+phase)
			stdout, err := command.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			command.Stderr = &stderr
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			line, scanErr := bufio.NewReader(stdout).ReadString('\n')
			if scanErr != nil || strings.TrimSpace(line) != "READY" {
				_ = command.Process.Kill()
				_ = command.Wait()
				t.Fatalf("crash worker handshake = (%q, %v); stderr=%q", line, scanErr, stderr.String())
			}
			if err := command.Process.Kill(); err != nil {
				t.Fatalf("kill worker: %v", err)
			}
			_ = command.Wait()

			recovered, err := Open(path)
			if err != nil {
				t.Fatalf("Open() after crash recovery error = %v", err)
			}
			defer recovered.Close()
			wantCommitted := phase == "after"
			if err := recovered.View(context.Background(), func(tx Reader) error {
				value, found, err := tx.Get([]byte("crash-key"))
				if err != nil {
					return err
				}
				if found != wantCommitted || (found && !bytes.Equal(value, []byte("committed"))) {
					return fmt.Errorf("recovered state found=%t value=%q, want committed=%t", found, value, wantCommitted)
				}
				return nil
			}); err != nil {
				t.Fatalf("View() after crash: %v", err)
			}
		})
	}
}

func TestCrashWorker(t *testing.T) {
	path := os.Getenv("BLUR_BOLT_CRASH_PATH")
	phase := os.Getenv("BLUR_BOLT_CRASH_PHASE")
	if path == "" || (phase != "before" && phase != "after") {
		return
	}
	db, err := Open(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "worker open failed")
		os.Exit(2)
	}
	write := func(tx Writer) error { return tx.Put([]byte("crash-key"), []byte("committed")) }
	if phase == "before" {
		_ = db.Update(context.Background(), func(tx Writer) error {
			if err := write(tx); err != nil {
				fmt.Fprintln(os.Stderr, "worker write failed")
				os.Exit(2)
			}
			fmt.Fprintln(os.Stdout, "READY")
			select {}
		})
		return
	}
	if err := db.Update(context.Background(), write); err != nil {
		fmt.Fprintln(os.Stderr, "worker commit failed")
		os.Exit(2)
	}
	fmt.Fprintln(os.Stdout, "READY")
	select {}
}

func TestCorruptDatabaseIsRejectedWithoutContentfulErrors(t *testing.T) {
	path := databasePath(t)
	const sentinel = "SENTINEL_CORRUPT_DATABASE_7103"
	original := []byte(sentinel)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Open(path)
	if !errors.Is(err, ErrCorrupt) || strings.Contains(fmt.Sprint(err), sentinel) {
		t.Fatalf("Open(corrupt database) error = %v, want content-free ErrCorrupt", err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil || !bytes.Equal(got, original) {
		t.Fatalf("corrupt database after refusal = (%q, %v), want original bytes preserved", got, readErr)
	}
}

func TestEncryptedRecordsLeaveNoPlaintextInFilesOrErrors(t *testing.T) {
	const sentinel = "SENTINEL_SYNTHETIC_PRIVATE_RECORD_9107"
	var logs bytes.Buffer
	oldLogWriter := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(oldLogWriter)
	codec, err := recordcodec.NewCodec(bytes.Repeat([]byte{0x42}, recordcodec.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	defer codec.Close()
	metadata := recordcodec.RecordMetadata{StoreID: "0123456789abcdef0123456789abcdef", SchemaVersion: 1, Namespace: "observations", Key: "synthetic-9107", Revision: 1}
	envelope, err := codec.Seal(metadata, []byte(sentinel))
	if err != nil {
		t.Fatal(err)
	}
	db, path := openTestDB(t)
	if err := db.Update(context.Background(), func(tx Writer) error {
		return tx.Put([]byte("opaque-record-key"), envelope)
	}); err != nil {
		t.Fatalf("Update(encrypted record) error = %v", err)
	}
	callbackErr := db.Update(context.Background(), func(Writer) error { return errors.New(sentinel) })
	if !errors.Is(callbackErr, ErrTransaction) || strings.Contains(fmt.Sprint(callbackErr), sentinel) {
		t.Fatalf("Update(contentful callback error) = %v, want content-free ErrTransaction", callbackErr)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if got, err := os.ReadFile(path); err != nil {
		t.Fatal(err)
	} else if bytes.Contains(got, []byte(sentinel)) {
		t.Fatal("database file contains synthetic plaintext sentinel")
	}
	root := filepath.Dir(path)
	err = filepath.WalkDir(root, func(filePath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !entry.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(filePath)
		if err != nil {
			return err
		}
		if bytes.Contains(data, []byte(sentinel)) {
			return fmt.Errorf("synthetic plaintext sentinel in managed artifact")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("artifact scan: %v", err)
	}
	if bytes.Contains(logs.Bytes(), []byte(sentinel)) {
		t.Fatal("backend logs contain synthetic plaintext sentinel")
	}

}

func BenchmarkUpdateOpaqueRecord(b *testing.B) {
	directory := filepath.Join(b.TempDir(), "private")
	if err := CreatePrivateDirectory(directory); err != nil {
		b.Fatal(err)
	}
	db, err := Open(filepath.Join(directory, "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	const keyPrefix = "synthetic-key-"
	key := make([]byte, len(keyPrefix)+8)
	copy(key, keyPrefix)
	value := bytes.Repeat([]byte{0x5a}, 1024)
	b.SetBytes(int64(len(key) + len(value)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		binary.BigEndian.PutUint64(key[len(keyPrefix):], uint64(i))
		if err := db.Update(ctx, func(tx Writer) error { return tx.Put(key, value) }); err != nil {
			b.Fatal(err)
		}
	}
}
