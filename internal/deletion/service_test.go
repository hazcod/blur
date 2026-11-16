package deletion

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/profile"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/semantic"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/durable"
	"github.com/hazcod/blur/internal/tasks"
)

func TestDeleteAllIncludesIndependentExplicitPreferences(t *testing.T) {
	ctx := context.Background()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "blur.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(store.Close)
	at := time.Now().UTC()
	record := profile.Record{ID: strings.Repeat("a", 32), Revision: 1, Kind: profile.Fact, Text: "My preferred language is Dutch.", Enabled: true, UserAuthored: true, CreatedAt: at, UpdatedAt: at}
	if err := store.PutProfile(ctx, record, 0); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.DeleteAll(ctx); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.GetProfile(ctx, record.ID); err != nil || found {
		t.Fatalf("explicit preference remains: found=%t err=%v", found, err)
	}
}

func TestDeleteAllSplitsAtomicBatchesWhenLargeVectorsExceedByteBudget(t *testing.T) {
	ctx := context.Background()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "blur.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(store.Close)
	m := semantic.Model{Name: "fixture", Digest: strings.Repeat("a", 64), Dimensions: semantic.MaxDimensions, Format: semantic.FormatPlain}
	vector := make([]float32, m.Dimensions)
	vector[0] = 1
	for i := 0; i < 6; i++ {
		text := fmt.Sprintf("%04d", i) + strings.Repeat("x", semantic.MaxChunksPerSource+semantic.ChunkBytes-5)
		observation, err := capture.SanitizeObservation("reader.app", "", text, 4096)
		if err != nil {
			t.Fatal(err)
		}
		at := time.Now().UTC()
		if err := store.PutObservationAt(ctx, observation, at); err != nil {
			t.Fatal(err)
		}
		doc := semantic.Document{SourceID: observation.ContentID(), AppID: observation.AppID(), Model: m, CapturedAt: at, TextBytes: len(observation.Text())}
		for n := 0; n < semantic.MaxChunksPerSource; n++ {
			doc.Chunks = append(doc.Chunks, semantic.Chunk{Start: n, End: n + semantic.ChunkBytes, Vector: vector})
		}
		if err := store.PutSemanticDocument(ctx, doc); err != nil {
			t.Fatal(err)
		}
	}
	service, err := NewService(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	removed, err := service.DeleteAll(ctx)
	if err != nil || removed != 6 {
		t.Fatalf("large-vector deletion removed=%d err=%v", removed, err)
	}
	page, err := store.ListSemanticDocuments(ctx, semantic.PageRequest{ModelKey: m.Key(), Limit: 8, MaxBytes: semantic.MaxPageBytes})
	if err != nil || len(page.Documents) != 0 {
		t.Fatalf("vectors remain after deletion: %d err=%v", len(page.Documents), err)
	}
}

type failingDeleteStore struct {
	storage.EncryptedStore
	err error
}

type recoveryOrderStore struct {
	*durable.Store
	recovered     bool
	recoveryError error
}

func (s *recoveryOrderStore) RecoverSourceDeletion(context.Context) error {
	s.recovered = true
	return s.recoveryError
}
func (s *recoveryOrderStore) ListObservationPage(ctx context.Context, r storage.ObservationPageRequest) (storage.ObservationPage, error) {
	if !s.recovered {
		return storage.ObservationPage{}, durable.ErrDeletionPending
	}
	return s.Store.ListObservationPage(ctx, r)
}

type recoveryOrderGuard struct{ store *recoveryOrderStore }

func (g recoveryOrderGuard) WithSourceDeletionGuard(ctx context.Context, action func(context.Context, func([]tasks.Item) error) error) error {
	if !g.store.recovered {
		return durable.ErrDeletionPending
	}
	return action(ctx, func([]tasks.Item) error { return nil })
}

func (g recoveryOrderGuard) GuardSources(context.Context, []string) (func(), error) {
	if !g.store.recovered {
		return nil, durable.ErrDeletionPending
	}
	return func() {}, nil
}
func (g recoveryOrderGuard) GuardAll(context.Context) (func(), error) {
	if !g.store.recovered {
		return nil, durable.ErrDeletionPending
	}
	return func() {}, nil
}

func TestDeletionRecoversCommittedIntentBeforeReadingNotificationOrSourceState(t *testing.T) {
	for _, action := range []string{"source", "all", "retention"} {
		t.Run(action, func(t *testing.T) {
			ctx := context.Background()
			base, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
			if err != nil {
				t.Fatal(err)
			}
			defer func(close func() error) { _ = close() }(base.Close)
			o, _ := capture.SanitizeObservation("chat.app", "local", "Source evidence", 1024)
			if err := base.PutObservationAt(ctx, o, time.Now().Add(-time.Hour)); err != nil {
				t.Fatal(err)
			}
			store := &recoveryOrderStore{Store: base}
			s, err := NewService(store, nil)
			if err != nil {
				t.Fatal(err)
			}
			s.SetNotificationCleanup(recoveryOrderGuard{store})
			switch action {
			case "source":
				_, err = s.DeleteSource(ctx, o.ContentID())
			case "all":
				_, err = s.DeleteAll(ctx)
			case "retention":
				_, err = s.DeleteBefore(ctx, time.Now())
			}
			if err != nil {
				t.Fatal("retry did not recover the already committed erasure before reading gated state", err)
			}
			if _, found, err := base.GetObservation(ctx, o.ContentID()); err != nil || found {
				t.Fatal(found, err)
			}
		})
	}
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
	defer func(close func() error) { _ = close() }(store.Close)
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
	defer func(close func() error) { _ = close() }(store.Close)
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
	defer func(close func() error) { _ = close() }(store.Close)
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
	defer func(close func() error) { _ = close() }(store.Close)
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
	defer func(close func() error) { _ = close() }(store.Close)
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
	defer func(close func() error) { _ = close() }(store.Close)
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
	defer func(close func() error) { _ = close() }(store.Close)
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
