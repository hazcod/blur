package enrich

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/storage"
)

type periodicRepairFixture struct {
	Repository
	Sources
	Generator
	mu         sync.Mutex
	checkpoint Checkpoint
	legacy     atomic.Int32
}

func (f *periodicRepairFixture) EncryptedAtRest() bool { return true }
func (f *periodicRepairFixture) Identity(context.Context) (string, error) {
	return "periodic-repair-fixture", nil
}
func (f *periodicRepairFixture) GetEnrichmentCheckpoint(context.Context) (Checkpoint, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.checkpoint, true, nil
}
func (f *periodicRepairFixture) PutEnrichmentCheckpoint(_ context.Context, c Checkpoint) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checkpoint = c
	return nil
}
func (f *periodicRepairFixture) ListObservationPage(context.Context, storage.ObservationPageRequest) (storage.ObservationPage, error) {
	f.legacy.Add(1)
	return storage.ObservationPage{}, nil
}
func (f *periodicRepairFixture) ListCaptureEvidencePage(context.Context, storage.CaptureEvidencePageRequest) (storage.CaptureEvidencePage, error) {
	return storage.CaptureEvidencePage{}, nil
}

func TestPeriodicRepairCannotBeStarvedByCaptureWakes(t *testing.T) {
	f := &periodicRepairFixture{checkpoint: Checkpoint{ModelKey: ModelKey("periodic-repair-fixture"), Complete: true, CaptureComplete: true}}
	s, err := NewService(f, f, f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.repairInterval = 40 * time.Millisecond
	s.Start()
	wake := time.NewTicker(3 * time.Millisecond)
	defer wake.Stop()
	deadline := time.NewTimer(500 * time.Millisecond)
	defer deadline.Stop()
	for f.legacy.Load() == 0 {
		select {
		case <-wake.C:
			s.WakeCaptures()
		case <-deadline.C:
			t.Fatal("capture wakes indefinitely postponed periodic legacy repair")
		}
	}
}
