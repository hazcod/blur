package client

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/chronicle"
	"github.com/hazcod/blur/internal/power"
)

type feedRepo struct {
	mu     sync.Mutex
	rows   map[string]chronicle.Summary
	notify chan struct{}
}

func (r *feedRepo) PutChronicle(_ context.Context, s chronicle.Summary) error {
	r.mu.Lock()
	r.rows[s.HourID] = s
	r.mu.Unlock()
	select {
	case r.notify <- struct{}{}:
	default:
	}
	return nil
}
func (r *feedRepo) GetChronicle(_ context.Context, id string) (chronicle.Summary, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.rows[id]
	return s, ok, nil
}

type feedPolicy struct{}

func (feedPolicy) Effective(context.Context) (power.Effective, error) {
	e, _ := power.Resolve(power.Config{Mode: power.Performance}, power.SystemState{})
	return e, nil
}

type feedModel struct{}

func (feedModel) CountInputTokens(context.Context, chronicle.Packed) (int, error) { return 12, nil }
func (feedModel) Generate(_ context.Context, p chronicle.Packed, _ int) (chronicle.Summary, error) {
	return chronicle.Summary{HourID: p.HourID, Start: p.Start, End: p.End, Text: "A short recap.", SourceIDs: p.SourceIDs}, nil
}

func TestChronicleFeedIsOptInEventDrivenAndClosesOnlyObservedHours(t *testing.T) {
	feed := newChronicleFeed()
	defer feed.Close()
	repo := &feedRepo{rows: map[string]chronicle.Summary{}, notify: make(chan struct{}, 1)}
	worker := chronicle.NewWorker(repo, feedPolicy{}, feedModel{}, chronicle.WorkerConfig{Enabled: true, Provider: power.LocalProvider})
	defer worker.Close()
	start := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Hour)
	first, _ := capture.SanitizeObservation("editor.app", "", "meeting notes first", 1024)
	feed.Add(first, start.Add(5*time.Minute))
	time.Sleep(10 * time.Millisecond)
	if got := len(repo.rows); got != 0 {
		t.Fatalf("unclosed hour scheduled early: %d", got)
	}
	feed.SetWorker(worker)
	feed.Add(first, start.Add(10*time.Minute))
	second, _ := capture.SanitizeObservation("editor.app", "", "meeting notes next hour", 1024)
	feed.Add(second, start.Add(time.Hour+time.Minute))
	select {
	case <-repo.notify:
	case <-time.After(time.Second):
		t.Fatal("closed hour was not summarized")
	}
	got, found, err := repo.GetChronicle(context.Background(), "hour:"+start.Format("20060102T15Z"))
	if err != nil || !found || len(got.SourceIDs) != 1 || got.SourceIDs[0] != first.ContentID() {
		t.Fatalf("summary=%+v found=%t err=%v", got, found, err)
	}
	feed.SetWorker(nil)
	third, _ := capture.SanitizeObservation("editor.app", "", "not opted in", 1024)
	feed.Add(third, start.Add(2*time.Hour+time.Minute))
	time.Sleep(10 * time.Millisecond)
	if len(repo.rows) != 1 {
		t.Fatalf("disabled feed created summaries: %d", len(repo.rows))
	}
}

func TestChronicleNotWrittenForDeletedSource(t *testing.T) {
	feed := newChronicleFeed()
	defer feed.Close()
	repo := &feedRepo{rows: map[string]chronicle.Summary{}, notify: make(chan struct{}, 1)}
	first, _ := capture.SanitizeObservation("editor.app", "", "meeting notes deleted", 1024)
	data := &testData{observations: map[string]capture.SanitizedObservation{first.ContentID(): first}}
	client := NewClient(data)
	client.addMemoryChangingHook(feed.Purge)
	verified := sourceVerifiedRepository{inner: repo, sources: data, client: client}
	worker := chronicle.NewWorker(verified, feedPolicy{}, feedModel{}, chronicle.WorkerConfig{Enabled: true, Provider: power.LocalProvider})
	defer worker.Close()
	feed.SetWorker(worker)
	start := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Hour)
	// The source is deleted after being queued but before the summary persists.
	delete(data.observations, first.ContentID())
	feed.Add(first, start.Add(time.Minute))
	next, _ := capture.SanitizeObservation("editor.app", "", "later hour", 1024)
	feed.Add(next, start.Add(time.Hour+time.Minute))
	select {
	case <-repo.notify:
		t.Fatal("chronicle persisted for deleted source")
	case <-time.After(200 * time.Millisecond):
	}
	if len(repo.rows) != 0 {
		t.Fatalf("rows=%d", len(repo.rows))
	}
}

func TestMemoryChangePurgesPendingFeed(t *testing.T) {
	feed := newChronicleFeed()
	defer feed.Close()
	feed.mu.Lock()
	feed.current = &chronicle.Hour{Start: time.Now().UTC().Truncate(time.Hour)}
	feed.mu.Unlock()
	client := NewClient(&testData{})
	client.addMemoryChangingHook(feed.Purge)
	client.NotifyMemoryChanging()
	defer client.NotifyMemoryStable()
	feed.mu.Lock()
	defer feed.mu.Unlock()
	if feed.current != nil {
		t.Fatal("current hour survived memory change")
	}
}
