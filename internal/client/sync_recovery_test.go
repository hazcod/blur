package client

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/deletion"
	"github.com/hazcod/blur/internal/replica"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

func syncRecoveryFixture(t *testing.T) (*replicaService, *Client, *durable.Store, string, []byte) {
	t.Helper()
	directory := t.TempDir()
	store, err := durable.OpenDurable(filepath.Join(directory, "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	c := NewClient(store)
	t.Cleanup(c.Close)
	del, err := deletion.NewService(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.WithDeletion(del)
	secrets := &bootstrapSecrets{values: map[string][]byte{}}
	s, err := newReplicaService(directory, store, del, c, secrets)
	if err != nil {
		t.Fatal(err)
	}
	c.replica = s
	key := bytes.Repeat([]byte{0x63}, replica.KeySize)
	return s, c, store, directory, key
}
func configureSyncFixture(t *testing.T, s *replicaService, folder string, key []byte) {
	t.Helper()
	if err := s.setFolder(folder, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.secrets.Put(context.Background(), replicaKeySecret, key); err != nil {
		t.Fatal(err)
	}
	if err := s.state.Update(func(st *replica.State) { st.Enabled = true; st.KeySet = true }); err != nil {
		t.Fatal(err)
	}
}
func putSyncFixture(t *testing.T, store *durable.Store, text string) string {
	t.Helper()
	o, err := capture.SanitizeObservation("com.apple.terminal", "", text, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(context.Background(), o, time.Now().UTC().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	return o.ContentID()
}

func TestSyncFolderReturnPreservesOldSegmentsAndStartsContiguousWriter(t *testing.T) {
	ctx := context.Background()
	s, _, store, _, key := syncRecoveryFixture(t)
	a, b := t.TempDir(), t.TempDir()
	configureSyncFixture(t, s, a, key)
	putSyncFixture(t, store, "initial source before folder switch")
	if err := s.runner.FlushNow(ctx); err != nil {
		t.Fatal(err)
	}
	firstID := s.state.Get().DeviceID
	path := filepath.Join(a, firstID, "000000000001.seg")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	peer, _, peerStore, _, _ := syncRecoveryFixture(t)
	configureSyncFixture(t, peer, a, key)
	if _, err := peer.runner.ImportNow(ctx); err != nil {
		t.Fatal(err)
	}
	configureSyncFixture(t, s, b, key)
	middleID := s.state.Get().DeviceID
	if err := s.runner.FlushNow(ctx); err != nil {
		t.Fatal(err)
	}
	configureSyncFixture(t, s, a, key)
	lastID := s.state.Get().DeviceID
	second := putSyncFixture(t, store, "new local source after returning to folder")
	if err := s.runner.FlushNow(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("folder return replaced an already consumed identity", err)
	}
	if firstID == middleID || middleID == lastID || firstID == lastID {
		t.Fatal("changed folder reused a writer generation")
	}
	if _, err := peer.runner.ImportNow(ctx); err != nil {
		t.Fatal(err)
	}
	if _, found, err := peerStore.GetObservation(ctx, second); err != nil || !found {
		t.Fatal("peer skipped new source after folder return", err)
	}
	if _, err := os.Stat(filepath.Join(a, lastID, "000000000001.seg")); err != nil {
		t.Fatal("new generation did not begin contiguously", err)
	}
}

func TestNativeDeletionClearsSealedPendingEvenWhenSyncDisabled(t *testing.T) {
	ctx := context.Background()
	s, c, store, directory, key := syncRecoveryFixture(t)
	folder := t.TempDir()
	configureSyncFixture(t, s, folder, key)
	source := putSyncFixture(t, store, "private source in pending sync work")
	checks := 0
	s.runner.Writer.Fence = func() (func() bool, bool) { return func() bool { checks++; return checks == 1 }, true }
	if _, err := s.runner.Writer.Flush(ctx); !errors.Is(err, replica.ErrMemoryChanging) {
		t.Fatal("fixture did not stage pending work", err)
	}
	paths, err := filepath.Glob(filepath.Join(directory, "replica", "outbox-*.sealed"))
	if err != nil || len(paths) != 1 {
		t.Fatal("fixture did not persist private outbox", paths, err)
	}
	if err := s.setEnabled(false); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DeleteSource(source); err != nil {
		t.Fatal(err)
	}
	paths, err = filepath.Glob(filepath.Join(directory, "replica", "outbox-*.sealed"))
	if err != nil || len(paths) != 0 {
		t.Fatal("native erasure retained sealed private source bytes", paths, err)
	}
	if s.state.Get().Pending != nil || s.state.Get().OutboxInvalid {
		t.Fatal("native erasure did not finish pending cleanup")
	}
	if _, found, err := store.GetObservation(ctx, source); err != nil || found {
		t.Fatal("source erasure failed", err)
	}
}

type clientBlockedSyncExport struct {
	*durable.Store
	started chan struct{}
}

func (s *clientBlockedSyncExport) ExportReplicaObservations(ctx context.Context, generation string, after []byte, maxCount, maxBytes int) (durable.ReplicaExportPage, error) {
	close(s.started)
	<-ctx.Done()
	return durable.ReplicaExportPage{}, ctx.Err()
}
func TestTurnOffCancelsNativeInflightExport(t *testing.T) {
	ctx := context.Background()
	s, _, store, _, key := syncRecoveryFixture(t)
	folder := t.TempDir()
	configureSyncFixture(t, s, folder, key)
	putSyncFixture(t, store, "private source when Turn off succeeds")
	blocked := &clientBlockedSyncExport{Store: store, started: make(chan struct{})}
	s.runner.Writer.Store = blocked
	done := make(chan error, 1)
	go func() { _, err := s.runner.Writer.Flush(ctx); done <- err }()
	<-blocked.started
	if err := s.setEnabled(false); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Turn off did not cancel exporter")
	}
	paths, err := filepath.Glob(filepath.Join(folder, "*", "*.seg"))
	if err != nil || len(paths) != 0 {
		t.Fatal("Turn off returned before publication was excluded", paths, err)
	}
}
