package deletion

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/durable"
)

type failingDeleteStore struct {
	storage.EncryptedStore
	err error
}

type noTimestampPageStore struct{ storage.EncryptedStore }

func (s noTimestampPageStore) ListObservationPage(ctx context.Context, request storage.ObservationPageRequest) (storage.ObservationPage, error) {
	page, err := s.EncryptedStore.ListObservationPage(ctx, request)
	page.CapturedAt = nil
	return page, err
}

func (s failingDeleteStore) DeleteObservationRecord(context.Context, string) error { return s.err }

func TestDeleteSourceClearsLiveAndDurableActivity(t *testing.T) {
	ctx := context.Background()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "blur.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	observation, err := capture.SanitizeObservation("editor", "", "private project context", 128)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservation(ctx, observation); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	engine := activity.New(activity.Config{MaxGap: time.Hour, RingSize: 8})
	_, _ = engine.Apply(activity.Event{Kind: activity.Seen, Monotonic: 0, Wall: start, SourceID: observation.ContentID(), AppID: "editor"})
	segments, err := engine.Apply(activity.Event{Kind: activity.Seen, Monotonic: int64(time.Minute), Wall: start.Add(time.Minute), SourceID: observation.ContentID(), AppID: "editor"})
	if err != nil || len(segments) != 1 {
		t.Fatalf("create source segment = %d, %v", len(segments), err)
	}
	if err := store.PutActivitySegment(ctx, segments[0]); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(store, engine)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.DeleteSource(ctx, observation.ContentID()); err != nil {
		t.Fatal(err)
	}
	if got := engine.Segments(); len(got) != 0 {
		t.Fatalf("live activity ring after deletion = %#v", got)
	}
	if got, err := store.GetActivitySegments(ctx, []string{segments[0].ID}); err != nil || len(got) != 0 {
		t.Fatalf("durable activity after deletion = %#v, %v", got, err)
	}
	if _, found, err := store.GetObservation(ctx, observation.ContentID()); err != nil || found {
		t.Fatalf("source after deletion found=%t err=%v", found, err)
	}
}

func TestDeleteAllSourcesRemovesMoreThanOneBoundedPage(t *testing.T) {
	ctx := context.Background()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "blur.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const sourceCount = storage.MaxObservationPageRows + 3
	sources := make([]capture.SanitizedObservation, 0, sourceCount)
	for i := range sourceCount {
		observation, err := capture.SanitizeObservation("editor", "", fmt.Sprintf("private source %03d", i), 128)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.PutObservation(ctx, observation); err != nil {
			t.Fatal(err)
		}
		sources = append(sources, observation)
	}
	service, err := NewService(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	removed, err := service.DeleteAll(ctx)
	if err != nil || removed != sourceCount {
		t.Fatalf("DeleteAll() = (%d, %v), want (%d, nil)", removed, err, sourceCount)
	}
	for _, source := range sources {
		if _, found, err := store.GetObservation(ctx, source.ContentID()); err != nil || found {
			t.Fatalf("source %s remains: found=%t err=%v", source.ContentID(), found, err)
		}
	}
	page, err := store.ListObservationPage(ctx, storage.ObservationPageRequest{Limit: storage.MaxObservationPageRows, MaxBytes: storage.MaxObservationPageBytes})
	if err != nil || len(page.Observations) != 0 || page.HasMore {
		t.Fatalf("remaining page = (%d, %t, %v), want (0, false, nil)", len(page.Observations), page.HasMore, err)
	}
}

func TestDeleteAllAlsoRemovesUserAuthoredKnowledge(t *testing.T) {
	ctx := context.Background()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "blur.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	entity := knowledge.Entity{ID: "project-a", Kind: knowledge.Project, Name: "Aurora", State: knowledge.Confirmed,
		Facts: []knowledge.Fact{{ID: "fact-a", Text: "Launch checklist", State: knowledge.Confirmed, Sources: []knowledge.Source{{UserAuthored: true}}}}}
	if err := store.PutKnowledgeEntity(ctx, entity); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.DeleteAll(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListKnowledge(ctx, knowledge.PageRequest{Limit: knowledge.MaxPageSize})
	if err != nil || len(page.Entities) != 0 {
		t.Fatalf("user-authored knowledge remains after DeleteAll: %+v err=%v", page, err)
	}
}

func TestDeleteBeforeRemovesOnlyStrictlyOlderTimestampedSources(t *testing.T) {
	ctx := context.Background()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "blur.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cutoff := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	times := []time.Time{cutoff.Add(-time.Nanosecond), cutoff, cutoff.Add(time.Nanosecond)}
	sources := make([]capture.SanitizedObservation, len(times))
	for i, at := range times {
		observation, err := capture.SanitizeObservation("editor", "", fmt.Sprintf("retention source %d", i), 128)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.PutObservationAt(ctx, observation, at); err != nil {
			t.Fatal(err)
		}
		sources[i] = observation
	}
	service, err := NewService(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	removed, err := service.DeleteBefore(ctx, cutoff)
	if err != nil || removed != 1 {
		t.Fatalf("DeleteBefore() = (%d, %v), want (1, nil)", removed, err)
	}
	for i, observation := range sources {
		_, found, err := store.GetObservation(ctx, observation.ContentID())
		if err != nil || found != (i != 0) {
			t.Fatalf("source %d after retention deletion found=%t err=%v", i, found, err)
		}
	}
}

func TestDeleteBeforeFailsClosedWhenCaptureTimeIsUnknown(t *testing.T) {
	ctx := context.Background()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "blur.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	observation, err := capture.SanitizeObservation("editor", "", "unknown capture date", 128)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(ctx, observation, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(noTimestampPageStore{EncryptedStore: store}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.DeleteBefore(ctx, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("DeleteBefore(unknown timestamp) error = %v, want ErrInvalid", err)
	}
	if _, found, err := store.GetObservation(ctx, observation.ContentID()); err != nil || !found {
		t.Fatalf("unknown-timestamp source was deleted: found=%t err=%v", found, err)
	}
}

func TestNewServiceRejectsUnencryptedStore(t *testing.T) {
	if _, err := NewService(storage.NewEphemeralMemoryStore(), nil); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("NewService(unencrypted) error=%v, want ErrUnavailable", err)
	}
}

func TestDeleteSourceScrubsLiveActivityEvenWhenDurableDeleteFails(t *testing.T) {
	ctx := context.Background()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "blur.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	observation, err := capture.SanitizeObservation("editor", "", "context", 128)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservation(ctx, observation); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	engine := activity.New(activity.Config{MaxGap: time.Hour, RingSize: 8})
	_, _ = engine.Apply(activity.Event{Kind: activity.Seen, Monotonic: 0, Wall: start, SourceID: observation.ContentID(), AppID: "editor"})
	_, _ = engine.Apply(activity.Event{Kind: activity.Seen, Monotonic: int64(time.Minute), Wall: start.Add(time.Minute), SourceID: observation.ContentID(), AppID: "editor"})
	wantErr := errors.New("persistent delete unavailable")
	service, err := NewService(failingDeleteStore{EncryptedStore: store, err: wantErr}, engine)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.DeleteSource(ctx, observation.ContentID()); !errors.Is(err, wantErr) {
		t.Fatalf("DeleteSource error=%v, want persistence error", err)
	}
	if got := engine.Segments(); len(got) != 0 {
		t.Fatalf("live activity after failed durable deletion = %#v", got)
	}
	if _, found, err := store.GetObservation(ctx, observation.ContentID()); err != nil || !found {
		t.Fatalf("expected persistent source to remain for retry, found=%t err=%v", found, err)
	}
}

func TestDeletionsRecordTombstonesUnlessReplayed(t *testing.T) {
	ctx := context.Background()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "blur.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, err := NewService(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := capture.SanitizeObservation("editor", "", "first", 128)
	b, _ := capture.SanitizeObservation("editor", "", "second", 128)
	for _, o := range []capture.SanitizedObservation{a, b} {
		if err := store.PutObservation(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.DeleteSource(WithoutTombstone(ctx), a.ContentID()); err != nil {
		t.Fatal(err)
	}
	if log, _ := store.Tombstones(ctx); len(log) != 0 {
		t.Fatalf("replayed deletion logged %d tombstones", len(log))
	}
	if _, err := service.DeleteSource(ctx, b.ContentID()); err != nil {
		t.Fatal(err)
	}
	if _, err := service.DeleteAll(ctx); err != nil {
		t.Fatal(err)
	}
	log, err := store.Tombstones(ctx)
	if err != nil || len(log) != 2 || log[0].SourceID != b.ContentID() || log[1].SourceID != durable.AllSourcesTombstone {
		t.Fatalf("tombstones = %+v, %v", log, err)
	}
}
