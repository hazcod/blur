package replica

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/storage/durable"
)

func drainExport(t *testing.T, d *device) {
	t.Helper()
	for i := 0; i < 20; i++ {
		more, err := d.w.Flush(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			return
		}
	}
	t.Fatal("export did not make bounded progress")
}

func TestExportAfterFutureImportKeepsLaterLocalCapture(t *testing.T) {
	ctx, folder, key := context.Background(), t.TempDir(), testKey(t, "future receipt")
	a, b, c := newDevice(t, folder, key), newDevice(t, folder, key), newDevice(t, folder, key)
	base := time.Now().UTC().Truncate(time.Second)
	observe(t, a, "future source from a fast peer", base.Add(30*time.Minute))
	drainExport(t, a)
	if _, err := b.im.Run(ctx); err != nil {
		t.Fatal(err)
	}
	drainExport(t, b)
	id := observe(t, b, "later local source after importing", base.Add(time.Minute))
	drainExport(t, b)
	if _, err := c.im.Run(ctx); err != nil || !has(t, c, id) {
		t.Fatalf("future import hid a later local capture: imported=%v err=%v", has(t, c, id), err)
	}
}

func TestExportKeepsEqualTimeBackdatedAndUpdatedSources(t *testing.T) {
	ctx, folder, key := context.Background(), t.TempDir(), testKey(t, "unordered receipt")
	a, b := newDevice(t, folder, key), newDevice(t, folder, key)
	base := time.Now().UTC().Add(-time.Hour)
	observe(t, a, "first source", base)
	drainExport(t, a)
	for _, at := range []time.Time{base, base.Add(-time.Minute)} {
		id := observe(t, a, "source at "+at.Format(time.RFC3339Nano), at)
		drainExport(t, a)
		if _, err := b.im.Run(ctx); err != nil || !has(t, b, id) {
			t.Fatalf("capture order became export authority: time=%v imported=%v err=%v", at, has(t, b, id), err)
		}
	}
}

func TestExportStateFailureNeverReplacesConsumedSequence(t *testing.T) {
	ctx, folder, key := context.Background(), t.TempDir(), testKey(t, "pending receipt")
	a, b := newDevice(t, folder, key), newDevice(t, folder, key)
	base := time.Now().UTC().Add(-time.Hour)
	first := observe(t, a, "first published source", base)
	good := a.state.path
	bad := filepath.Join(t.TempDir(), "not-directory")
	if err := os.WriteFile(bad, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.state.path = filepath.Join(bad, "state.json")
	if _, err := a.w.Flush(ctx); err == nil {
		t.Fatal("state persistence failure was ignored")
	}
	path := filepath.Join(folder, a.state.Get().DeviceID, segmentName(1))
	published, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if _, err := b.im.Run(ctx); err != nil {
		t.Fatal(err)
	}
	a.state.path = good
	second := observe(t, a, "source recorded before retry", base.Add(time.Minute))
	drainExport(t, a)
	if len(published) > 0 {
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(published, after) {
			t.Fatal("an exposed segment identity was replaced", err)
		}
	}
	if _, err := b.im.Run(ctx); err != nil || !has(t, b, first) || !has(t, b, second) {
		t.Fatalf("retry lost captured text: first=%v second=%v err=%v", has(t, b, first), has(t, b, second), err)
	}
}

type failPublishedState struct {
	*durable.Store
	state   *StateFile
	badPath string
	failed  bool
}

func (s *failPublishedState) AcknowledgeReplicaExport(ctx context.Context, generation string, items []durable.ExportedObservation, importedOnly bool) error {
	if err := s.Store.AcknowledgeReplicaExport(ctx, generation, items, importedOnly); err != nil {
		return err
	}
	if !importedOnly && !s.failed {
		s.failed = true
		s.state.path = s.badPath
	}
	return nil
}

func TestPublishedOutboxReopensAndReplaysExactBytesAfterStateFailure(t *testing.T) {
	ctx, folder, key := context.Background(), t.TempDir(), testKey(t, "published outbox")
	a, b := newDevice(t, folder, key), newDevice(t, folder, key)
	base := time.Now().UTC().Add(-time.Hour)
	first := observe(t, a, "original captured text before state failure", base)
	good := a.state.path
	bad := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(bad, []byte("not directory"), 0600); err != nil {
		t.Fatal(err)
	}
	a.w.Store = &failPublishedState{Store: a.store, state: a.state, badPath: filepath.Join(bad, "state.json")}
	if _, err := a.w.Flush(ctx); err == nil {
		t.Fatal("post-publication state failure was ignored")
	}
	a.state.path = good
	snapshot := a.state.Get()
	if snapshot.Pending == nil || snapshot.LastSeq != 0 {
		t.Fatal("exact pending identity was not retained", snapshot)
	}
	path := filepath.Join(folder, snapshot.DeviceID, segmentName(1))
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.im.Run(ctx); err != nil || !has(t, b, first) {
		t.Fatal("peer did not consume original segment", err)
	}
	local, err := os.ReadFile(a.state.outboxPath(snapshot.DeviceID, snapshot.Pending.Hash))
	if err != nil || bytes.Contains(local, []byte("original captured text")) {
		t.Fatal("private outbox was missing or plaintext", err)
	}
	second := observe(t, a, "captured after public segment was consumed", base.Add(time.Minute))
	reopened, err := OpenState(good)
	if err != nil {
		t.Fatal(err)
	}
	a.state = reopened
	a.w.State = reopened
	a.w.Store = a.store
	drainExport(t, a)
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("reopen replaced consumed bytes", err)
	}
	if _, err := b.im.Run(ctx); err != nil || !has(t, b, second) {
		t.Fatal("recovered sequence lost later text", err)
	}
}

func TestUnpublishedPendingDeletedBeforeRestartNeverLeavesDevice(t *testing.T) {
	ctx, folder, key := context.Background(), t.TempDir(), testKey(t, "deleted outbox")
	a, b := newDevice(t, folder, key), newDevice(t, folder, key)
	base := time.Now().UTC().Add(-time.Hour)
	id := observe(t, a, "private source deleted before first publication", base)
	checks := 0
	a.w.Fence = func() (func() bool, bool) { return func() bool { checks++; return checks == 1 }, true }
	if _, err := a.w.Flush(ctx); !errors.Is(err, ErrMemoryChanging) {
		t.Fatal("fixture did not stop after staging", err)
	}
	if a.state.Get().Pending == nil {
		t.Fatal("fixture did not retain sealed outbox")
	}
	if _, err := a.del.DeleteSource(ctx, id); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenState(a.state.path)
	if err != nil {
		t.Fatal(err)
	}
	a.state = reopened
	a.w.State = reopened
	a.w.Fence = nil
	drainExport(t, a)
	for _, file := range listSegments(filepath.Join(folder, reopened.Get().DeviceID)) {
		data, err := os.ReadFile(file.path)
		if err != nil {
			t.Fatal(err)
		}
		_, records, err := OpenSegment(key, data)
		if err != nil {
			t.Fatal(err)
		}
		for _, record := range records {
			if record.Kind == KindObservation {
				t.Fatal("deleted pending source was newly published")
			}
		}
	}
	if _, err := b.im.Run(ctx); err != nil || has(t, b, id) {
		t.Fatal("deleted pending source resurfaced on peer", err)
	}
}

type blockedReplicaExport struct {
	*durable.Store
	started chan struct{}
	release chan struct{}
}

func (s *blockedReplicaExport) ExportReplicaObservations(ctx context.Context, generation string, after []byte, maxCount, maxBytes int) (durable.ReplicaExportPage, error) {
	close(s.started)
	select {
	case <-ctx.Done():
		return durable.ReplicaExportPage{}, ctx.Err()
	case <-s.release:
	}
	return s.Store.ExportReplicaObservations(ctx, generation, after, maxCount, maxBytes)
}
func TestDisableCancelsInflightExportBeforePublication(t *testing.T) {
	ctx, folder, key := context.Background(), t.TempDir(), testKey(t, "disabled outbox")
	a := newDevice(t, folder, key)
	observe(t, a, "private active export", time.Now().UTC())
	blocked := &blockedReplicaExport{Store: a.store, started: make(chan struct{}), release: make(chan struct{})}
	a.w.Store = blocked
	done := make(chan error, 1)
	go func() { _, err := a.w.Flush(ctx); done <- err }()
	<-blocked.started
	if err := a.state.Update(func(s *State) { s.Enabled = false }); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) && !errors.Is(err, ErrDisabled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("disable did not cancel active exporter")
	}
	if _, err := os.Stat(filepath.Join(folder, a.state.Get().DeviceID, segmentName(1))); !os.IsNotExist(err) {
		t.Fatal("disabled export published a segment", err)
	}
}

func TestPrivacyInvalidationPrunesPendingAndDoesNotDeadlockImporter(t *testing.T) {
	ctx, folder, key := context.Background(), t.TempDir(), testKey(t, "pending invalidation")
	a, b := newDevice(t, folder, key), newDevice(t, folder, key)
	private := observe(t, b, "private unexported pending source", time.Now().UTC().Add(-time.Hour))
	checks := 0
	b.w.Fence = func() (func() bool, bool) { return func() bool { checks++; return checks == 1 }, true }
	if _, err := b.w.Flush(ctx); !errors.Is(err, ErrMemoryChanging) {
		t.Fatal(err)
	}
	st := b.state.Get()
	path := b.state.outboxPath(st.DeviceID, st.Pending.Hash)
	b.im.Changing = func() {
		if err := b.state.InvalidateOutbox(); err != nil {
			t.Error(err)
		}
	}
	observe(t, a, "new peer source", time.Now().UTC())
	drainExport(t, a)
	// Changing can retire the old writer identity. The current import may then
	// retry its receipt, but must never hang on StateFile or runner locks.
	_, err := b.im.Run(ctx)
	if err != nil && !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("private pending bytes retained after invalidation", err)
	}
	if b.state.Get().Pending != nil || b.state.Get().DeviceID == st.DeviceID {
		t.Fatal("uncertain sequence identity was reused")
	}
	if !has(t, b, private) {
		t.Fatal("outbox invalidation erased source memory")
	}
	if _, err := b.im.Run(ctx); err != nil {
		t.Fatal("import did not recover after pending invalidation", err)
	}
}

func TestPrivacyInvalidationStateFailureErasesBytesAndRetiresIdentityOnRestart(t *testing.T) {
	ctx, folder, key := context.Background(), t.TempDir(), testKey(t, "failed invalidation")
	a, b := newDevice(t, folder, key), newDevice(t, folder, key)
	id := observe(t, a, "private pending text removed before publication", time.Now().UTC().Add(-time.Hour))
	checks := 0
	a.w.Fence = func() (func() bool, bool) { return func() bool { checks++; return checks == 1 }, true }
	if _, err := a.w.Flush(ctx); !errors.Is(err, ErrMemoryChanging) {
		t.Fatal("fixture did not stop after durable staging", err)
	}
	old := a.state.Get()
	oldJSON, err := os.ReadFile(a.state.path)
	if err != nil || old.Pending == nil {
		t.Fatal("fixture has no durable pending state", err)
	}
	privatePath := a.state.outboxPath(old.DeviceID, old.Pending.Hash)
	// Keep the state parent writable so private-byte erasure succeeds, while
	// replacing only state.json with a directory makes every state rename fail.
	if err := os.Remove(a.state.path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(a.state.path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := a.state.InvalidateOutbox(); err == nil {
		t.Fatal("state persistence failure was ignored")
	}
	if !a.state.Get().OutboxInvalid {
		t.Fatal("failed invalidation did not block publication")
	}
	if _, err := os.Stat(privatePath); !os.IsNotExist(err) {
		t.Fatal("private pending bytes survived failed state update", err)
	}
	if _, err := a.del.DeleteSource(ctx, id); err != nil {
		t.Fatal(err)
	}
	// A crash can leave the old on-disk marker pointing at erased bytes. The
	// restarted writer must abandon the uncertain identity, never reuse seq 1.
	if err := os.Remove(a.state.path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.state.path, oldJSON, 0600); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenState(a.state.path)
	if err != nil {
		t.Fatal(err)
	}
	a.state, a.w.State, a.w.Fence = reopened, reopened, nil
	drainExport(t, a)
	if reopened.Get().DeviceID == old.DeviceID {
		t.Fatal("restart reused an uncertain sequence identity")
	}
	if _, err := os.Stat(filepath.Join(folder, old.DeviceID, segmentName(1))); !os.IsNotExist(err) {
		t.Fatal("restart published erased pending bytes", err)
	}
	if _, err := b.im.Run(ctx); err != nil || has(t, b, id) {
		t.Fatal("erased pending text resurfaced on a peer", err)
	}
}

func TestPrivacyInvalidationPrunesUnreferencedSealedOutbox(t *testing.T) {
	ctx, folder, key := context.Background(), t.TempDir(), testKey(t, "orphan invalidation")
	a := newDevice(t, folder, key)
	observe(t, a, "orphan private text after staging state failure", time.Now().UTC())
	if err := os.Remove(a.state.path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(a.state.path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := a.w.Flush(ctx); err == nil {
		t.Fatal("staging state failure was ignored")
	}
	files, err := filepath.Glob(filepath.Join(filepath.Dir(a.state.path), "outbox-*.sealed"))
	if err != nil || len(files) != 1 || a.state.Get().Pending != nil {
		t.Fatal("fixture did not leave a sealed unreferenced outbox", files, err)
	}
	if err := a.state.InvalidateOutbox(); err == nil {
		t.Fatal("invalidation state failure was ignored")
	}
	for _, path := range files {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("unreferenced private bytes escaped cleanup", err)
		}
	}
	if !a.state.Get().OutboxInvalid {
		t.Fatal("orphan cleanup failure did not block publication")
	}
}

func TestPrivacyInvalidationPrunesCaptureSidecarOutboxAndRetiresSequence(t *testing.T) {
	state, err := OpenState(filepath.Join(t.TempDir(), "replica", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	before := state.Get()
	hash := strings.Repeat("a", 64)
	outbox := state.captureOutboxPath(before.DeviceID, hash)
	if err := os.MkdirAll(filepath.Dir(outbox), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outbox, []byte("sealed capture payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := state.Update(func(s *State) { s.CapturePending = &PendingState{Sequence: 1, Hash: hash} }); err != nil {
		t.Fatal(err)
	}
	if err := state.InvalidateOutbox(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(outbox); !os.IsNotExist(err) {
		t.Fatalf("capture outbox survived invalidation: %v", err)
	}
	after := state.Get()
	if after.CapturePending != nil || after.CaptureLastSeq != 0 || after.DeviceID == before.DeviceID || after.OutboxInvalid {
		t.Fatalf("capture pending sequence was not safely retired: %+v", after)
	}
}
