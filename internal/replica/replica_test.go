package replica

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/deletion"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

type device struct {
	store *durable.Store
	state *StateFile
	del   *deletion.Service
	w     *Writer
	im    *Importer
}

func testKey(t *testing.T, pass string) []byte {
	t.Helper()
	key, err := pbkdf2ForTest(pass)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// pbkdf2ForTest avoids the 600k-iteration cost in most tests.
func pbkdf2ForTest(pass string) ([]byte, error) {
	k := make([]byte, KeySize)
	copy(k, pass)
	return k, nil
}

func newDevice(t *testing.T, folder string, key []byte) *device {
	t.Helper()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	state, err := OpenState(filepath.Join(t.TempDir(), "replica", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Update(func(s *State) { s.Enabled = true }); err != nil {
		t.Fatal(err)
	}
	del, err := deletion.NewService(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	resolve := func(context.Context) (Target, error) {
		return Target{Dir: folder, Key: bytes.Clone(key)}, nil
	}
	return &device{
		store: store, state: state, del: del,
		w:  &Writer{Store: store, State: state, Resolve: resolve},
		im: &Importer{Store: store, Deleter: del, State: state, Resolve: resolve},
	}
}

func observe(t *testing.T, d *device, text string, at time.Time) string {
	t.Helper()
	o, err := capture.SanitizeObservation("editor", "", text, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.store.PutObservationAt(context.Background(), o, at); err != nil {
		t.Fatal(err)
	}
	return o.ContentID()
}

func has(t *testing.T, d *device, id string) bool {
	t.Helper()
	_, found, err := d.store.GetObservation(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func TestRoundTripObservationsAndDeletionPropagate(t *testing.T) {
	ctx := context.Background()
	folder := t.TempDir()
	key := testKey(t, "correct horse")
	a, b := newDevice(t, folder, key), newDevice(t, folder, key)
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	id1 := observe(t, a, "alpha project notes", base)
	id2 := observe(t, a, "beta project notes", base.Add(time.Minute))
	if _, err := a.w.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	res, err := b.im.Run(ctx)
	if err != nil || res.Observations != 2 || res.Segments != 1 {
		t.Fatalf("import = %+v, %v", res, err)
	}
	if !has(t, b, id1) || !has(t, b, id2) {
		t.Fatal("observations missing after import")
	}
	page, err := b.store.SearchExact(ctx, search.ExactQuery{Terms: []string{"alpha"}, TopK: 5, MaxRows: 64, MaxBytes: 1 << 20})
	if err != nil || len(page.Hits) == 0 {
		t.Fatalf("imported observation not searchable: %+v, %v", page, err)
	}
	if got := b.state.Get().OtherDevices; got != 1 {
		t.Fatalf("other devices = %d", got)
	}

	// A deletes one source; the tombstone reaches B.
	if _, err := a.del.DeleteSource(ctx, id1); err != nil {
		t.Fatal(err)
	}
	if _, err := a.w.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if res, err = b.im.Run(ctx); err != nil || res.Tombstones != 1 {
		t.Fatalf("tombstone import = %+v, %v", res, err)
	}
	if has(t, b, id1) || !has(t, b, id2) {
		t.Fatal("deletion did not propagate precisely")
	}
	// B relays the tombstone once (so devices that only see B converge), but
	// the exchange must settle: A importing it changes nothing and exports
	// nothing new.
	if _, err := b.w.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	seqA := a.state.Get().LastSeq
	if _, err := a.im.Run(ctx); err != nil || has(t, a, id1) {
		t.Fatalf("resurrection: %v", err)
	}
	if _, err := a.w.Flush(ctx); err != nil || a.state.Get().LastSeq != seqA {
		t.Fatalf("tombstone echoed: %v", err)
	}
	if _, err := b.w.Flush(ctx); err != nil || b.state.Get().LastSeq != 1 {
		t.Fatalf("relay repeated: %v", err)
	}

	// DeleteAll propagates as one "before" tombstone.
	if _, err := a.del.DeleteAll(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := a.w.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := b.im.Run(ctx); err != nil || has(t, b, id2) {
		t.Fatalf("delete all did not propagate: %v", err)
	}
}

func TestReplayIsIdempotent(t *testing.T) {
	ctx := context.Background()
	folder := t.TempDir()
	key := testKey(t, "pw")
	a, b := newDevice(t, folder, key), newDevice(t, folder, key)
	observe(t, a, "one", time.Now().UTC().Add(-time.Hour))
	if _, err := a.w.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if res, err := b.im.Run(ctx); err != nil || res.Observations != 1 {
		t.Fatalf("first = %+v, %v", res, err)
	}
	if res, err := b.im.Run(ctx); err != nil || res.Segments != 0 || res.Observations != 0 {
		t.Fatalf("second = %+v, %v", res, err)
	}
	// Even with the watermark lost, content-ID dedupe keeps it a no-op.
	if err := b.state.Update(func(s *State) { s.Imported = nil }); err != nil {
		t.Fatal(err)
	}
	if res, err := b.im.Run(ctx); err != nil || res.Observations != 0 || res.Skipped != 1 {
		t.Fatalf("replay = %+v, %v", res, err)
	}
	// Nothing new: no empty segment.
	if _, err := a.w.Flush(ctx); err != nil || a.state.Get().LastSeq != 1 {
		t.Fatalf("empty flush wrote a segment: %v", err)
	}
}

func TestTamperedAndWrongKeyRejected(t *testing.T) {
	ctx := context.Background()
	folder := t.TempDir()
	a := newDevice(t, folder, testKey(t, "right"))
	wrong := newDevice(t, folder, testKey(t, "wrong"))
	observe(t, a, "secret thing", time.Now().UTC().Add(-time.Hour))
	if _, err := a.w.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if res, err := wrong.im.Run(ctx); !errors.Is(err, ErrAuth) || res.Observations != 0 {
		t.Fatalf("wrong key = %+v, %v", res, err)
	}
	if wrong.state.Get().Imported[a.state.Get().DeviceID] != 0 {
		t.Fatal("watermark advanced past a rejected segment")
	}

	good := newDevice(t, folder, testKey(t, "right"))
	path := filepath.Join(folder, a.state.Get().DeviceID, segmentName(1))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, offset := range []int{3, 20, 30, len(data) / 2, len(data) - 1} {
		bad := bytes.Clone(data)
		bad[offset] ^= 0x01
		if err := os.WriteFile(path, bad, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := good.im.Run(ctx); err == nil {
			t.Fatalf("tampered byte %d accepted", offset)
		}
	}
	// A copied segment under another sequence number fails header binding.
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, a.state.Get().DeviceID, segmentName(2)), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if res, err := good.im.Run(ctx); err == nil || res.Segments != 1 || res.Observations != 1 {
		t.Fatalf("renamed segment = %+v, %v", res, err)
	}
}

func TestPlaceholdersAndOwnFolderSkipped(t *testing.T) {
	ctx := context.Background()
	folder := t.TempDir()
	key := testKey(t, "pw")
	a, b := newDevice(t, folder, key), newDevice(t, folder, key)
	observe(t, a, "text", time.Now().UTC().Add(-time.Hour))
	if _, err := a.w.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	devDir := filepath.Join(folder, a.state.Get().DeviceID)
	for _, name := range []string{".000000000002.seg.icloud", ".tmp-123", "notes.txt", "000000000003.seg.bak"} {
		if err := os.WriteFile(filepath.Join(devDir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(folder, ".hidden"), 0o700); err != nil {
		t.Fatal(err)
	}
	if res, err := b.im.Run(ctx); err != nil || res.Segments != 1 || res.Rejected != 0 {
		t.Fatalf("import = %+v, %v", res, err)
	}
	// A device never imports from its own folder.
	if res, err := a.im.Run(ctx); err != nil || res.Segments != 0 {
		t.Fatalf("own folder import = %+v, %v", res, err)
	}
}

func TestSegmentPaddingClasses(t *testing.T) {
	key := testKey(t, "pw")
	var device [16]byte
	for _, size := range []int{1, 1000, PadClass - 100, PadClass, 3 * PadClass / 2} {
		obs := bytes.Repeat([]byte{7}, size)
		sealed, err := SealSegment(key, device, 1, []Record{{Kind: KindObservation, At: time.Now(), Observation: obs}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if (len(sealed)-headerSize-16)%PadClass != 0 {
			t.Fatalf("size %d sealed to %d, not a padding class", size, len(sealed))
		}
	}
}

func TestTombstoneRecordsRoundTrip(t *testing.T) {
	key := testKey(t, "pw")
	var device [16]byte
	id := "ab" + string(bytes.Repeat([]byte("0"), 62))
	at := time.Now().UTC().Truncate(time.Nanosecond)
	sealed, err := SealSegment(key, device, 9, []Record{{Kind: KindTombstone, At: at, SourceID: id}, {Kind: KindTombstone, At: at, SourceID: "*"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	h, records, err := OpenSegment(key, sealed)
	if err != nil || h.Seq != 9 || len(records) != 2 || records[0].SourceID != id || records[1].SourceID != "*" || !records[0].At.Equal(at) {
		t.Fatalf("records = %+v, %v", records, err)
	}
}

func TestFlushSkippedWhileMemoryChanging(t *testing.T) {
	ctx := context.Background()
	folder := t.TempDir()
	a := newDevice(t, folder, testKey(t, "pw"))
	observe(t, a, "text", time.Now().UTC().Add(-time.Hour))
	a.w.Fence = func() (func() bool, bool) { return func() bool { return false }, false }
	if _, err := a.w.Flush(ctx); !errors.Is(err, ErrMemoryChanging) {
		t.Fatalf("flush while changing = %v", err)
	}
	if entries, _ := os.ReadDir(folder); len(entries) != 0 {
		t.Fatal("wrote while memory was changing")
	}
	// The fence closing between scan and write also aborts, leaving the watermark.
	a.w.Fence = func() (func() bool, bool) { return func() bool { return false }, true }
	if _, err := a.w.Flush(ctx); !errors.Is(err, ErrMemoryChanging) || a.state.Get().LastSeq != 0 {
		t.Fatalf("late fence = %v", err)
	}
}

func TestRootSaltAndKeyDerivation(t *testing.T) {
	dir := t.TempDir()
	root, err := LoadOrCreateRoot(dir)
	if err != nil || len(root.Salt) != SaltSize {
		t.Fatalf("root = %+v, %v", root, err)
	}
	again, err := LoadOrCreateRoot(dir)
	if err != nil || !bytes.Equal(again.Salt, root.Salt) {
		t.Fatal("salt not stable")
	}
	k1, err := DeriveKey("passphrase", root.Salt)
	if err != nil || len(k1) != KeySize {
		t.Fatal(err)
	}
	k2, _ := DeriveKey("passphrase", root.Salt)
	k3, _ := DeriveKey("passphrasf", root.Salt)
	if !bytes.Equal(k1, k2) || bytes.Equal(k1, k3) {
		t.Fatal("derivation is not deterministic or not passphrase-sensitive")
	}
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, ".blur-replica.json.icloud"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateRoot(other); !errors.Is(err, ErrFolderNotReady) {
		t.Fatalf("placeholder root = %v", err)
	}
}

func TestImportedContentIsNotReExported(t *testing.T) {
	ctx := context.Background()
	folder := t.TempDir()
	key := testKey(t, "correct horse")
	a, b := newDevice(t, folder, key), newDevice(t, folder, key)
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	observe(t, a, "from device a", base)
	if _, err := a.w.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if res, err := b.im.Run(ctx); err != nil || res.Observations != 1 {
		t.Fatalf("import = %+v, %v", res, err)
	}
	// B flushes: only imported content exists, so no segment is written.
	if _, err := b.w.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if b.state.Get().LastSeq != 0 {
		t.Fatal("imported observation was re-exported")
	}
	if b.state.Get().ObsWatermark == 0 {
		t.Fatal("watermark did not advance past imported content")
	}
	// B's own capture is still exported, alone.
	observe(t, b, "from device b", base.Add(time.Minute))
	if _, err := b.w.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if got := b.state.Get().LastSeq; got != 1 {
		t.Fatalf("own capture not exported, seq = %d", got)
	}
	if res, err := a.im.Run(ctx); err != nil || res.Observations != 1 {
		t.Fatalf("a import = %+v, %v", res, err)
	}
	// Delete-all forgets origin marks.
	if _, err := b.del.DeleteAll(ctx); err != nil {
		t.Fatal(err)
	}
	set, err := b.store.ImportedSet(ctx, []string{fmt.Sprintf("%064x", 1)})
	if err != nil || len(set) != 0 {
		t.Fatalf("set = %v, %v", set, err)
	}
}
