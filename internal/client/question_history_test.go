package client

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/durable"
)

type questionFixture struct {
	client *Client
	store  *durable.Store
	path   string
	source string
	now    time.Time
}

func newQuestionFixture(t *testing.T) *questionFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "blur.db")
	store, err := durable.OpenDurable(path, securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	o, err := capture.SanitizeObservation("com.tinyspeck.slackmacgap", "app.slack.com", "The Orfit external retest is scheduled Thursday 20 November.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 16, 15, 30, 0, 0, time.UTC)
	if err := store.PutObservationAt(context.Background(), o, now.Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	client := NewClient(store)
	t.Cleanup(client.Close)
	client.askNow = func() time.Time { return now }
	return &questionFixture{client: client, store: store, path: path, source: o.ContentID(), now: now}
}

func TestAskPersistsQuestionWithKindTimeAndCitations(t *testing.T) {
	f := newQuestionFixture(t)
	result, err := f.client.AskMemory(context.Background(), "what did Dima say about the Orfit retest?")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := f.store.ListQuestions(context.Background(), 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
	row := rows[0]
	if row.Question != "what did Dima say about the Orfit retest?" || !row.AskedAt.Equal(f.now) || row.PlanKind != planKindLabel(result.Plan.Kind) {
		t.Fatalf("row = %+v plan=%v", row, result.Plan.Kind)
	}
	want := slices.Clone(result.Answer.SourceIDs)
	slices.Sort(want)
	want = slices.Compact(want)
	if !slices.Equal(row.SourceIDs, filterLive(want, f.source)) {
		t.Fatalf("cited = %v, answer cited %v", row.SourceIDs, want)
	}
}

// filterLive keeps only the IDs the fixture can resolve.
func filterLive(ids []string, live ...string) []string {
	var out []string
	for _, id := range ids {
		if slices.Contains(live, id) {
			out = append(out, id)
		}
	}
	return out
}

func TestAskDoesNotPersistOverlongOrFailedQuestions(t *testing.T) {
	f := newQuestionFixture(t)
	long := "what did I do " + strings.Repeat("today ", 100)
	if _, err := f.client.AskMemory(context.Background(), long); err != nil {
		t.Logf("long ask returned %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = f.client.AskMemory(cancelled, "a cancelled question")
	rows, err := f.store.ListQuestions(context.Background(), 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("rows = %+v err=%v", rows, err)
	}
}

func TestAskSucceedsWhenQuestionHistoryCannotBeWritten(t *testing.T) {
	// A data layer without the optional history interface must not break Ask.
	data := &testData{}
	client := NewClient(data)
	defer client.Close()
	if _, err := client.AskMemory(context.Background(), "what is 2 + 2"); err != nil {
		t.Fatalf("Ask failed without a history store: %v", err)
	}
	if got := client.RecentQuestions(context.Background(), 8); len(got) != 0 {
		t.Fatalf("recent = %v", got)
	}
}

func TestRecentQuestionsReturnsNewestWindowOldestFirst(t *testing.T) {
	f := newQuestionFixture(t)
	for i, q := range []string{"alpha", "beta", "beta", "gamma"} {
		if err := appendTestQuestion(t, f.store, q, f.now.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	got := f.client.RecentQuestions(context.Background(), 2)
	if !slices.Equal(got, []string{"beta", "gamma"}) {
		t.Fatalf("recent = %v", got)
	}
}

func TestQuickHistoryLoadsPersistedQuestionsAtStartup(t *testing.T) {
	f := newQuestionFixture(t)
	for i := 0; i < quickHistoryLimit+3; i++ {
		if err := appendTestQuestion(t, f.store, "question "+string(rune('a'+i)), f.now.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	d := &desktop{client: f.client}
	d.restoreQuickHistory()
	if len(d.quickHistory.entries) != quickHistoryLimit {
		t.Fatalf("entries = %v", d.quickHistory.entries)
	}
	if got, ok := d.quickHistory.Previous(""); !ok || got != "question "+string(rune('a'+quickHistoryLimit+2)) {
		t.Fatalf("Up arrow recalled %q", got)
	}
	if got, ok := d.quickHistory.Complete("question a"); ok {
		t.Fatalf("completion offered %q for an entry older than the window", got)
	}
	if got, ok := d.quickHistory.Complete("question"); !ok || got != "question "+string(rune('a'+quickHistoryLimit+2)) {
		t.Fatalf("completion for a persisted entry = %q %v", got, ok)
	}
}

func TestQuickHistorySurvivesReopenButNotDeleteAll(t *testing.T) {
	f := newQuestionFixture(t)
	if err := appendTestQuestion(t, f.store, "persisted question", f.now); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := durable.OpenDurable(f.path, securestore.OfflineFixtureKey(), durable.OpenExisting)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	client := NewClient(reopened)
	defer client.Close()
	d := &desktop{client: client}
	d.restoreQuickHistory()
	if !slices.Equal(d.quickHistory.entries, []string{"persisted question"}) {
		t.Fatalf("after reopen = %v", d.quickHistory.entries)
	}
	if err := reopened.DeleteAllQuestions(context.Background()); err != nil {
		t.Fatal(err)
	}
	d.restoreQuickHistory()
	if len(d.quickHistory.entries) != 0 {
		t.Fatalf("history survived the namespace clear: %v", d.quickHistory.entries)
	}
}

func TestRestoreQuickHistoryWithoutAStoreLeavesHistoryEmpty(t *testing.T) {
	d := &desktop{}
	d.quickHistory.Record("typed")
	d.restoreQuickHistory()
	if len(d.quickHistory.entries) != 0 {
		t.Fatalf("entries = %v", d.quickHistory.entries)
	}
}

func appendTestQuestion(t *testing.T, store *durable.Store, question string, at time.Time) error {
	t.Helper()
	return store.AppendQuestion(context.Background(), storage.QuestionRecord{Question: question, AskedAt: at, PlanKind: "lookup"})
}
