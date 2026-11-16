package client

import (
	"context"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/chronicle"
	"github.com/hazcod/blur/internal/power"
)

type captureFeedFixture struct {
	mu        sync.Mutex
	pending   bool
	sources   []capture.Evidence
	reads     int
	entered   chan struct{}
	release   chan struct{}
	hourReads int
	hour      chronicle.Hour
}

func (r *captureFeedFixture) CaptureIndexPending(context.Context) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pending, nil
}
func (r *captureFeedFixture) ListCaptureSourceIDs(_ context.Context, _ string, after int64, limit int) ([]string, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if limit != 1 {
		panic("unbounded fixture metadata request")
	}
	index := int(after + 1)
	if index >= len(r.sources) {
		return nil, false, nil
	}
	return []string{r.sources[index].SourceID()}, index+1 < len(r.sources), nil
}
func (r *captureFeedFixture) GetSourceEvidenceWithCaptureTime(_ context.Context, id string) (capture.Evidence, time.Time, bool, error) {
	r.mu.Lock()
	r.reads++
	entered, release := r.entered, r.release
	r.entered = nil
	var source capture.Evidence
	for _, e := range r.sources {
		if e.SourceID() == id {
			source = e
			break
		}
	}
	r.mu.Unlock()
	if entered != nil {
		close(entered)
	}
	if release != nil {
		<-release
	} // deliberately cancellation-ignoring synthetic port
	return source, time.Unix(1, 0).UTC(), source.Validate() == nil, nil
}
func (r *captureFeedFixture) CaptureHourEvidence(context.Context, time.Time) (chronicle.Hour, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hourReads++
	return r.hour, nil
}

func makeCaptureFeedSource(t *testing.T, at time.Time) (capture.CaptureSummary, []capture.Evidence) {
	t.Helper()
	b, err := capture.NewCaptureBuilder("editor.app", "example.test", strings.Repeat("c", 64), at)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Abort()
	var parts []capture.CapturePart
	emit := func(_ context.Context, p []capture.CapturePart) error { parts = append(parts, p...); return nil }
	if err = b.Write(context.Background(), strings.Repeat("Ordered compiler evidence. ", 2000)+"Mira owns rollback.", emit); err != nil {
		t.Fatal(err)
	}
	summary, err := b.Finish(context.Background(), capture.CaptureComplete, emit)
	if err != nil {
		t.Fatal(err)
	}
	var sources []capture.Evidence
	for _, part := range parts {
		e, err := capture.CapturePartEvidence(summary, part, strings.Repeat("e", 64))
		if err != nil {
			t.Fatal(err)
		}
		sources = append(sources, e)
	}
	return summary, sources
}
func waitCaptureFeed(t *testing.T, f *chronicleFeed, count int) chronicle.Hour {
	t.Helper()
	deadline := time.After(2 * time.Second)
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		if h, ok := f.CurrentHour(); ok && len(h.Evidence) == count {
			return h
		}
		select {
		case <-deadline:
			t.Fatal("capture feed did not resolve bounded metadata")
			return chronicle.Hour{}
		case <-tick.C:
		}
	}
}

func TestChronicleCaptureFeedWaitsForIndexThenRetainsBoundedActualVisitEvidence(t *testing.T) {
	at := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Hour).Add(time.Minute)
	summary, sources := makeCaptureFeedSource(t, at)
	r := &captureFeedFixture{pending: true, sources: sources}
	f := newChronicleFeed()
	defer f.Close()
	w := newTestWorker()
	defer w.Close()
	f.SetWorker(w)
	f.AddCapture(summary, true, r)
	// Barrier on the same worker: a pending index cannot load payloads.
	time.Sleep(10 * time.Millisecond)
	r.mu.Lock()
	if r.reads != 0 {
		t.Fatal("pending index read capture payload")
	}
	r.pending = false
	r.mu.Unlock()
	f.WakeCaptures()
	h := waitCaptureFeed(t, f, len(sources))
	for _, e := range h.Evidence {
		if !e.At.Equal(at) || len(e.Text()) > chronicle.MaxEvidenceItemBytes || !e.Excerpted() {
			t.Fatal("feed retained full source or invented saved/index time")
		}
	}
}

func TestChronicleCaptureFeedPurgeRejectsCancellationIgnoringReadWithoutClearingNewHour(t *testing.T) {
	at := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Hour).Add(time.Minute)
	summary, sources := makeCaptureFeedSource(t, at)
	entered, release := make(chan struct{}), make(chan struct{})
	r := &captureFeedFixture{sources: sources, entered: entered, release: release}
	f := newChronicleFeed()
	defer f.Close()
	w := newTestWorker()
	defer w.Close()
	f.SetWorker(w)
	f.AddCapture(summary, true, r)
	select {
	case <-entered:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("source read not entered")
	}
	f.Purge()
	o, _ := capture.SanitizeObservation("editor.app", "", "new epoch evidence", 512)
	f.Add(o, at)
	close(release)
	h := waitCaptureFeed(t, f, 1)
	if h.Evidence[0].SourceID() != o.ContentID() {
		t.Fatal("old capture survived deletion epoch")
	}
}

func TestChronicleCaptureFeedCloseJoinsActualReadAndMetadataAdmissionIsBounded(t *testing.T) {
	at := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Hour).Add(time.Minute)
	summary, sources := makeCaptureFeedSource(t, at)
	entered, release := make(chan struct{}), make(chan struct{})
	r := &captureFeedFixture{sources: sources, entered: entered, release: release}
	f := newChronicleFeed()
	w := newTestWorker()
	defer w.Close()
	f.SetWorker(w)
	f.AddCapture(summary, true, r)
	select {
	case <-entered:
	case <-time.After(time.Second):
		close(release)
		f.Close()
		t.Fatal("source read not entered")
	}
	closed := make(chan struct{})
	go func() { f.Close(); close(closed) }()
	select {
	case <-closed:
		close(release)
		t.Fatal("Close abandoned active source read")
	case <-time.After(10 * time.Millisecond):
	}
	close(release)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not join released read")
	}
	f.AddCapture(summary, true, r)
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.captures) != 0 || f.current != nil {
		t.Fatal("closed feed retained capture metadata")
	}
}

type captureFeedBlockingModel struct {
	entered, release chan struct{}
	once             sync.Once
}

func (m *captureFeedBlockingModel) CountInputTokens(context.Context, chronicle.Packed) (int, error) {
	return 12, nil
}
func (m *captureFeedBlockingModel) Generate(ctx context.Context, p chronicle.Packed, _ int) (chronicle.Summary, error) {
	first := false
	m.once.Do(func() { first = true; close(m.entered) })
	if first {
		select {
		case <-ctx.Done():
			return chronicle.Summary{}, ctx.Err()
		case <-m.release:
		}
	}
	return chronicle.Summary{HourID: p.HourID, Start: p.Start, End: p.End, Text: "A short recap.", SourceIDs: p.SourceIDs}, nil
}

func TestChronicleCaptureFeedRetainsLateIndexedHourWhileEarlierGenerationRuns(t *testing.T) {
	at := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Hour).Add(time.Minute)
	summary, sources := makeCaptureFeedSource(t, at)
	hour := chronicle.Hour{Start: at.Truncate(time.Hour), End: at.Truncate(time.Hour).Add(time.Hour)}
	for _, s := range sources {
		e, err := chronicle.NewSourceEvidence(s, at, 9)
		if err != nil {
			t.Fatal(err)
		}
		hour.Evidence = append(hour.Evidence, e)
	}
	// The real worker requires chronological source ordering for equal times.
	sort.Slice(hour.Evidence, func(i, j int) bool { return hour.Evidence[i].SourceID() < hour.Evidence[j].SourceID() })
	repo := &feedRepo{rows: map[string]chronicle.Summary{}, notify: make(chan struct{}, 8)}
	model := &captureFeedBlockingModel{entered: make(chan struct{}), release: make(chan struct{})}
	w := chronicle.NewWorker(repo, feedPolicy{}, model, chronicle.WorkerConfig{Enabled: true, Provider: power.LocalProvider})
	defer w.Close()
	old := hour
	old.Evidence = old.Evidence[:1]
	if err := w.Schedule(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	select {
	case <-model.entered:
	case <-time.After(time.Second):
		close(model.release)
		t.Fatal("old generation not active")
	}
	r := &captureFeedFixture{sources: sources, hour: hour}
	f := newChronicleFeed()
	defer f.Close()
	f.SetWorker(w)
	f.AddCapture(summary, true, r)
	deadline := time.After(time.Second)
	for {
		r.mu.Lock()
		reads := r.hourReads
		r.mu.Unlock()
		if reads > 0 {
			break
		}
		select {
		case <-deadline:
			close(model.release)
			t.Fatal("expired capture not resolved")
		case <-time.After(time.Millisecond):
		}
	}
	f.mu.Lock()
	retained := len(f.captures)
	f.mu.Unlock()
	if retained != 1 {
		close(model.release)
		t.Fatal("duplicate active hour dropped the pending full input")
	}
	close(model.release)
	deadline = time.After(2 * time.Second)
	for {
		row, found, err := repo.GetChronicle(context.Background(), "hour:"+hour.Start.Format("20060102T15Z"))
		if err == nil && found && len(row.SourceIDs) == len(hour.Evidence) {
			break
		}
		select {
		case <-deadline:
			t.Fatal("late indexed parts never replaced earlier recap")
		case <-repo.notify:
		}
	}
}

func TestChronicleCaptureMetadataOverflowMarksCoverageWithoutPayloadQueue(t *testing.T) {
	at := time.Now().UTC().Add(2 * time.Hour)
	summary, sources := makeCaptureFeedSource(t, at)
	w := newTestWorker()
	defer w.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &chronicleFeed{worker: w, ctx: ctx, captureWake: make(chan struct{}, 1), generation: 1}
	for i := 0; i < maxChronicleFeedQueue; i++ {
		f.captures = append(f.captures, chronicleCaptureJob{edition: summary.ContentID(), at: at.Add(time.Duration(i+1) * time.Second), after: -1})
	}
	f.AddCapture(summary, true, &captureFeedFixture{sources: sources})
	if len(f.captures) != maxChronicleFeedQueue || f.limitedStart.IsZero() {
		t.Fatal("capture metadata overflow unbounded or coverage not marked")
	}
}
