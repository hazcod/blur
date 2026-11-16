package semantic

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/storage"
)

type periodicRepairFixture struct {
	Repository
	Embedder
	mu         sync.Mutex
	checkpoint Checkpoint
	legacy     atomic.Int32
}

func (f *periodicRepairFixture) EncryptedAtRest() bool { return true }
func (f *periodicRepairFixture) Identity(context.Context) (Model, error) {
	return Model{Name: "periodic:fixture", Digest: strings.Repeat("a", 64), Dimensions: 2, Format: FormatPlain}, nil
}
func (f *periodicRepairFixture) GetSemanticCheckpoint(context.Context, string) (Checkpoint, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.checkpoint, true, nil
}
func (f *periodicRepairFixture) PutSemanticCheckpoint(_ context.Context, c Checkpoint) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checkpoint = c
	return nil
}
func (f *periodicRepairFixture) ListSemanticDocuments(context.Context, PageRequest) (Page, error) {
	return Page{}, nil
}
func (f *periodicRepairFixture) ListObservationPage(context.Context, storage.ObservationPageRequest) (storage.ObservationPage, error) {
	f.legacy.Add(1)
	return storage.ObservationPage{}, nil
}
func (f *periodicRepairFixture) ListCaptureEvidencePage(context.Context, storage.CaptureEvidencePageRequest) (storage.CaptureEvidencePage, error) {
	return storage.CaptureEvidencePage{}, nil
}

func TestStartupAndPeriodicRepairCannotBeStarvedByCaptureWakes(t *testing.T) {
	f := &periodicRepairFixture{}
	identity, _ := f.Identity(context.Background())
	f.checkpoint = Checkpoint{ModelKey: identity.Key(), Complete: true, CaptureComplete: true}
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
	for f.legacy.Load() < 2 {
		select {
		case <-wake.C:
			s.WakeCaptures()
		case <-deadline.C:
			t.Fatal("startup/periodic legacy repair lost or postponed by capture wakes", f.legacy.Load())
		}
	}
}
