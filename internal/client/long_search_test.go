package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/deletion"
	"github.com/hazcod/blur/internal/localapi"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/semantic"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/durable"
)

type longSearchProbe struct {
	*durable.Store
	queries             []search.ExactQuery
	dates               [][]string
	afterQuery          func()
	rowBudget           int
	afterWindow         func()
	returnedWindowBytes int
}

func (s *longSearchProbe) SearchExact(ctx context.Context, q search.ExactQuery) (search.ExactPage, error) {
	if s.rowBudget > 0 {
		q.MaxRows = min(q.MaxRows, s.rowBudget)
	}
	s.queries = append(s.queries, q)
	page, err := s.Store.SearchExact(ctx, q)
	if s.afterQuery != nil {
		fn := s.afterQuery
		s.afterQuery = nil
		fn()
	}
	return page, err
}
func (s *longSearchProbe) ListSourceEvidenceWindow(ctx context.Context, q storage.ObservationWindowRequest) (storage.SourceEvidenceWindow, error) {
	page, err := s.Store.ListSourceEvidenceWindow(ctx, q)
	for _, source := range page.Sources {
		s.returnedWindowBytes += len(source.Text()) + len(source.AppID()) + len(source.Domain())
	}
	if s.afterWindow != nil {
		fn := s.afterWindow
		s.afterWindow = nil
		fn()
	}
	return page, err
}
func (s *longSearchProbe) ListActivitySegmentsForDates(ctx context.Context, dates []string, limit int) ([]activity.Segment, error) {
	s.dates = append(s.dates, slices.Clone(dates))
	return s.Store.ListActivitySegmentsForDates(ctx, dates, limit)
}
func longSearchFixture(t *testing.T) (*Client, *longSearchProbe, map[string]string) {
	t.Helper()
	t.Setenv("TZ", "UTC")
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "long-search.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ids := map[string]string{}
	for _, tc := range []struct{ name, app, at string }{
		{"early", "com.apple.mail", "2024-01-01T00:01:00Z"},
		{"middle", "com.apple.mail", "2024-06-15T09:00:00Z"},
		{"late", "com.apple.mail", "2024-12-31T23:59:00Z"},
		{"legacy", "com.apple.mail", "2023-07-01T09:00:00Z"},
		{"outside", "com.apple.mail", "2025-01-01T00:01:00Z"},
		{"slack", "com.tinyspeck.slackmacgap", "2024-07-01T09:00:00Z"},
		{"self", "com.hazcod.blur", "2024-07-01T09:00:00Z"},
	} {
		o, err := capture.SanitizeObservation(tc.app, "finance.example", "Orchid invoice saved evidence: "+tc.name+" edition, corrected supplier amount recorded.", 1024)
		if err != nil {
			t.Fatal(err)
		}
		if tc.name == "early" {
			o = o.WithTitle("Orchid invoice")
		}
		at, _ := time.Parse(time.RFC3339, tc.at)
		if err := store.PutObservationAt(context.Background(), o, at); err != nil {
			t.Fatal(err)
		}
		ids[tc.name] = o.ContentID()
	}
	// This legacy read has no observation-sighting event; its only in-range
	// evidence is the authenticated foreground segment linking the old source.
	at := time.Date(2024, 6, 16, 10, 0, 0, 0, time.UTC)
	if err := store.PutActivitySegment(context.Background(), activity.Segment{ID: "legacy-june-read", AppID: "com.apple.mail", Domain: "finance.example", SourceIDs: []string{ids["legacy"]}, WallStart: at, WallEnd: at.Add(time.Minute), Duration: int64(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	o, _, err := store.GetObservation(context.Background(), ids["early"])
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordObservationSighting(context.Background(), o, time.Date(2024, 12, 20, 9, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	probe := &longSearchProbe{Store: store}
	c := NewClient(probe)
	c.askNow = func() time.Time { return time.Date(2026, 11, 21, 15, 0, 0, 0, time.UTC) }
	t.Cleanup(c.Close)
	return c, probe, ids
}
func assertLongSearchIDs(t *testing.T, hits []SearchHit, want []string) {
	t.Helper()
	var got []string
	for _, hit := range hits {
		got = append(got, hit.SourceID)
	}
	slices.Sort(got)
	want = slices.Clone(want)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("long-scope originals = %v, want %v", got, want)
	}
}
func TestLongSearchNativeAndMCPKeepBothEndsAndLegacyRevisit(t *testing.T) {
	c, probe, ids := longSearchFixture(t)
	query := `find "orchid invoice" in Mail in 2024`
	page, err := c.SearchMemoryPageContext(context.Background(), query)
	if err != nil {
		t.Fatal("year-wide native search unavailable:", err)
	}
	want := []string{ids["early"], ids["middle"], ids["late"], ids["legacy"]}
	assertLongSearchIDs(t, page.Hits, want)
	for _, hit := range page.Hits {
		if hit.SourceID == ids["legacy"] && hit.CapturedAt.UTC().Format(time.RFC3339) != "2024-06-16T10:00:00Z" {
			t.Fatalf("legacy visit lost its actual in-range time: %v", hit.CapturedAt)
		}
		if hit.SourceID == ids["early"] && hit.CapturedAt.UTC().Format(time.RFC3339) != "2024-12-20T09:00:00Z" {
			t.Fatalf("revisit was duplicated or dated outside its latest match: %v", hit.CapturedAt)
		}
	}
	if len(probe.queries) < 2 {
		t.Fatal("long search did not cross storage windows")
	}
	for _, q := range probe.queries {
		if q.CapturedEnd.Sub(q.CapturedStart) > 31*24*time.Hour || q.MaxRows > 1000 || q.MaxBytes > 2<<20 {
			t.Fatal("search widened a physical query budget")
		}
	}
	for _, dates := range probe.dates {
		if len(dates) > 33 {
			t.Fatal("activity read exceeded bounded date partitions")
		}
	}
	raw, _ := json.Marshal(mcpSearchParams{Query: query, Limit: 8})
	wire, err := c.HandleMCPRead(context.Background(), localapi.RPCSearchMemory, raw)
	if err != nil {
		t.Fatal(err)
	}
	var response mcpSearchResponse
	if err := json.Unmarshal(wire, &response); err != nil {
		t.Fatal(err)
	}
	var actual []string
	for _, hit := range response.Results {
		actual = append(actual, hit.SourceID)
	}
	slices.Sort(actual)
	slices.Sort(want)
	if !slices.Equal(actual, want) {
		t.Fatalf("MCP lost year-wide originals: %s", wire)
	}
}
func TestLongSearchBrowseKeepsActualCapturesAcrossYear(t *testing.T) {
	c, _, ids := longSearchFixture(t)
	page, err := c.SearchMemoryPageContext(context.Background(), "in Mail in 2024")
	if err != nil {
		t.Fatal(err)
	}
	assertLongSearchIDs(t, page.Hits, []string{ids["early"], ids["middle"], ids["late"]})
	if page.Hits[0].SourceID != ids["late"] {
		t.Fatal("scoped browse did not keep newest capture first")
	}
}

func TestLongSearchGlobalPrefixKeepsStrongerOldMatchAndLatestVisit(t *testing.T) {
	c, _, ids := longSearchFixture(t)
	start, end := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	windows, err := c.searchScopeWindows(context.Background(), assistant.Plan{Scope: assistant.Scope{Start: start, End: end}}, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	page, err := c.searchExactWindows(context.Background(), search.ExactQuery{Terms: []string{"invoice", "orchid"}, AppID: "com.apple.mail", TopK: 1, MaxRows: 1000, MaxBytes: 2 << 20}, windows)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Hits) != 1 || page.Hits[0].SourceID != ids["early"] || page.Hits[0].CapturedAt.UTC().Format(time.RFC3339) != "2024-12-20T09:00:00Z" {
		t.Fatalf("global prefix lost the strong old source or duplicated its revisit: %+v", page)
	}
}

func TestLongSearchMonthKeepsCrossBoundaryLegacyActivity(t *testing.T) {
	c, probe, _ := longSearchFixture(t)
	o, err := capture.SanitizeObservation("com.apple.mail", "finance.example", "Juniper invoice corrected supplier details were read across the New Year boundary.", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err := probe.PutObservationAt(context.Background(), o, time.Date(2023, 7, 1, 9, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	from := time.Date(2023, 12, 31, 23, 59, 0, 0, time.UTC)
	if err := probe.PutActivitySegment(context.Background(), activity.Segment{ID: "legacy-new-year-read", AppID: "com.apple.mail", SourceIDs: []string{o.ContentID()}, WallStart: from, WallEnd: from.Add(2 * time.Minute), Duration: int64(2 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	page, err := c.SearchMemoryPageContext(context.Background(), `find "juniper invoice" in Mail in January 2024`)
	if err != nil {
		t.Fatal(err)
	}
	assertLongSearchIDs(t, page.Hits, []string{o.ContentID()})
	if page.Hits[0].CapturedAt.UTC().Format(time.RFC3339) != "2024-01-01T00:00:00Z" {
		t.Fatalf("overlapping read was dated outside requested month: %v", page.Hits[0].CapturedAt)
	}
}

func TestLongSearchAppAndDomainQualifiersIntersect(t *testing.T) {
	c, probe, ids := longSearchFixture(t)
	o, _ := capture.SanitizeObservation("com.apple.mail", "other.example", "Orchid invoice corrected supplier information from the wrong domain.", 1024)
	if err := probe.PutObservationAt(context.Background(), o, time.Date(2024, 9, 1, 9, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	page, err := c.SearchMemoryPageContext(context.Background(), `find "orchid invoice" in Mail on finance.example in 2024`)
	if err != nil {
		t.Fatal(err)
	}
	assertLongSearchIDs(t, page.Hits, []string{ids["early"], ids["middle"], ids["late"], ids["legacy"]})
}

func TestLongSearchQuotedYearRemainsTopic(t *testing.T) {
	c, probe, _ := longSearchFixture(t)
	o, _ := capture.SanitizeObservation("com.apple.mail", "finance.example", "The saved 2024 planning document was received and corrected the following year.", 1024)
	if err := probe.PutObservationAt(context.Background(), o, time.Date(2025, 2, 1, 9, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	page, err := c.SearchMemoryPageContext(context.Background(), `find "2024"`)
	if err != nil {
		t.Fatal(err)
	}
	assertLongSearchIDs(t, page.Hits, []string{o.ContentID()})
}

func TestLongSearchUsesResolvedZoneAfterProcessClockWasCached(t *testing.T) {
	_, offset := time.Now().Zone()
	zone, want := "Asia/Kathmandu", "2023-12-31T18:15:00Z"
	if offset == 20700 {
		zone, want = "UTC", "2024-01-01T00:00:00Z"
	}
	c, _, _ := longSearchFixture(t)
	t.Setenv("TZ", zone)
	c.askNow = nil
	_, plan, err := c.naturalSearchPage(context.Background(), `find "orchid invoice" in Mail in 2024`, 8)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Scope.Start.UTC().Format(time.RFC3339) != want {
		t.Fatalf("Search kept cached zone instead of %s: %+v", zone, plan.Scope)
	}
}

func TestLongSearchCancellationAfterEarlierWindowReturnsNoPage(t *testing.T) {
	c, probe, _ := longSearchFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	probe.afterQuery = cancel
	page, err := c.SearchMemoryPageContext(ctx, `find "orchid invoice" in Mail in 2024`)
	if !errors.Is(err, context.Canceled) || len(page.Hits) != 0 {
		t.Fatalf("cancelled long read published earlier rows: %+v %v", page, err)
	}
}

func TestLongSearchBudgetFailureAfterEarlierWindowReturnsNoPage(t *testing.T) {
	c, probe, _ := longSearchFixture(t)
	// Tighten the real encrypted index's budget only after a first child read.
	// The actual index must reject a prefix it cannot prove, not return a stub.
	probe.afterQuery = func() { probe.rowBudget = 1 }
	page, err := c.SearchMemoryPageContext(context.Background(), `find "orchid invoice" in Mail in 2024`)
	if !errors.Is(err, search.ErrIndexBudget) || len(page.Hits) != 0 {
		t.Fatalf("unproven long prefix published partial rows: %+v %v", page, err)
	}
}

func TestLongSearchErasureAfterEarlierWindowReturnsNoPage(t *testing.T) {
	c, probe, ids := longSearchFixture(t)
	service, err := deletion.NewService(probe.Store, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.WithDeletion(service)
	probe.afterQuery = func() {
		if _, err := c.DeleteSource(ids["early"]); err != nil {
			t.Error(err)
		}
	}
	page, err := c.SearchMemoryPageContext(context.Background(), `find "orchid invoice" in Mail in 2024`)
	if err == nil || len(page.Hits) != 0 {
		t.Fatalf("erased read published an earlier child prefix: %+v %v", page, err)
	}
}

func TestLongSearchInternalBoundaryVisit(t *testing.T) {
	c, probe, ids := longSearchFixture(t)
	at := time.Date(2024, 7, 16, 23, 59, 0, 0, time.UTC)
	if err := probe.PutActivitySegment(context.Background(), activity.Segment{ID: "internal-boundary-visit", AppID: "com.apple.mail", SourceIDs: []string{ids["legacy"]}, WallStart: at, WallEnd: at.Add(2 * time.Minute), Duration: int64(2 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	page, err := c.SearchMemoryPageContext(context.Background(), `find "orchid invoice" in Mail in 2024`)
	if err != nil {
		t.Fatal(err)
	}
	for _, hit := range page.Hits {
		if hit.SourceID == ids["legacy"] {
			if !hit.CapturedAt.Equal(at) {
				t.Fatalf("actual matching visit %s was shifted to artificial child boundary %s", at, hit.CapturedAt)
			}
			return
		}
	}
	t.Fatal("missing legacy")
}

type longSearchLimitedIndex struct{ *longSearchProbe }

func (s *longSearchLimitedIndex) SearchExact(ctx context.Context, q search.ExactQuery) (search.ExactPage, error) {
	p, err := s.longSearchProbe.SearchExact(ctx, q)
	p.CoverageLimited = true
	return p, err
}
func TestLongSearchRetainsCoverageFlag(t *testing.T) {
	_, probe, _ := longSearchFixture(t)
	c := NewClient(&longSearchLimitedIndex{probe})
	defer c.Close()
	c.askNow = func() time.Time { return time.Date(2026, 11, 21, 15, 0, 0, 0, time.UTC) }
	page, err := c.SearchMemoryPageContext(context.Background(), `find "no such literal" in 2024`)
	if err != nil {
		t.Fatal(err)
	}
	if !page.CoverageLimited {
		t.Fatal("limited child became complete empty public page")
	}
}

func TestLongSearchConcurrentAdditions(t *testing.T) {
	c, probe, _ := longSearchFixture(t)
	probe.afterQuery = func() {
		for _, item := range []struct {
			text string
			at   time.Time
		}{
			{"Azalea receipt latest addition", time.Date(2024, 12, 31, 9, 0, 0, 0, time.UTC)},
			{"Azalea receipt older addition", time.Date(2024, 1, 1, 9, 0, 0, 0, time.UTC)},
		} {
			o, err := capture.SanitizeObservation("com.apple.mail", "finance.example", item.text, 1024)
			if err != nil {
				t.Fatal(err)
			}
			if err := probe.PutObservationAt(context.Background(), o, item.at); err != nil {
				t.Fatal(err)
			}
		}
	}
	page, err := c.SearchMemoryPageContext(context.Background(), `find "azalea receipt" in 2024`)
	if !errors.Is(err, ErrMemoryChanging) || len(page.Hits) != 0 {
		t.Fatalf("non-snapshot result published after ordered commits: %+v %v", page, err)
	}
}
func TestLongSearchDurableErasure(t *testing.T) {
	c, probe, ids := longSearchFixture(t)
	probe.afterQuery = func() {
		if err := probe.DeleteObservationRecord(context.Background(), ids["late"]); err != nil {
			t.Fatal(err)
		}
	}
	page, err := c.SearchMemoryPageContext(context.Background(), `find "orchid invoice" in Mail in 2024`)
	if !errors.Is(err, ErrMemoryChanging) || len(page.Hits) != 0 {
		t.Fatalf("durable erasure returned successful mixed page with %d hits, coverageLimited=%t", len(page.Hits), page.CoverageLimited)
	}
}
func TestLongSearchSelectedModelSynonym(t *testing.T) {
	for _, expand := range []bool{false, true} {
		t.Run(map[bool]string{false: "recent", true: "rewrite"}[expand], func(t *testing.T) {
			c, s, _, g := singleSearchFixture(t)
			t.Setenv("TZ", "UTC")
			o, err := capture.SanitizeObservation("reader.app", "example.test", "Drycleanres saved an old wool coat receipt.", 4096)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.PutObservationAt(context.Background(), o, time.Date(2024, 6, 1, 9, 0, 0, 0, time.UTC)); err != nil {
				t.Fatal(err)
			}
			if expand {
				for i := 0; i < recallCandidates+1; i++ {
					other, err := capture.SanitizeObservation("reader.app", "example.test", fmt.Sprintf("Unrelated December meeting %d", i), 4096)
					if err != nil {
						t.Fatal(err)
					}
					if err := s.PutObservationAt(context.Background(), other, time.Date(2024, 12, 1, 9, i, 0, 0, time.UTC)); err != nil {
						t.Fatal(err)
					}
				}
			}
			g.respond = func(ctx context.Context, system, user string) (string, error) {
				if strings.Contains(system, "alternate search queries") {
					return `{"queries":["drycleanres"]}`, nil
				}
				var input struct {
					Passages []struct{ Ref, Text string } `json:"passages"`
				}
				if err := json.Unmarshal([]byte(user), &input); err != nil {
					return "", err
				}
				matches := []map[string]any{}
				for _, p := range input.Passages {
					if strings.Contains(p.Text, "wool coat") {
						matches = append(matches, map[string]any{"ref": p.Ref, "score": .95})
					}
				}
				result := map[string]any{"matches": matches}
				if strings.Contains(system, "both tasks") {
					result["queries"] = []string{"drycleanres"}
				}
				body, err := json.Marshal(result)
				return string(body), err
			}
			page, err := c.SearchMemoryPageContext(context.Background(), "laundry in 2024")
			if err != nil {
				t.Fatal(err)
			}
			assertLongSearchIDs(t, page.Hits, []string{o.ContentID()})
			if !page.CoverageLimited || !page.Relaxed || !page.Hits[0].Semantic {
				t.Fatalf("unproven meaning match flags: %+v", page)
			}
			if expand && g.calls < 2 {
				t.Fatalf("rewrite never ran: %d calls", g.calls)
			}
		})
	}
}

func TestLongSearchRevisitDuringReadReturnsNoPage(t *testing.T) {
	c, probe, ids := longSearchFixture(t)
	probe.afterQuery = func() {
		o, found, err := probe.GetObservation(context.Background(), ids["middle"])
		if err != nil || !found {
			t.Fatalf("original: %v %v", found, err)
		}
		if err := probe.RecordObservationSighting(context.Background(), o, time.Date(2024, 12, 31, 12, 0, 0, 0, time.UTC)); err != nil {
			t.Fatal(err)
		}
	}
	page, err := c.SearchMemoryPageContext(context.Background(), `find "orchid invoice" in 2024`)
	if !errors.Is(err, ErrMemoryChanging) || len(page.Hits) != 0 {
		t.Fatalf("changed visit published: %+v %v", page, err)
	}
}
func TestLongSearchBrowseMutationReturnsNoPage(t *testing.T) {
	for _, erase := range []bool{false, true} {
		t.Run(map[bool]string{false: "addition", true: "erasure"}[erase], func(t *testing.T) {
			c, probe, ids := longSearchFixture(t)
			probe.afterWindow = func() {
				if erase {
					if err := probe.DeleteObservationRecord(context.Background(), ids["late"]); err != nil {
						t.Fatal(err)
					}
					return
				}
				o, err := capture.SanitizeObservation("com.apple.mail", "finance.example", "Azalea new source", 1024)
				if err != nil {
					t.Fatal(err)
				}
				if err := probe.PutObservationAt(context.Background(), o, time.Date(2024, 12, 31, 12, 0, 0, 0, time.UTC)); err != nil {
					t.Fatal(err)
				}
			}
			page, err := c.SearchMemoryPageContext(context.Background(), "in Mail in 2024")
			if !errors.Is(err, ErrMemoryChanging) || len(page.Hits) != 0 {
				t.Fatalf("changed browse published: %+v %v", page, err)
			}
		})
	}
}

func TestLongSearchBrowseLargeSourcesStopsWithinReadBudget(t *testing.T) {
	c, probe, _ := longSearchFixture(t)
	for _, app := range []string{"com.apple.mail", "com.tinyspeck.slackmacgap"} {
		for i := 0; i < 2; i++ {
			body := fmt.Sprintf("Large saved document %s %d ", app, i) + strings.Repeat("x", 600<<10)
			o, err := capture.SanitizeObservation(app, "finance.example", body, capture.MaxObservationTextBytes)
			if err != nil {
				t.Fatal(err)
			}
			if err := probe.PutObservationAt(context.Background(), o, time.Date(2024, 12, 31, 10, i, 0, 0, time.UTC)); err != nil {
				t.Fatal(err)
			}
		}
	}
	page, err := c.SearchMemoryPageContext(context.Background(), "in Mail in 2024")
	if err != nil {
		t.Fatalf("large browse failed instead of bounded coverage: %v", err)
	}
	if len(page.Hits) == 0 || !page.CoverageLimited || !page.HasMore {
		t.Fatalf("unproven large browse: %+v", page)
	}
	probe.returnedWindowBytes = 0
	_, err = c.selectedRecentRecallCandidates(context.Background(), semantic.Query{AppIDs: []string{"com.apple.mail", "com.tinyspeck.slackmacgap"}, Start: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	if probe.returnedWindowBytes > MaxSearchBytes {
		t.Fatalf("meaning candidate source bytes %d exceed aggregate %d", probe.returnedWindowBytes, MaxSearchBytes)
	}
}
