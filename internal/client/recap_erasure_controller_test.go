package client

import (
	"context"
	"crypto/rand"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/chronicle"
	"github.com/hazcod/blur/internal/storage/durable"
)

var _ chronicleErasureMaintenanceStore = (*durable.Store)(nil)

type recapErasureOnlyStore struct {
	calls atomic.Int32
	done  chan struct{}
}

func (*recapErasureOnlyStore) ExactIndexPending(context.Context) (bool, error) { return false, nil }
func (*recapErasureOnlyStore) IndexPendingExact(context.Context, int) (bool, error) {
	panic("no source indexing work")
}
func (s *recapErasureOnlyStore) ResumeChronicleErasure(ctx context.Context) (bool, bool, error) {
	if err := ctx.Err(); err != nil {
		return false, true, err
	}
	n := s.calls.Add(1)
	if n == 3 {
		close(s.done)
	}
	return true, n < 3, nil
}

func TestLocalStorageMaintenanceResumesRecapErasureWithoutModels(t *testing.T) {
	s := &recapErasureOnlyStore{done: make(chan struct{})}
	var canRead atomic.Bool
	c := newExactIndexController(s, canRead.Load, nil)
	defer c.Close()
	// The same gate as source erasure must also pause recap maintenance.
	time.Sleep(20 * time.Millisecond)
	if s.calls.Load() != 0 {
		t.Fatal("recap maintenance ignored the memory-changing gate")
	}
	canRead.Store(true)
	c.Wake()
	select {
	case <-s.done:
	case <-time.After(3 * time.Second):
		t.Fatal("model-free local storage service left recap erasure pending")
	}
	if c.Failed() {
		t.Fatal("successful recap maintenance marked storage unavailable")
	}
}

func TestLocalAppReopenFinishesRecapErasureWithAIDisabled(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "Blur")
	secrets := &bootstrapSecrets{values: make(map[string][]byte)}
	app, err := openLocalAppAt(ctx, directory, secrets, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close() }()
	if err := app.Client.ConfigureLocalAI(ctx, LocalAISettings{}); err != nil {
		t.Fatal(err)
	}
	app.exactIndex.Close()
	at := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Hour)
	o, err := capture.SanitizeObservation("com.example.mail", "", "Synthetic ownership decision remains original evidence.", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.store.PutObservationAt(ctx, o, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	hour := chronicle.Summary{HourID: "hour:" + at.Format("20060102T15Z"), Start: at, End: at.Add(time.Hour), Text: "Reviewed the ownership decision.", SourceIDs: []string{o.ContentID()}}
	if err := app.store.PutChronicle(ctx, hour); err != nil {
		t.Fatal(err)
	}
	// Reproduce the encrypted intent left by a process stopping immediately
	// after the commit. The durable regression exercises the real begin path.
	if err := app.store.Update(ctx, func(tx *durable.Tx) error {
		return tx.PutIndexRecord("chronicle-rollup-meta", "erasing", []byte(`{"Version":1,"Hours":true,"Phase":"hours"}`))
	}); err != nil {
		t.Fatal(err)
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	app, err = openLocalAppAt(ctx, directory, secrets, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if app.Client.LocalAISettings().Enabled || app.localAI.chronicleWorker != nil {
		t.Fatal("fixture unexpectedly has an inference worker")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		status, err := app.store.ChronicleRollupWorkStatus(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !status.ErasurePending {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("bootstrap stranded recap erasure without an inference worker")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, found, err := app.store.GetChronicle(ctx, hour.HourID); err != nil || found {
		t.Fatalf("hour survived completed maintenance: %v %v", found, err)
	}
	if _, found, err := app.store.GetObservation(ctx, o.ContentID()); err != nil || !found {
		t.Fatalf("recap maintenance changed the original: %v %v", found, err)
	}
}
