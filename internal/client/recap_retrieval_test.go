package client

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/chronicle"
	"github.com/hazcod/blur/internal/deletion"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

func TestRecapIncludesMorningBeforeBusyLateDayWithoutSummaries(t *testing.T) {
	f := newAskFixture(t)
	defer f.client.Close()
	f.data.activity = nil
	var morning string
	for i := 0; i < 28; i++ {
		at := f.now.Add(-time.Duration(i) * time.Minute)
		text := fmt.Sprintf("Late release review %d", i)
		if i == 27 {
			at, text = f.now.Add(-7*time.Hour), "Morning: investigated the Aurora startup failure."
		}
		o, _ := capture.SanitizeObservation("com.example.editor", "", text, 4096)
		f.data.observations[o.ContentID()], f.data.observationAt[o.ContentID()] = o, at
		if i == 27 {
			morning = o.ContentID()
		}
	}
	got, err := f.client.RetrievePlan(context.Background(), todayPlan(assistant.KindRecap))
	if err != nil {
		t.Fatal(err)
	}
	if !hasRecapSource(got.Evidence, morning) {
		t.Fatal("day recap omitted the morning because recent captures filled its selection")
	}
}

func TestRecapSupplementsTwoSummariesWithUncoveredHours(t *testing.T) {
	f := newAskFixture(t)
	defer f.client.Close()
	f.data.activity = nil
	f.client.chronicles = fakeChronicles{rows: []chronicle.Summary{
		{HourID: "h1", Start: f.now.Add(-7 * time.Hour), End: f.now.Add(-6 * time.Hour), Text: "Morning research"},
		{HourID: "h2", Start: f.now.Add(-6 * time.Hour), End: f.now.Add(-5 * time.Hour), Text: "Morning planning"},
	}}
	got, err := f.client.RetrievePlan(context.Background(), todayPlan(assistant.KindRecap))
	if err != nil || !hasRecapSource(got.Evidence, f.ids["wa-today2"]) {
		t.Fatalf("two summaries hid later captured work: %+v %v", got, err)
	}
}

func TestRecapKeepsDistinctChroniclesWhenTheyShareOneSupportingCapture(t *testing.T) {
	f := newAskFixture(t)
	defer f.client.Close()
	f.data.activity = nil
	sourceID := f.ids["wa-today"]
	f.client.chronicles = fakeChronicles{rows: []chronicle.Summary{
		{HourID: "hour:20261111T08Z", Start: f.now.Add(-7 * time.Hour), End: f.now.Add(-6 * time.Hour), Text: "Morning: reviewed the rollout checklist.", SourceIDs: []string{sourceID}},
		{HourID: "hour:20261111T09Z", Start: f.now.Add(-6 * time.Hour), End: f.now.Add(-5 * time.Hour), Text: "Then: clarified the release owner.", SourceIDs: []string{sourceID}},
	}}
	got, err := f.client.RetrievePlan(context.Background(), todayPlan(assistant.KindRecap))
	if err != nil {
		t.Fatal(err)
	}
	texts := map[string]bool{}
	var recapIDs []string
	for _, item := range got.Evidence {
		texts[item.Text] = true
		if item.Layer == assistant.LayerHour {
			recapIDs = append(recapIDs, item.SourceID)
		}
	}
	if !texts["Morning: reviewed the rollout checklist."] || !texts["Then: clarified the release owner."] {
		t.Fatalf("distinct hourly recaps collapsed onto a shared supporting capture: %+v", got.Evidence)
	}
	if len(recapIDs) != 2 || recapIDs[0] == recapIDs[1] || recapIDs[0] == sourceID || recapIDs[1] == sourceID {
		t.Fatalf("hourly recap IDs do not independently identify each recap: %v", recapIDs)
	}
	followup, err := f.client.RetrieveSources(context.Background(), todayPlan(assistant.KindRecap), []string{recapIDs[0]})
	if err != nil || len(followup.Evidence) != 1 || followup.Evidence[0].SourceID != recapIDs[0] {
		t.Fatalf("a cited recap could not be reloaded for a follow-up: %+v %v", followup, err)
	}
}

func TestRecapReadsBeyondFirstChroniclePageAcrossWeek(t *testing.T) {
	f := newAskFixture(t)
	defer f.client.Close()
	f.data.activity = nil
	for id := range f.data.observations {
		delete(f.data.observations, id)
	}
	start := f.now.AddDate(0, 0, -7)
	rows := make([]chronicle.Summary, 50)
	for i := range rows {
		at := start.Add(time.Duration(i) * 3 * time.Hour)
		rows[i] = chronicle.Summary{HourID: fmt.Sprintf("hour-%d", i), Start: at, End: at.Add(time.Hour), Text: fmt.Sprintf("Work session %d", i)}
	}
	pages := &pagedRecapChronicles{rows: rows}
	f.client.chronicles = pages
	plan := assistant.Plan{Kind: assistant.KindRecap, Question: "What did I do this week?", Scope: assistant.Scope{Start: start, End: f.now}}
	got, err := f.client.RetrievePlan(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	first, last := false, false
	for _, item := range got.Evidence {
		first = first || item.Text == "Work session 0"
		last = last || item.Text == "Work session 49"
	}
	if !first || !last || pages.calls < 2 {
		t.Fatalf("week recap used only an early chronicle page: first=%t last=%t reads=%d", first, last, pages.calls)
	}
}

func TestRecapSelectionPreservesAppScopeAndCancellation(t *testing.T) {
	f := newAskFixture(t)
	defer f.client.Close()
	got, err := f.client.RetrievePlan(context.Background(), todayPlan(assistant.KindRecap, waApp))
	if err != nil || len(got.Evidence) != 2 {
		t.Fatal(got, err)
	}
	for _, item := range got.Evidence {
		if !strings.EqualFold(item.AppID, waApp) || strings.Contains(item.Text, "last week") {
			t.Fatal("recap crossed app/time scope", item)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.client.RetrievePlan(ctx, todayPlan(assistant.KindRecap)); !errors.Is(err, context.Canceled) {
		t.Fatal("recap ignored cancellation", err)
	}
}

func TestRecapDomainScopeExcludesOtherSitesAndFutureCaptures(t *testing.T) {
	f := newAskFixture(t)
	defer f.client.Close()
	f.data.activity = nil
	f.data.observations = map[string]capture.SanitizedObservation{}
	f.data.observationAt = map[string]time.Time{}
	for _, fixture := range []struct {
		domain, text string
		delta        time.Duration
	}{
		{"one.example", "Morning research", -4 * time.Hour},
		{"two.example", "Unrelated site", -3 * time.Hour},
		{"one.example", "Future fixture must not be recalled", time.Hour},
	} {
		o, _ := capture.SanitizeObservation("com.example.browser", fixture.domain, fixture.text, 4096)
		f.data.observations[o.ContentID()], f.data.observationAt[o.ContentID()] = o, f.now.Add(fixture.delta)
		if fixture.delta > 0 {
			at := f.now.Add(fixture.delta)
			f.data.activity = append(f.data.activity, activity.Segment{ID: "future-segment", AppID: o.AppID(), Domain: o.Domain(), SourceIDs: []string{o.ContentID()}, WallStart: at, WallEnd: at.Add(time.Minute), Duration: int64(time.Minute)})
		}
	}
	p := todayPlan(assistant.KindRecap)
	p.Domains = []string{"one.example"}
	got, err := f.client.RetrievePlan(context.Background(), p)
	if err != nil || len(got.Evidence) != 1 || got.Evidence[0].Domain != "one.example" || got.Evidence[0].Text != "Morning research" {
		t.Fatal("domain/time scope crossed", got, err)
	}
}

func TestRecapRejectsStalledChronicleContinuation(t *testing.T) {
	f := newAskFixture(t)
	defer f.client.Close()
	pages := &stalledRecapChronicles{}
	f.client.chronicles = pages
	_, err := f.client.RetrievePlan(context.Background(), todayPlan(assistant.KindRecap))
	if !errors.Is(err, ErrActivityLimit) || pages.calls != 2 {
		t.Fatalf("stalled continuation was not bounded: calls=%d err=%v", pages.calls, err)
	}
}

type stalledRecapChronicles struct {
	testChronicles
	calls int
}

func (p *stalledRecapChronicles) ListChronicles(context.Context, time.Time, time.Time, string, int) ([]chronicle.Summary, string, error) {
	p.calls++
	return []chronicle.Summary{{HourID: "same", Text: "Old summary"}}, "stalled", nil
}

func hasRecapSource(items []assistant.Evidence, id string) bool {
	for _, item := range items {
		if item.SourceID == id {
			return true
		}
	}
	return false
}

func TestRecapRetainsDistributedLargeEncryptedOriginalsAcrossReopenAndDeletion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	store, err := durable.OpenDurable(path, securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { func() { _ = store.Close() }() }()
	start := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	ids := make([]string, 8)
	for i := range ids {
		text := fmt.Sprintf("PERIOD_%d ", i) + strings.Repeat("Complete saved evidence. ", 14000)
		o, err := capture.SanitizeObservation("com.example.editor", "project.example", text, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = o.ContentID()
		if err := store.PutObservationAt(context.Background(), o, start.Add(time.Duration(i*3)*time.Hour+time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	for round := 0; round < 2; round++ {
		deleter, err := deletion.NewService(store, nil)
		if err != nil {
			t.Fatal(err)
		}
		c := NewClient(store).WithDeletion(deleter)
		c.askNow = func() time.Time { return start.Add(36 * time.Hour) }
		plan := todayPlan(assistant.KindRecap)
		got, err := c.RetrievePlan(context.Background(), plan)
		want := 8 - round
		if err != nil || len(got.Evidence) != want {
			c.Close()
			t.Fatalf("large originals lost temporal coverage: round=%d items=%d err=%v", round, len(got.Evidence), err)
		}
		for _, item := range got.Evidence {
			if len(item.Text) < 300000 || item.Partial || item.Domain != "project.example" || round == 1 && item.SourceID == ids[3] {
				c.Close()
				t.Fatal("original changed or deleted source reappeared", item.SourceID)
			}
		}
		if round == 0 {
			if _, err := c.DeleteSource(ids[3]); err != nil {
				c.Close()
				t.Fatal(err)
			}
			c.Close()
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = durable.OpenDurable(path, securestore.OfflineFixtureKey(), durable.OpenExisting)
			if err != nil {
				t.Fatal(err)
			}
		} else {
			c.Close()
		}
	}
}

func TestQuickRecapRetainsVisibleCoverageDisclosure(t *testing.T) {
	turns := appendQuickConversation(nil, "What did I do today?", assistant.Result{Answer: assistant.Answer{Text: "Recorded work."}, CoverageLimited: true})
	if len(turns) != 1 || !strings.Contains(quickTurnMeta(turns[0]), "recap may miss work") {
		t.Fatal("popup hid limited recap coverage", turns)
	}
}

type pagedRecapChronicles struct {
	testChronicles
	rows  []chronicle.Summary
	calls int
}

func (p *pagedRecapChronicles) ListChronicles(ctx context.Context, start, end time.Time, after string, limit int) ([]chronicle.Summary, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	p.calls++
	index, _ := strconv.Atoi(after)
	last := min(len(p.rows), index+limit)
	next := ""
	if last < len(p.rows) {
		next = strconv.Itoa(last)
	}
	return p.rows[index:last], next, nil
}
