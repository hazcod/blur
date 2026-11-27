package client

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

func scopedStreamFixture(t *testing.T, years int) (*Client, *longSearchProbe, assistant.Plan, []string) {
	t.Helper()
	t.Setenv("TZ", "UTC")
	s, err := durable.OpenDurable(filepath.Join(t.TempDir(), "search.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	end := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	start := end.AddDate(-years, 0, 0)
	var ids []string
	for i, at := range []time.Time{start.Add(time.Minute), start.AddDate(years/2, 0, 0), end.Add(-time.Minute), start.AddDate(-1, 0, 0)} {
		o, err := capture.SanitizeObservation("com.apple.mail", "finance.example", fmt.Sprintf("Iris invoice source %d records the supplier correction.", i), 1024)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.PutObservationAt(context.Background(), o, at); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, o.ContentID())
	}
	visit := start.AddDate(years/2, 1, 0)
	if err := s.PutActivitySegment(context.Background(), activity.Segment{ID: "multi-year-legacy-visit", AppID: "com.apple.mail", Domain: "finance.example", WallStart: visit, WallEnd: visit.Add(time.Minute), Duration: int64(time.Minute), SourceIDs: []string{ids[3]}}); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct{ app, domain, text string }{
		{"com.tinyspeck.slackmacgap", "finance.example", "Iris invoice wrong app."},
		{"com.apple.mail", "other.example", "Iris invoice wrong domain."},
		{"com.apple.mail", "finance.example", "An unrelated security article mentions neither requested topic."},
	} {
		o, err := capture.SanitizeObservation(fixture.app, fixture.domain, fixture.text, 1024)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.PutObservationAt(context.Background(), o, start.AddDate(1, 0, 0)); err != nil {
			t.Fatal(err)
		}
	}
	probe := &longSearchProbe{Store: s}
	c := NewClient(probe)
	c.askNow = func() time.Time { return end }
	t.Cleanup(c.Close)
	plan := assistant.Plan{Kind: assistant.KindLookup, Terms: []string{"invoice", "iris"}, LiteralTerms: []string{"invoice", "iris"}, Literal: true, AppIDs: []string{"com.apple.mail"}, Domains: []string{"finance.example"}, Scope: assistant.Scope{Start: start, End: end}}
	return c, probe, plan, ids
}

func TestScopedSearchStreamsThreeAndTenYearsWithoutTotalHorizon(t *testing.T) {
	for _, years := range []int{3, 10} {
		t.Run(fmt.Sprint(years), func(t *testing.T) {
			c, probe, plan, ids := scopedStreamFixture(t, years)
			page, err := c.rankedSearchPage(context.Background(), "iris invoice", plan, true, nil, 8)
			if err != nil {
				t.Fatalf("%d-year source query rejected: %v", years, err)
			}
			var got []string
			for _, hit := range page.Hits {
				got = append(got, hit.SourceID)
			}
			slices.Sort(got)
			slices.Sort(ids)
			if !slices.Equal(got, ids) {
				t.Fatalf("multi-year source query lost an end, middle or legacy revisit: %v want %v", got, ids)
			}
			for _, query := range probe.queries {
				if query.CapturedEnd.Sub(query.CapturedStart) > 31*24*time.Hour || len(query.ScopeSourceIDs) > search.MaxScopeSourceIDs || query.MaxRows > search.MaxIndexRows {
					t.Fatalf("widened a physical request: %+v", query)
				}
			}
		})
	}
}

func TestScopedSearchLongCancellationDiscardsEarlierPrefix(t *testing.T) {
	c, probe, plan, _ := scopedStreamFixture(t, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	probe.afterQuery = cancel
	page, err := c.rankedSearchPage(ctx, "iris invoice", plan, true, nil, 8)
	if !errors.Is(err, context.Canceled) || len(page.Hits) != 0 {
		t.Fatalf("cancelled long query published a successful prefix: %+v %v", page, err)
	}
}

func TestScopedAskKeepsLongScopeAndLegacyVisits(t *testing.T) {
	c, _, plan, ids := scopedStreamFixture(t, 10)
	retrieval, err := c.RetrievePlan(context.Background(), plan)
	if err != nil {
		t.Fatalf("multi-year Ask rejected: %v", err)
	}
	var got []string
	for _, item := range retrieval.Evidence {
		got = append(got, item.SourceID)
	}
	slices.Sort(got)
	slices.Sort(ids)
	if !slices.Equal(got, ids) {
		t.Fatalf("Ask lost scoped originals: %v want %v", got, ids)
	}
}

type denseSearchActivity struct {
	*longSearchProbe
	day                                     time.Time
	legacyID                                string
	count, pages, maxScope, firstQueryPages int
	queried                                 bool
	metadataCancel                          context.CancelFunc
}

func (s *denseSearchActivity) ListActivitySegmentsForDates(context.Context, []string, int) ([]activity.Segment, error) {
	return nil, errors.New("a dense source scope must use activity continuation pages")
}

func (s *denseSearchActivity) ListActivitySegmentsForDate(ctx context.Context, date, after string, limit int) ([]activity.Segment, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if date != s.day.Format("2006-01-02") {
		return nil, "", nil
	}
	start := 0
	if after != "" {
		index, err := strconv.Atoi(strings.TrimPrefix(after, "dense-"))
		if err != nil {
			return nil, "", err
		}
		start = index + 1
	}
	var rows []activity.Segment
	for i := start; i < min(start+limit, s.count); i++ {
		at := s.day.Add(time.Duration(i) * time.Second)
		row := activity.Segment{ID: fmt.Sprintf("dense-%d", i), AppID: "com.apple.mail", Domain: "finance.example", WallStart: at, WallEnd: at.Add(time.Second), Duration: int64(time.Second)}
		for j := range 32 {
			row.SourceIDs = append(row.SourceIDs, fmt.Sprintf("%064x", i*32+j+1))
		}
		if i == s.count-1 {
			row.SourceIDs[31] = s.legacyID
		}
		rows = append(rows, row)
	}
	s.pages++
	if s.metadataCancel != nil {
		cancel := s.metadataCancel
		s.metadataCancel = nil
		cancel()
	}
	if start+len(rows) < s.count {
		return rows, rows[len(rows)-1].ID, nil
	}
	return rows, "", nil
}

func (s *denseSearchActivity) SearchExact(ctx context.Context, query search.ExactQuery) (search.ExactPage, error) {
	if !s.queried {
		s.firstQueryPages, s.queried = s.pages, true
	}
	s.maxScope = max(s.maxScope, len(query.ScopeSourceIDs))
	return s.longSearchProbe.SearchExact(ctx, query)
}

func TestScopedSearchDrainsDenseActivityWithoutAggregateIDCeiling(t *testing.T) {
	c, probe, plan, ids := scopedStreamFixture(t, 3)
	dense := &denseSearchActivity{longSearchProbe: probe, day: plan.Scope.End.AddDate(0, 0, -14), legacyID: ids[3], count: 2500}
	c.Close()
	c = NewClient(dense)
	defer c.Close()
	page, err := c.rankedSearchPage(context.Background(), "iris invoice", plan, true, nil, 8)
	if err != nil {
		t.Fatalf("dense activity prevented exact search: %v", err)
	}
	if dense.firstQueryPages != 0 || dense.pages < 39 || dense.maxScope > 64*32 {
		t.Fatalf("scope metadata was eagerly accumulated or clipped: first=%d pages=%d maxIDs=%d", dense.firstQueryPages, dense.pages, dense.maxScope)
	}
	if !slices.ContainsFunc(page.Hits, func(hit search.IndexHit) bool {
		return hit.SourceID == ids[3] && hit.CapturedAt.Equal(dense.day.Add(2499*time.Second))
	}) {
		t.Fatal("dense activity continuation lost its tail-only legacy visit")
	}
}

func TestScopedAskStreamsShortDenseActivityWithoutBulkPreload(t *testing.T) {
	c, probe, plan, ids := scopedStreamFixture(t, 3)
	plan.Scope.Start = plan.Scope.End.AddDate(0, 0, -2)
	dense := &denseSearchActivity{longSearchProbe: probe, day: plan.Scope.End.AddDate(0, 0, -1), legacyID: ids[3], count: 2500}
	c.Close()
	c = NewClient(dense)
	defer c.Close()
	retrieval, err := c.RetrievePlan(context.Background(), plan)
	if err != nil {
		t.Fatalf("short dense Ask failed before paged source retrieval: %v", err)
	}
	var got []string
	for _, item := range retrieval.Evidence {
		got = append(got, item.SourceID)
	}
	want := []string{ids[2], ids[3]}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) || dense.pages < 39 || dense.maxScope > 2048 {
		t.Fatalf("short dense Ask lost tail evidence or widened page: %v want %v; pages=%d ids=%d", got, want, dense.pages, dense.maxScope)
	}
}

func TestScopedSearchMetadataCancellationReturnsNoPrefix(t *testing.T) {
	c, probe, plan, ids := scopedStreamFixture(t, 3)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dense := &denseSearchActivity{longSearchProbe: probe, day: plan.Scope.End.AddDate(0, 0, -14), legacyID: ids[3], count: 2500, metadataCancel: cancel}
	c.Close()
	c = NewClient(dense)
	defer c.Close()
	page, err := c.rankedSearchPage(ctx, "iris invoice", plan, true, nil, 8)
	if !errors.Is(err, context.Canceled) || len(page.Hits) != 0 {
		t.Fatalf("cancelled metadata walk published an earlier capture: %+v %v", page, err)
	}
}
