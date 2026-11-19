package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/localapi"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
	"github.com/hazcod/blur/internal/tasks"
)

func TestNaturalSearchAndMCPShareVisitAndAppScopes(t *testing.T) {
	ctx := context.Background()
	s, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) { _ = close() }(s.Close)
	now := time.Date(2026, 11, 13, 15, 0, 0, 0, time.UTC)
	add := func(app, domain, text string, at time.Time) string {
		o, err := capture.SanitizeObservation(app, domain, text, 4096)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.PutObservationAt(ctx, o, at); err != nil {
			t.Fatal(err)
		}
		return o.ContentID()
	}
	old := now.AddDate(0, 0, -5)
	safari := add("com.apple.safari", "example.com", "Mira owns the Aurora rollback.", old)
	o, _, _ := s.GetObservation(ctx, safari)
	visit := now.AddDate(0, 0, -1).Add(-time.Hour)
	if err := s.RecordObservationSighting(ctx, o, visit); err != nil {
		t.Fatal(err)
	}
	code := add("com.microsoft.vscode", "", "Mira owns the Borealis deployment.", visit)
	_ = add("com.apple.safari", "example.com", "Mira attends a different meeting today.", now.Add(-time.Hour))
	unrelated := add("com.apple.safari", "work.example", "Independent compiler research", visit.Add(-time.Hour))
	otherSite := add("com.apple.safari", "work.example", "Mira reviews the Sable release.", visit)
	otherApp := add("com.microsoft.vscode", "example.com", "Mira builds a local parser.", visit)
	literal := add("com.microsoft.vscode", "", "Literal yesterday appears in today.swift", old)
	codeIdentifier := add("com.microsoft.vscode", "", "Return context.Canceled from time.Now checks", old)
	c := NewClient(s)
	defer c.Close()
	c.askNow = func() time.Time { return now }
	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"Mira yesterday", []string{safari, code, otherSite, otherApp}},
		{"Mira in Safari yesterday", []string{safari, otherSite}},
		{"Mira in Safari yesterday.", []string{safari, otherSite}},
		{"what was I doing in Safari yesterday", []string{safari, unrelated, otherSite}},
		{"Mira on example.com yesterday", []string{safari, otherApp}},
		{"Mira on https://example.com yesterday", []string{safari, otherApp}},
		{"Mira on https://example.com/rollback?q=today yesterday", []string{safari, otherApp}},
		{"Mira in Safari on example.com yesterday", []string{safari}},
		{"what was I doing in Safari on example.com yesterday", []string{safari}},
		{"nonexistent in Safari yesterday", nil},
		{"today.swift", []string{literal}},
		{"context.Canceled", []string{codeIdentifier}},
		{"time.Now", []string{codeIdentifier}},
		{"\"yesterday\"", []string{literal}},
		{"`Mira` in Safari yesterday", []string{safari, otherSite}},
	} {
		t.Run(tc.query, func(t *testing.T) {
			page, err := c.SearchMemoryPageContext(ctx, tc.query)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, hit := range page.Hits {
				got = append(got, hit.SourceID)
			}
			assertSearchSourceIDs(t, got, tc.want)
			if tc.query == "Mira in Safari on example.com yesterday" {
				encoded, _ := json.Marshal(page.Hits[0])
				var metadata map[string]any
				_ = json.Unmarshal(encoded, &metadata)
				if metadata["captured_at"] != visit.Format(time.RFC3339Nano) {
					t.Fatalf("Search hid the matching visit time: %s", encoded)
				}
			}
			raw, _ := json.Marshal(mcpSearchParams{Query: tc.query, Limit: 8})
			wire, err := c.HandleMCPRead(ctx, localapi.RPCSearchMemory, raw)
			if err != nil {
				t.Fatal(err)
			}
			var response mcpSearchResponse
			if err := json.Unmarshal(wire, &response); err != nil {
				t.Fatal(err)
			}
			got = nil
			for _, hit := range response.Results {
				got = append(got, hit.SourceID)
			}
			assertSearchSourceIDs(t, got, tc.want)
			if tc.query == "Mira in Safari on example.com yesterday" {
				var metadata struct {
					Results []struct {
						At string `json:"captured_at"`
					} `json:"results"`
				}
				_ = json.Unmarshal(wire, &metadata)
				if metadata.Results[0].At != visit.Format(time.RFC3339Nano) {
					t.Fatalf("MCP hid the matching visit time: %s", wire)
				}
			}
		})
	}
}

func TestScopedLiteralSearchRequiresEveryTerm(t *testing.T) {
	c, s, now := todayTaskClient(t)
	ctx := context.Background()
	o, _ := capture.SanitizeObservation("com.apple.safari", "example.com", "alpha beta without the final word", 512)
	if err := s.PutObservationAt(ctx, o, now.AddDate(0, 0, -1)); err != nil {
		t.Fatal(err)
	}
	query := `"alpha beta gamma" in Safari yesterday`
	page, err := c.SearchMemoryPageContext(ctx, query)
	if err != nil || len(page.Hits) != 0 {
		t.Fatalf("literal Search returned a partial match: %+v %v", page, err)
	}
	params, _ := json.Marshal(mcpSearchParams{Query: query, Limit: 5})
	wire, err := c.HandleMCPRead(ctx, localapi.RPCSearchMemory, params)
	var response mcpSearchResponse
	if err != nil || json.Unmarshal(wire, &response) != nil || len(response.Results) != 0 {
		t.Fatalf("literal MCP returned a partial match: %s %v", wire, err)
	}
}

func TestLiteralSearchRejectsTooManyMandatoryTerms(t *testing.T) {
	c, s, now := todayTaskClient(t)
	ctx := context.Background()
	o, _ := capture.SanitizeObservation("com.apple.safari", "example.com", "unrelated capture", 512)
	if err := s.PutObservationAt(ctx, o, now.AddDate(0, 0, -1)); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`"a01 a02 a03 a04 a05 a06 a07 a08 a09 a10 a11 a12 a13 a14 a15 a16 a17" yesterday`,
		`"a01 a02 a03 a04 a05 a06 a07 a08" "a09 a10 a11 a12 a13 a14 a15 a16 a17" yesterday`,
	} {
		page, err := c.SearchMemoryPageContext(ctx, query)
		if !errors.Is(err, ErrInvalidRequest) || len(page.Hits) != 0 {
			t.Fatalf("over-budget literal broadened into browsing: %+v %v", page, err)
		}
		params, _ := json.Marshal(mcpSearchParams{Query: query, Limit: 5})
		wire, err := c.HandleMCPRead(ctx, localapi.RPCSearchMemory, params)
		if !errors.Is(err, ErrInvalidRequest) || len(wire) != 0 {
			t.Fatalf("over-budget literal MCP returned data: %s %v", wire, err)
		}
	}
}

func TestQuotedTaskVocabularyRemainsSearchable(t *testing.T) {
	c, _, _ := todayTaskClient(t)
	ctx := context.Background()
	created, err := c.CreateTask(ctx, "Document completed reminder tasks", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{`"completed"`, `"reminder"`, `"tasks"`, `show "completed" tasks`} {
		page, err := c.SearchMemoryPageContext(ctx, query)
		if err != nil || len(page.Hits) != 1 || page.Hits[0].Task == nil || page.Hits[0].Task.ID != created.Item.ID {
			t.Fatalf("literal task word %s was removed: %+v %v", query, page, err)
		}
		params, _ := json.Marshal(mcpSearchParams{Query: query, Limit: 5})
		wire, err := c.HandleMCPRead(ctx, localapi.RPCSearchMemory, params)
		var response mcpSearchResponse
		if err != nil || json.Unmarshal(wire, &response) != nil || len(response.Results) != 1 || response.Results[0].AppID != "blur.tasks" {
			t.Fatalf("literal task word %s was removed from MCP: %s %v", query, wire, err)
		}
	}
}

func TestNaturalSearchAndMCPUseCancellationHistory(t *testing.T) {
	c, s, now := todayTaskClient(t)
	ctx := context.Background()
	for i := 1; i <= 2; i++ {
		item := tasks.Item{ID: fmt.Sprintf("%032x", i), Revision: 1, Title: fmt.Sprintf("Compiler review %d", i), State: tasks.Open, UserAuthored: true, CreatedAt: now.AddDate(0, 0, -3), UpdatedAt: now.AddDate(0, 0, -3)}
		if err := s.PutTask(ctx, item, 0); err != nil {
			t.Fatal(err)
		}
		item.Revision, item.State, item.UpdatedAt = 2, tasks.Cancelled, now.AddDate(0, 0, -i)
		if err := s.PutTask(ctx, item, 1); err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			// A later reopen must not erase the cancellation from yesterday.
			item.Revision, item.State, item.UpdatedAt = 3, tasks.Open, now
			if err := s.PutTask(ctx, item, 2); err != nil {
				t.Fatal(err)
			}
		}
	}
	query := "cancelled tasks yesterday"
	page, err := c.SearchMemoryPageContext(ctx, query)
	if err != nil || len(page.Hits) != 1 || page.Hits[0].Task == nil || page.Hits[0].Task.ID != "00000000000000000000000000000001" || !strings.Contains(page.Hits[0].Text, "Task state event: cancelled") {
		t.Fatalf("cancellation period used current state instead of history: %+v %v", page, err)
	}
	params, _ := json.Marshal(mcpSearchParams{Query: query, Limit: 5})
	wire, err := c.HandleMCPRead(ctx, localapi.RPCSearchMemory, params)
	var response mcpSearchResponse
	if err != nil || json.Unmarshal(wire, &response) != nil || len(response.Results) != 1 || !strings.Contains(response.Results[0].Excerpt, "Task state event: cancelled") {
		t.Fatalf("MCP omitted the cancellation event: %s %v", wire, err)
	}
	answer, err := c.AskMemory(ctx, "Which tasks were cancelled yesterday?")
	if err != nil || !strings.Contains(answer.Answer.Text, "1 task cancelled in that period") || len(answer.Targets) != 1 || answer.Targets[0].ID != "00000000000000000000000000000001" {
		t.Fatalf("Ask cancellation history disagrees with Search: %+v %v", answer, err)
	}
}

func TestSearchAndMCPDiscloseIncompleteCoverage(t *testing.T) {
	for _, kind := range []string{"omitted task history", "unavailable meaning search"} {
		t.Run(kind, func(t *testing.T) {
			c, s, now := todayTaskClient(t)
			ctx := context.Background()
			query := "completed tasks yesterday"
			if kind == "omitted task history" {
				item := tasks.Item{ID: "00000000000000000000000000000001", Title: "Compiler review", UserAuthored: true, CreatedAt: now.Add(-time.Hour)}
				for revision := 1; revision <= tasks.MaxHistory+3; revision++ {
					item.Revision = uint64(revision)
					item.State = tasks.Open
					if revision%2 == 0 {
						item.State = tasks.Completed
					}
					item.UpdatedAt = now.Add(-time.Hour).Add(time.Duration(revision) * time.Minute)
					if err := s.PutTask(ctx, item, uint64(revision-1)); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				// A keyword query with exact hits never consults the meaning step, so
				// only a natural-language question can find it unavailable.
				query = "what was the compiler review?"
				o, _ := capture.SanitizeObservation("com.apple.safari", "example.com", "Compiler review", 512)
				if err := s.PutObservationAt(ctx, o, now); err != nil {
					t.Fatal(err)
				}
				withTaskSemanticFixture(t, c, s, &taskRecallEmbedder{score: .8})
				c.memoryIndex.service.Close()
			}
			params, _ := json.Marshal(mcpSearchParams{Query: query, Limit: 5})
			wire, err := c.HandleMCPRead(ctx, localapi.RPCSearchMemory, params)
			var response struct {
				Limited bool `json:"coverage_limited"`
			}
			if err != nil || json.Unmarshal(wire, &response) != nil || !response.Limited {
				t.Fatalf("MCP hid %s: %s %v", kind, wire, err)
			}
			page, err := c.SearchMemoryPageContext(ctx, query)
			encoded, _ := json.Marshal(page)
			var fields map[string]any
			_ = json.Unmarshal(encoded, &fields)
			if err != nil || fields["CoverageLimited"] != true {
				t.Fatalf("Search hid %s: %s %v", kind, encoded, err)
			}
		})
	}
}

type taskChangesDuringMCP struct {
	*durable.Store
	id    string
	reads int
}

func (s *taskChangesDuringMCP) GetTask(ctx context.Context, id string) (tasks.Item, bool, error) {
	item, found, err := s.Store.GetTask(ctx, id)
	if id != s.id || err != nil || !found {
		return item, found, err
	}
	s.reads++
	if s.reads == 2 {
		updated := tasks.Clone(item)
		updated.Title = "Compiler review changed during search"
		updated.Revision++
		updated.UpdatedAt = updated.UpdatedAt.Add(time.Second)
		if err := s.PutTask(ctx, updated, item.Revision); err != nil {
			return tasks.Item{}, false, err
		}
		return s.Store.GetTask(ctx, id)
	}
	return item, found, nil
}

func TestMCPRejectsTaskEditedAfterItsSearchSnapshot(t *testing.T) {
	c, s, _ := todayTaskClient(t)
	ctx := context.Background()
	created, err := c.CreateTask(ctx, "Compiler review", nil)
	if err != nil {
		t.Fatal(err)
	}
	c.data = &taskChangesDuringMCP{Store: s, id: created.Item.ID}
	wire, err := c.HandleMCPRead(ctx, localapi.RPCSearchMemory, json.RawMessage(`{"query":"compiler","limit":5}`))
	if !errors.Is(err, ErrMemoryChanging) || len(wire) != 0 {
		t.Fatalf("MCP published stale task text: %s %v", wire, err)
	}
}

func TestQuotedSearchDoesNotAdmitSemanticOnlySourcesOrTasks(t *testing.T) {
	c, s, now := todayTaskClient(t)
	ctx := context.Background()
	want, _ := capture.SanitizeObservation("com.apple.safari", "example.com", "Mira owns the rollback.", 512)
	other, _ := capture.SanitizeObservation("com.apple.safari", "example.com", "Drycleanres unrelated semantic source", 512)
	for _, o := range []capture.SanitizedObservation{want, other} {
		if err := s.PutObservationAt(ctx, o, now.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.CreateTask(ctx, "Drycleanres unrelated semantic task", nil); err != nil {
		t.Fatal(err)
	}
	withTaskSemanticFixture(t, c, s, &taskRecallEmbedder{score: .99})
	page, err := c.SearchMemoryPageContext(ctx, "`Mira`")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, hit := range page.Hits {
		ids = append(ids, hit.SourceID)
	}
	assertSearchSourceIDs(t, ids, []string{want.ContentID()})
}

func TestNaturalSearchKeepsItsOwnFiftyResultLimit(t *testing.T) {
	c, s, now := todayTaskClient(t)
	ctx := context.Background()
	for i := 0; i < 60; i++ {
		o, _ := capture.SanitizeObservation("com.apple.safari", "example.com", fmt.Sprintf("Mira review number %03d", i), 512)
		if err := s.PutObservationAt(ctx, o, now.Add(-time.Hour-time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	page, err := c.SearchMemoryPageContext(ctx, "Mira in Safari today")
	if err != nil || len(page.Hits) != 50 || !page.HasMore {
		t.Fatalf("Search inherited answer-source limit: rows=%d more=%t err=%v", len(page.Hits), page.HasMore, err)
	}
}

func TestNaturalSearchAndMCPUseReminderCalendarBeforeTitleRanking(t *testing.T) {
	c, _, now := todayTaskClient(t)
	ctx := context.Background()
	want, err := c.CreateTask(ctx, "Compiler review", &tasks.Reminder{DueAt: now.Add(3 * time.Hour), Timezone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTask(ctx, "Compiler review tomorrow", &tasks.Reminder{DueAt: now.AddDate(0, 0, 1), Timezone: "UTC"}); err != nil {
		t.Fatal(err)
	}
	query := "What are my compiler reminders today?"
	page, err := c.SearchMemoryPageContext(ctx, query)
	if err != nil || len(page.Hits) != 1 || page.Hits[0].Task == nil || page.Hits[0].Task.ID != want.Item.ID {
		t.Fatalf("Search ignored reminder due period: %+v %v", page, err)
	}
	raw, _ := json.Marshal(mcpSearchParams{Query: query, Limit: 5})
	wire, err := c.HandleMCPRead(ctx, localapi.RPCSearchMemory, raw)
	if err != nil {
		t.Fatal(err)
	}
	var response mcpSearchResponse
	if json.Unmarshal(wire, &response) != nil || len(response.Results) != 1 || response.Results[0].AppID != "blur.tasks" {
		t.Fatalf("MCP omitted current calendar reminder: %s", wire)
	}
}

func TestMixedLiteralTaskQueryKeepsUnquotedReminderGrammar(t *testing.T) {
	c, _, now := todayTaskClient(t)
	ctx := context.Background()
	created, err := c.CreateTask(ctx, "Compiler review", &tasks.Reminder{DueAt: now.Add(time.Hour), Timezone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTask(ctx, "Compiler review tomorrow", &tasks.Reminder{DueAt: now.AddDate(0, 0, 1), Timezone: "UTC"}); err != nil {
		t.Fatal(err)
	}
	query := `show "compiler" reminders today`
	page, err := c.SearchMemoryPageContext(ctx, query)
	if err != nil || len(page.Hits) != 1 || page.Hits[0].Task == nil || page.Hits[0].Task.ID != created.Item.ID {
		t.Fatalf("unquoted task grammar became mandatory title text: %+v %v", page, err)
	}
	params, _ := json.Marshal(mcpSearchParams{Query: query, Limit: 5})
	wire, err := c.HandleMCPRead(ctx, localapi.RPCSearchMemory, params)
	var response mcpSearchResponse
	if err != nil || json.Unmarshal(wire, &response) != nil || len(response.Results) != 1 || !strings.Contains(response.Results[0].Excerpt, "Compiler review") {
		t.Fatalf("MCP lost mixed literal task query: %s %v", wire, err)
	}
}

func TestTaskStateHistoryUsesSharedLastWeekScope(t *testing.T) {
	c, s, now := todayTaskClient(t)
	ctx := context.Background()
	for i := 1; i <= 4; i++ {
		item := tasks.Item{ID: fmt.Sprintf("%032x", i), Revision: 1, Title: fmt.Sprintf("Compiler review %d", i), State: tasks.Open, UserAuthored: true, CreatedAt: now.AddDate(0, 0, -14), UpdatedAt: now.AddDate(0, 0, -14)}
		if err := s.PutTask(ctx, item, 0); err != nil {
			t.Fatal(err)
		}
		item.Revision, item.State, item.UpdatedAt = 2, tasks.Completed, now.AddDate(0, 0, -7)
		if i > 2 {
			item.State = tasks.Cancelled
		}
		if i%2 == 0 {
			item.UpdatedAt = now.AddDate(0, 0, -1)
		}
		if err := s.PutTask(ctx, item, 1); err != nil {
			t.Fatal(err)
		}
		if i%2 != 0 {
			item.Revision, item.State, item.UpdatedAt = 3, tasks.Open, now
			if err := s.PutTask(ctx, item, 2); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, tc := range []struct{ query, id string }{
		{"completed tasks last week", "00000000000000000000000000000001"},
		{"completed compiler tasks last week", "00000000000000000000000000000001"},
		{"cancelled tasks last week", "00000000000000000000000000000003"},
		{"cancelled compiler tasks last week", "00000000000000000000000000000003"},
	} {
		t.Run(tc.query, func(t *testing.T) {
			page, err := c.SearchMemoryPageContext(ctx, tc.query)
			if err != nil || len(page.Hits) != 1 || page.Hits[0].Task == nil || page.Hits[0].Task.ID != tc.id {
				t.Fatalf("shared task period lost: %+v %v", page, err)
			}
			params, _ := json.Marshal(mcpSearchParams{Query: tc.query, Limit: 5})
			wire, err := c.HandleMCPRead(ctx, localapi.RPCSearchMemory, params)
			var response mcpSearchResponse
			if err != nil || json.Unmarshal(wire, &response) != nil || len(response.Results) != 1 || !strings.Contains(response.Results[0].Excerpt, "Task state event:") {
				t.Fatalf("MCP shared task period lost: %s %v", wire, err)
			}
		})
	}
}

func TestSearchFindsAcceptedTaskTitlesBeyondQueryTokenLimit(t *testing.T) {
	c, _, _ := todayTaskClient(t)
	ctx := context.Background()
	created, err := c.CreateTask(ctx, "Compiler a01 a02 a03 a04 a05 a06 a07 a08 a09 a10 a11 a12 a13 a14 a15 a16", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"show all tasks", "compiler", `"compiler"`} {
		page, err := c.SearchMemoryPageContext(ctx, query)
		if err != nil || len(page.Hits) != 1 || page.Hits[0].Task == nil || page.Hits[0].Task.ID != created.Item.ID {
			t.Fatalf("accepted task title is invisible to %q: %+v %v", query, page, err)
		}
		params, _ := json.Marshal(mcpSearchParams{Query: query, Limit: 5})
		wire, err := c.HandleMCPRead(ctx, localapi.RPCSearchMemory, params)
		var response mcpSearchResponse
		if err != nil || json.Unmarshal(wire, &response) != nil || len(response.Results) != 1 || !strings.Contains(response.Results[0].Excerpt, "Compiler") {
			t.Fatalf("accepted task title missing from MCP: %s %v", wire, err)
		}
	}
}

func assertSearchSourceIDs(t *testing.T, got, want []string) {
	t.Helper()
	got = append([]string(nil), got...)
	want = append([]string(nil), want...)
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("source count %d, want %d; got=%v want=%v", len(got), len(want), got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("sources=%v want=%v", got, want)
		}
	}
}
