package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/hazcod/blur/internal/model/modeltest"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/chronicle"
	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/knowledge/derive"
	"github.com/hazcod/blur/internal/localapi"
	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/durable"
	"github.com/hazcod/blur/internal/tasks"
	"github.com/hazcod/blur/internal/today"
)

type testData struct {
	today                     today.Snapshot
	found                     bool
	query                     search.ExactQuery
	queries                   []search.ExactQuery
	page                      search.ExactPage
	pages                     []search.ExactPage
	searchCalls               int
	readSourceIDs             []string
	observationWindowRequests []storage.ObservationWindowRequest
	knowledge                 knowledge.Page
	confirmedDate             string
	confirmedID               string
	answeredID                string
	answer                    string
	dismissedID               string
	observations              map[string]capture.SanitizedObservation
	observationAt             map[string]time.Time
	activity                  []activity.Segment
	activityDates             []string
	savedToday                today.Snapshot
	savedTodaySet             bool
	todayWrites               int
}

type testKnowledgeData struct {
	*testData
	entity           knowledge.Entity
	sourcePutID      string
	deletedID        string
	unmergedSourceID string
	unmergedTargetID string
	err              error
}

type testChronicles map[string]chronicle.Summary

func (r testChronicles) PutChronicle(_ context.Context, row chronicle.Summary) error {
	r[row.HourID] = row
	return nil
}
func (r testChronicles) GetChronicle(_ context.Context, id string) (chronicle.Summary, bool, error) {
	row, ok := r[id]
	return row, ok, nil
}
func (r testChronicles) ListChronicles(context.Context, time.Time, time.Time, string, int) ([]chronicle.Summary, string, error) {
	return nil, "", nil
}
func (r testChronicles) DeleteChronicle(_ context.Context, id string) error {
	delete(r, id)
	return nil
}
func (r testChronicles) DeleteAllChronicles(context.Context) error { clear(r); return nil }

type pagedTestChronicles struct{ rows []chronicle.Summary }

func (r pagedTestChronicles) PutChronicle(context.Context, chronicle.Summary) error { return nil }
func (r pagedTestChronicles) GetChronicle(context.Context, string) (chronicle.Summary, bool, error) {
	return chronicle.Summary{}, false, nil
}
func (r pagedTestChronicles) ListChronicles(_ context.Context, start, end time.Time, after string, limit int) ([]chronicle.Summary, string, error) {
	var rows []chronicle.Summary
	for _, row := range r.rows {
		if row.Start.Before(start) || !row.Start.Before(end) || after != "" && row.HourID <= after {
			continue
		}
		rows = append(rows, row)
	}
	if len(rows) > limit {
		return rows[:limit], rows[limit-1].HourID, nil
	}
	return rows, "", nil
}
func (r pagedTestChronicles) DeleteChronicle(context.Context, string) error { return nil }
func (r pagedTestChronicles) DeleteAllChronicles(context.Context) error     { return nil }

type evidenceBudgetProbe struct{}

func (evidenceBudgetProbe) CountInputTokens(_ context.Context, evidence []derive.Evidence) (int, error) {
	count := 500
	for _, item := range evidence {
		count += len(item.Text) + len(item.SourceID)
	}
	return count, nil
}
func (evidenceBudgetProbe) Generate(context.Context, []derive.Evidence) ([]derive.Proposal, error) {
	return nil, nil
}

func (d *testKnowledgeData) PutKnowledgeEntity(_ context.Context, entity knowledge.Entity) error {
	d.entity = knowledge.Clone(entity)
	return d.err
}
func (d *testKnowledgeData) PutKnowledgeEntityFromSource(_ context.Context, entity knowledge.Entity, sourceID string) error {
	d.entity, d.sourcePutID = knowledge.Clone(entity), sourceID
	return d.err
}

func TestHandleMCPReadPagesLibrarySubjectDetails(t *testing.T) {
	ctx := context.Background()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "mcp-wiki.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	}()
	entity := knowledge.Entity{ID: "project-northstar", Kind: knowledge.Project, Name: "Northstar", State: knowledge.Confirmed,
		Aliases: []string{"North Star"},
		Facts: []knowledge.Fact{
			{ID: "fact-one", Text: "The project uses Go.", State: knowledge.Confirmed, Sources: []knowledge.Source{{UserAuthored: true}}},
			{ID: "fact-two", Text: "Mira owns rollback decisions.", State: knowledge.Confirmed, Sources: []knowledge.Source{{UserAuthored: true}}},
		},
		OpenLoops: []knowledge.OpenLoop{{ID: "loop-one", Text: "Confirm launch date.", State: knowledge.Proposed, Sources: []knowledge.Source{{UserAuthored: true}}}},
	}
	if err := store.PutKnowledgeEntity(ctx, entity); err != nil {
		t.Fatal(err)
	}
	client := NewClient(store)
	defer client.Close()
	wire, err := client.HandleMCPRead(ctx, localapi.RPCGetWikiPage, json.RawMessage(`{"id":"project-northstar","offset":1,"limit":1}`))
	if err != nil {
		t.Fatal(err)
	}
	var output struct {
		Found  bool `json:"found"`
		Entity struct {
			Name    string   `json:"name"`
			Aliases []string `json:"aliases"`
		} `json:"entity"`
		Details []struct {
			Type    string             `json:"type"`
			Text    string             `json:"text"`
			Sources []knowledge.Source `json:"sources"`
		} `json:"details"`
		NextOffset int  `json:"next_offset"`
		HasMore    bool `json:"has_more"`
	}
	if json.Unmarshal(wire, &output) != nil || !output.Found || output.Entity.Name != "Northstar" || len(output.Entity.Aliases) != 1 || len(output.Details) != 1 || output.Details[0].Type != "fact" || output.Details[0].Text != "Mira owns rollback decisions." || len(output.Details[0].Sources) != 1 || !output.Details[0].Sources[0].UserAuthored || output.NextOffset != 2 || !output.HasMore {
		t.Fatalf("Library page = %s", wire)
	}
	if _, err := client.HandleMCPRead(ctx, localapi.RPCGetWikiPage, json.RawMessage(`{"id":"bad id","offset":0,"limit":1}`)); err == nil {
		t.Fatal("invalid Library subject ID was accepted")
	}
}

func TestSuggestKnowledgeFromChronicleRequiresAnExplicitLocalProvider(t *testing.T) {
	source, _ := capture.SanitizeObservation("com.example.editor", "local", "The Northstar project uses Go.", 4096)
	data := &testData{observations: map[string]capture.SanitizedObservation{source.ContentID(): source}}
	client := NewClient(data)
	client.chronicles = testChronicles{"hour:test": {HourID: "hour:test", SourceIDs: []string{source.ContentID()}}}
	if _, err := client.SuggestKnowledgeFromChronicle(context.Background(), "hour:test"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("suggest without local model = %v, want unavailable", err)
	}
}

func TestSuggestKnowledgeFromChronicleNeverRoutesToRemoteNotesProvider(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	service := notes.NewService(nil, nil, time.Now)
	extractor, err := notes.NewOllamaExtractor(server.URL, "qwen3:4b")
	if err != nil {
		t.Fatal(err)
	}
	service.SetExtractor(extractor)
	controller := &localAIController{service: service, settings: LocalAISettings{Enabled: true, Provider: ProviderOpenAICompatible, Endpoint: server.URL, Model: "remote-model"}}
	client := NewClient(&testData{}).WithLocalAI(controller)
	if client.CanSuggestKnowledge() {
		t.Fatal("remote note provider enabled captured-evidence suggestions")
	}
	if _, err := client.SuggestKnowledgeFromChronicle(context.Background(), "hour:20261106T13Z"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("remote provider suggestion error = %v, want unavailable", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("remote provider received %d requests", requests.Load())
	}
}

func TestSuggestKnowledgeFromChronicleRejectsDeletedRecapSourceBeforeModelCall(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	service := notes.NewService(nil, nil, time.Now)
	extractor, err := notes.NewOllamaExtractor(server.URL, "qwen3:4b")
	if err != nil {
		t.Fatal(err)
	}
	service.SetExtractor(extractor)
	controller := &localAIController{service: service, settings: LocalAISettings{Enabled: true, Provider: ProviderOllama, Endpoint: server.URL, Model: "qwen3:4b"}}
	client := NewClient(&testData{}).WithLocalAI(controller)
	hourID, sourceID := "hour:20261106T13Z", strings.Repeat("a", 64)
	client.chronicles = testChronicles{hourID: {HourID: hourID, SourceIDs: []string{sourceID}}}
	if _, err := client.SuggestKnowledgeFromChronicle(context.Background(), hourID); err == nil {
		t.Fatal("missing recap source was sent to the model")
	}
	if requests.Load() != 0 {
		t.Fatalf("model was called %d times for missing source", requests.Load())
	}
}

func TestFitKnowledgeEvidenceHonorsProviderTokenBudget(t *testing.T) {
	evidence := []derive.Evidence{
		{SourceID: strings.Repeat("a", 64), Text: strings.Repeat("a", 4000)},
		{SourceID: strings.Repeat("b", 64), Text: strings.Repeat("b", 4000)},
	}
	got, err := fitKnowledgeEvidence(context.Background(), evidenceBudgetProbe{}, evidence)
	if err != nil || len(got) != 1 || got[0].SourceID != evidence[0].SourceID {
		t.Fatalf("fitKnowledgeEvidence() = %d sources, %v; want only the first under-budget source", len(got), err)
	}
}

func TestSuggestKnowledgeFromChronicleUsesOnlySelectedLocalRecapSourcesOnDemand(t *testing.T) {
	selected, err := capture.SanitizeObservation("com.example.editor", "local", "Northstar is written in Go.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	other, err := capture.SanitizeObservation("com.example.chat", "local", "Secret unrelated conversation.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	hourID := "hour:20261106T13Z"
	var requests atomic.Int32
	server := httptest.NewServer(modeltest.LocalOllama("qwen3:4b", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var body struct {
			Messages []struct{ Role, Content string } `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Messages) != 2 || !strings.Contains(body.Messages[1].Content, selected.Text()) || strings.Contains(body.Messages[1].Content, other.Text()) {
			t.Errorf("model received evidence outside selected recap: %+v", body.Messages)
		}
		proposal := `{"suggestions":[{"kind":"project","name":"Northstar","fact":"The project is written in Go.","source_id":"e1"}]}`
		_, _ = fmt.Fprintf(w, `{"message":{"content":%q},"done":true,"prompt_eval_count":80,"eval_count":24}`, proposal)
	})))
	defer server.Close()
	service := notes.NewService(nil, nil, time.Now)
	extractor, err := notes.NewOllamaExtractor(server.URL, "qwen3:4b")
	if err != nil {
		t.Fatal(err)
	}
	service.SetExtractor(extractor)
	localAI := &localAIController{service: service, settings: LocalAISettings{Enabled: true, Provider: ProviderOllama, Endpoint: server.URL, Model: "qwen3:4b"}}
	data := &testData{observations: map[string]capture.SanitizedObservation{selected.ContentID(): selected, other.ContentID(): other}}
	client := NewClient(data).WithLocalAI(localAI)
	client.chronicles = testChronicles{hourID: {HourID: hourID, SourceIDs: []string{selected.ContentID()}}}
	if !client.CanSuggestKnowledge() || requests.Load() != 0 {
		t.Fatalf("capability check sent request: available=%t requests=%d", client.CanSuggestKnowledge(), requests.Load())
	}
	proposals, err := client.SuggestKnowledgeFromChronicle(context.Background(), hourID)
	if err != nil || len(proposals) != 1 || proposals[0].SourceID != selected.ContentID() || requests.Load() != 1 {
		t.Fatalf("SuggestKnowledgeFromChronicle() = %+v, %v, requests=%d", proposals, err, requests.Load())
	}
}

func TestAcceptKnowledgeSuggestionRequiresSourceInSameRecap(t *testing.T) {
	selected, _ := capture.SanitizeObservation("com.example.editor", "local", "Northstar is written in Go.", 4096)
	other, _ := capture.SanitizeObservation("com.example.chat", "local", "Another recap source.", 4096)
	hourID := "hour:20261106T13Z"
	data := &testKnowledgeData{testData: &testData{observations: map[string]capture.SanitizedObservation{selected.ContentID(): selected, other.ContentID(): other}}}
	client := NewClient(data)
	client.chronicles = testChronicles{hourID: {HourID: hourID, SourceIDs: []string{selected.ContentID()}}}
	proposal := derive.Proposal{Kind: knowledge.Project, Name: "Northstar", Fact: "The project is written in Go.", SourceID: other.ContentID()}
	if _, err := client.AcceptKnowledgeSuggestion(context.Background(), hourID, proposal); err == nil {
		t.Fatal("proposal citing another recap was accepted")
	}
	if data.entity.ID != "" {
		t.Fatal("invalid proposal was persisted")
	}
	proposal.SourceID = selected.ContentID()
	entity, err := client.AcceptKnowledgeSuggestion(context.Background(), hourID, proposal)
	if err != nil || entity.State != knowledge.Confirmed || len(entity.Facts) != 1 || entity.Facts[0].Sources[0].ID != selected.ContentID() || data.sourcePutID != selected.ContentID() {
		t.Fatalf("AcceptKnowledgeSuggestion() = %+v, %v; stored source=%q", entity, err, data.sourcePutID)
	}
}

func (d *testKnowledgeData) DeleteKnowledgeEntity(_ context.Context, id string) error {
	d.deletedID = id
	return d.err
}

func (d *testKnowledgeData) UnmergeKnowledge(_ context.Context, sourceID, targetID string) error {
	d.unmergedSourceID, d.unmergedTargetID = sourceID, targetID
	return d.err
}

type testDeleter struct {
	id    string
	calls int
	err   error
}

func (d *testDeleter) DeleteSource(_ context.Context, id string) (int, error) {
	d.id, d.calls = id, d.calls+1
	return 1, d.err
}

type testAllDeleter struct {
	calls  int
	count  int
	gotCtx context.Context
}

func (d *testAllDeleter) DeleteSource(context.Context, string) (int, error) { return 0, nil }
func (d *testAllDeleter) DeleteAll(ctx context.Context) (int, error) {
	d.calls++
	d.gotCtx = ctx
	return d.count, nil
}

func TestDeleteAllRequiresConfiguredBulkDeleterAndContext(t *testing.T) {
	if NewClient(&testData{}).CanDeleteAll() {
		t.Fatal("client without persistent deletion should not expose delete-all")
	}
	deleter := &testAllDeleter{count: 12}
	client := NewClient(&testData{}).WithDeletion(deleter)
	if !client.CanDeleteAll() {
		t.Fatal("client with persistent bulk deleter should expose delete-all")
	}
	ctx := context.Background()
	if count, err := client.DeleteAll(ctx); err != nil || count != 12 || deleter.calls != 1 || deleter.gotCtx != ctx {
		t.Fatalf("DeleteAll() = (%d, %v), calls=%d, context=%v; want (12, nil), 1, same context", count, err, deleter.calls, deleter.gotCtx)
	}
	if _, err := client.DeleteAll(nil); err == nil { //nolint:staticcheck // Verify that the API rejects a nil context.
		t.Fatal("DeleteAll(nil) unexpectedly succeeded")
	}
	if deleter.calls != 1 {
		t.Fatalf("invalid context reached deleter: calls=%d", deleter.calls)
	}
}

func (d *testData) ConfirmProposal(_ context.Context, date, id string) error {
	d.confirmedDate, d.confirmedID = date, id
	return nil
}
func (d *testData) AnswerQuestion(_ context.Context, _ string, id, answer string, _ time.Time) error {
	d.answeredID, d.answer = id, answer
	return nil
}
func (d *testData) DismissQuestion(_ context.Context, _ string, id string) error {
	d.dismissedID = id
	return nil
}

func (d *testData) GetTodaySnapshot(_ context.Context, _ string) (today.Snapshot, bool, error) {
	if d.savedTodaySet {
		return d.savedToday.Clone(), true, nil
	}
	return d.today, d.found, nil
}
func (d *testData) PutTodaySnapshot(_ context.Context, snapshot today.Snapshot) error {
	d.savedToday, d.savedTodaySet = snapshot.Clone(), true
	d.todayWrites++
	return nil
}
func (d *testData) SearchExact(_ context.Context, q search.ExactQuery) (search.ExactPage, error) {
	d.searchCalls++
	d.query = q
	d.queries = append(d.queries, q)
	page := d.page
	if d.searchCalls <= len(d.pages) {
		page = d.pages[d.searchCalls-1]
	}
	filtered := make([]search.IndexHit, 0, len(page.Hits))
	for _, hit := range page.Hits {
		observation := d.observations[hit.SourceID]
		excluded := false
		for _, app := range q.ExcludeAppIDs {
			if strings.EqualFold(observation.AppID(), app) {
				excluded = true
			}
		}
		if excluded {
			continue
		}
		if !q.CapturedStart.IsZero() {
			at := d.observationAt[hit.SourceID]
			inScope := !at.IsZero() && !at.Before(q.CapturedStart) && at.Before(q.CapturedEnd)
			for _, id := range q.ScopeSourceIDs {
				inScope = inScope || id == hit.SourceID
			}
			if !inScope {
				continue
			}
			hit.CapturedAt = at
		}
		filtered = append(filtered, hit)
	}
	page.Hits = filtered
	return page, nil
}
func (d *testData) ListKnowledge(_ context.Context, _ knowledge.PageRequest) (knowledge.Page, error) {
	return d.knowledge, nil
}
func (d *testData) GetObservation(_ context.Context, id string) (capture.SanitizedObservation, bool, error) {
	d.readSourceIDs = append(d.readSourceIDs, id)
	o, ok := d.observations[id]
	return o, ok, nil
}

func (d *testData) ListObservationWindow(_ context.Context, request storage.ObservationWindowRequest) (storage.ObservationWindow, error) {
	d.observationWindowRequests = append(d.observationWindowRequests, request)
	if !storage.ValidObservationWindow(request) {
		return storage.ObservationWindow{}, &storage.StoreError{Code: storage.Invalid}
	}
	type candidate struct {
		id string
		at time.Time
	}
	var candidates []candidate
	for id := range d.observations {
		at := d.observationAt[id]
		if at.IsZero() || at.Before(request.Start) || !at.Before(request.End) {
			continue
		}
		observation := d.observations[id]
		if request.AppID != "" && observation.AppID() != request.AppID || request.Domain != "" && observation.Domain() != request.Domain {
			continue
		}
		excluded := false
		for _, app := range request.ExcludeAppIDs {
			excluded = excluded || observation.AppID() == app
		}
		if excluded {
			continue
		}
		if !request.BeforeCapturedAt.IsZero() && (!at.Before(request.BeforeCapturedAt) && (!at.Equal(request.BeforeCapturedAt) || id >= request.BeforeSourceID)) {
			continue
		}
		candidates = append(candidates, candidate{id: id, at: at})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if !candidates[i].at.Equal(candidates[j].at) {
			return candidates[i].at.After(candidates[j].at)
		}
		return candidates[i].id > candidates[j].id
	})
	page := storage.ObservationWindow{Observations: make([]capture.SanitizedObservation, 0, request.Limit), CapturedAt: make([]time.Time, 0, request.Limit)}
	for _, item := range candidates {
		if len(page.Observations) >= request.Limit {
			page.HasMore = true
			break
		}
		observation := d.observations[item.id]
		used := len(observation.Text()) + len(observation.AppID()) + len(observation.Domain())
		if used > request.MaxBytes-page.Bytes {
			page.HasMore = true
			break
		}
		page.Observations = append(page.Observations, observation)
		page.CapturedAt = append(page.CapturedAt, item.at)
		page.Bytes += used
		page.NextCapturedAt, page.NextSourceID = item.at, item.id
	}
	return page, nil
}

func (d *testData) ListActivitySegmentsForDates(_ context.Context, dates []string, limit int) ([]activity.Segment, error) {
	d.activityDates = append(d.activityDates, dates...)
	wanted := make(map[string]struct{}, len(dates))
	for _, date := range dates {
		wanted[date] = struct{}{}
	}
	filtered := make([]activity.Segment, 0)
	for _, segment := range d.activity {
		if _, ok := wanted[segment.WallStart.UTC().Format("2006-01-02")]; ok {
			filtered = append(filtered, segment)
		}
	}
	if len(filtered) > limit {
		return nil, ErrActivityLimit
	}
	return filtered, nil
}

func TestActivityReportUsesBoundedUTCIndexAndLocalCalendar(t *testing.T) {
	zone, err := time.LoadLocation("Europe/Brussels")
	if err != nil {
		t.Fatal(err)
	}
	segment := activity.Segment{ID: "segment-1", WallStart: time.Date(2026, 11, 4, 22, 30, 0, 0, time.UTC), WallEnd: time.Date(2026, 11, 4, 23, 30, 0, 0, time.UTC), Duration: int64(time.Hour), AppID: "com.example.editor"}
	d := &testData{activity: []activity.Segment{segment}}
	got, err := NewClient(d).ActivityReport("2026-11-05", "2026-11-05", zone.String())
	if err != nil {
		t.Fatal(err)
	}
	if got.Eligible != int64(30*time.Minute) || len(got.Days) != 1 || got.Days[0].Date != "2026-11-05" || got.Days[0].Duration != int64(30*time.Minute) {
		t.Fatalf("local-day report = %+v", got)
	}
	if len(d.activityDates) == 0 || d.activityDates[0] != "2026-11-04" {
		t.Fatalf("report did not query the overlapping UTC partition: %v", d.activityDates)
	}
}

func TestActivityReportRejectsInvalidOrOverlongRanges(t *testing.T) {
	client := NewClient(&testData{})
	for _, input := range [][3]string{{"bad", "2026-11-05", "UTC"}, {"2026-11-06", "2026-11-05", "UTC"}, {"2026-01-01", "2026-02-01", "UTC"}, {"2026-11-05", "2026-11-05", "No/Such_Zone"}} {
		if _, err := client.ActivityReport(input[0], input[1], input[2]); err == nil {
			t.Fatalf("accepted range %q..%q zone %q", input[0], input[1], input[2])
		}
	}
}

func TestTodayReturnsExplicitEmptyStateWhenNoSnapshotExists(t *testing.T) {
	client := NewClient(nil)
	got, err := client.Today("2026-11-05")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "empty" || got.Date != "2026-11-05" || got.CaptureState != "not_configured" {
		t.Fatalf("unexpected empty state: %+v", got)
	}
}

func TestTodayShowsRecentEncryptedCapturesWithoutMaterializedRecap(t *testing.T) {
	zone, err := time.LoadLocation(localZoneName())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().In(zone)
	date := now.Format("2006-01-02")
	at := time.Date(now.Year(), now.Month(), now.Day(), 10, 15, 0, 0, zone)
	first, err := capture.SanitizeObservation("com.example.editor", "", "A saved paragraph about the deployment plan.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	second, err := capture.SanitizeObservation("com.hazcod.blur", "", "A previous Ask query from Blur.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	d := &testData{
		observations:  map[string]capture.SanitizedObservation{first.ContentID(): first, second.ContentID(): second},
		observationAt: map[string]time.Time{first.ContentID(): at, second.ContentID(): at.Add(time.Minute)},
	}
	got, err := NewClient(d).Today(date)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "ready" || len(got.RecentCaptures) != 1 {
		t.Fatalf("Today without a materialized recap = state %q, captures %+v", got.State, got.RecentCaptures)
	}
	item := got.RecentCaptures[0]
	if item.SourceID != first.ContentID() || item.AppID != "com.example.editor" || item.Text != first.Text() || !item.CapturedAt.Equal(at) {
		t.Fatalf("recent capture = %+v", item)
	}
}

func TestTodayMaterializesBoundedLocalActivityAndPreservesNoteTasks(t *testing.T) {
	zone, err := time.LoadLocation("Europe/Brussels")
	if err != nil {
		t.Fatal(err)
	}
	date := "2026-11-05"
	generated := time.Date(2026, 11, 5, 12, 0, 0, 0, zone)
	base, err := today.Build(today.Input{Date: date, Zone: zone.String(), GeneratedAt: generated, GenerationID: "note-generation", Evidence: []today.Evidence{{
		SourceID: "note-source", At: generated, Change: "what is 2+2", ChangeOrigin: today.ChangeOriginUser, Todo: "Send the plan", TodoMarker: today.MarkerUserCreated,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(base.RecentChanges) != 1 || base.RecentChanges[0].Origin != today.ChangeOriginUser {
		t.Fatalf("test note fixture lost user provenance: %+v", base.RecentChanges)
	}
	segments := []activity.Segment{
		{ID: "segment-before-midnight", WallStart: time.Date(2026, 11, 4, 22, 30, 0, 0, time.UTC), WallEnd: time.Date(2026, 11, 4, 23, 30, 0, 0, time.UTC), Duration: int64(time.Hour), AppID: "com.example.slack", SourceIDs: []string{"capture-slack"}},
		{ID: "segment-other-app", WallStart: time.Date(2026, 11, 5, 1, 0, 0, 0, time.UTC), WallEnd: time.Date(2026, 11, 5, 1, 20, 0, 0, time.UTC), Duration: int64(20 * time.Minute), AppID: "com.example.editor", SourceIDs: []string{"capture-editor"}},
	}
	d := &testData{today: base, found: true, activity: segments}
	got, err := NewClient(d).Today(date)
	if err != nil {
		t.Fatal(err)
	}
	if !d.savedTodaySet {
		t.Fatal("activity recap was not materialized into encrypted Today storage")
	}
	if len(got.Snapshot.RecentChanges) != 3 {
		t.Fatalf("first materialization has duplicate or missing note/activity changes: %+v", got.Snapshot.RecentChanges)
	}
	for _, highlight := range previewChangeTexts(got.Snapshot.RecentChanges) {
		if strings.Contains(highlight, "2+2") {
			t.Fatalf("Highlights included the user's prompt: %+v", highlight)
		}
	}
	if len(got.Snapshot.ActiveSubjects) != 2 || len(got.Snapshot.TimeSplit) != 2 {
		t.Fatalf("activity subjects/splits = %d/%d, want 2/2", len(got.Snapshot.ActiveSubjects), len(got.Snapshot.TimeSplit))
	}
	var slackDuration int64
	for _, split := range got.Snapshot.TimeSplit {
		if split.Label == "Slack" {
			slackDuration = int64(split.Duration)
		}
	}
	if slackDuration != int64(30*time.Minute) {
		t.Fatalf("cross-midnight Slack duration = %s, want 30m", time.Duration(slackDuration))
	}
	if len(got.Snapshot.TodoCandidates) != 1 || got.Snapshot.TodoCandidates[0].Text != "Send the plan" {
		t.Fatalf("activity refresh lost existing note todo: %+v", got.Snapshot.TodoCandidates)
	}
	if !containsString(got.Snapshot.SourceIDs, "capture-slack") || !containsString(got.Snapshot.SourceIDs, "note-source") {
		t.Fatalf("recap source links missing: %v", got.Snapshot.SourceIDs)
	}
	if len(d.activityDates) == 0 || d.activityDates[0] != "2026-11-04" {
		t.Fatalf("local calendar read omitted prior UTC partition: %v", d.activityDates)
	}
	_, err = NewClient(d).Today(date)
	if err != nil || d.todayWrites != 1 || len(d.savedToday.RecentChanges) != 3 {
		t.Fatalf("repeated Today read was not idempotent: writes=%d changes=%d snapshot=%+v err=%v", d.todayWrites, len(d.savedToday.RecentChanges), d.savedToday.RecentChanges, err)
	}
}

func TestDisplayAppNameUsesFamiliarNamesAndStableFallback(t *testing.T) {
	for appID, want := range map[string]string{
		"com.tinyspeck.slackmacgap": "Slack",
		"com.apple.Safari":          "Safari",
		"com.example.editor":        "Editor",
	} {
		if got := displayAppName(appID); got != want {
			t.Errorf("displayAppName(%q) = %q, want %q", appID, got, want)
		}
	}
}

func TestTodayOmitsAggregatesWhoseDeletionProvenanceExceedsBound(t *testing.T) {
	zone := time.UTC
	date := "2026-11-05"
	start := time.Date(2026, 11, 5, 9, 0, 0, 0, zone)
	segments := make([]activity.Segment, 513)
	for i := range segments {
		id := fmt.Sprintf("source-%04d", i)
		segments[i] = activity.Segment{
			ID: fmt.Sprintf("segment-%04d", i), WallStart: start.Add(time.Duration(i) * time.Minute),
			WallEnd: start.Add(time.Duration(i+1) * time.Minute), Duration: int64(time.Minute),
			AppID: "com.example.editor", SourceIDs: []string{id},
		}
	}
	d := &testData{activity: segments}
	got, err := NewClient(d).Today(date)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Snapshot.SourceIDs) > today.MaxSources || len(got.Snapshot.TimeSplit) != 0 {
		t.Fatalf("oversized provenance was retained: sources=%d splits=%+v", len(got.Snapshot.SourceIDs), got.Snapshot.TimeSplit)
	}
	indexed := make(map[string]bool, len(got.Snapshot.SourceIDs))
	for _, id := range got.Snapshot.SourceIDs {
		indexed[id] = true
	}
	for _, item := range got.Snapshot.RecentChanges {
		for _, id := range item.SourceIDs {
			if !indexed[id] {
				t.Fatalf("change has source %q absent from deletion index", id)
			}
		}
	}
}

func TestTodayDoesNotClassifyUserChangesByDisplayText(t *testing.T) {
	date := "2026-11-05"
	generated := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	base, err := today.Build(today.Input{Date: date, Zone: "UTC", GeneratedAt: generated, GenerationID: "note-generation", Evidence: []today.Evidence{{
		SourceID: "note-source", At: generated, Change: "Active in the plan: reviewed with the team", ChangeOrigin: today.ChangeOriginUser, Todo: "Send the plan", TodoMarker: today.MarkerUserCreated,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	segment := activity.Segment{ID: "segment-one", WallStart: generated.Add(-time.Hour), WallEnd: generated, Duration: int64(time.Hour), AppID: "com.example.editor", SourceIDs: []string{"activity-source"}}
	d := &testData{today: base, found: true, activity: []activity.Segment{segment}}
	client := NewClient(d)
	if _, err := client.Today(date); err != nil {
		t.Fatal(err)
	}
	// Force a new activity generation, as would happen when another segment is
	// captured later in the day.
	segment.ID = "segment-two"
	d.activity = []activity.Segment{segment}
	got, err := client.Today(date)
	if err != nil {
		t.Fatal(err)
	}
	foundNoteChange := false
	for _, item := range got.Snapshot.RecentChanges {
		if item.Text == "Active in the plan: reviewed with the team" && containsString(item.SourceIDs, "note-source") && item.Origin == today.ChangeOriginUser {
			foundNoteChange = true
		}
	}
	if !foundNoteChange || !containsString(got.Snapshot.SourceIDs, "note-source") || len(got.Snapshot.TodoCandidates) != 1 {
		t.Fatalf("display-text collision discarded user data: %+v", got.Snapshot)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestSearchBoundsQueryAndPage(t *testing.T) {
	d := &testData{page: search.ExactPage{Hits: []search.IndexHit{{SourceID: "source"}}}}
	client := NewClient(d)
	if _, err := client.Search(string(make([]byte, MaxQueryBytes+1))); err == nil {
		t.Fatal("oversized query accepted")
	}
	if _, err := client.Search("  standup  "); err != nil {
		t.Fatal(err)
	}
	if len(d.query.Terms) != 1 || d.query.Terms[0] != "standup" || d.query.TopK != MaxSearchResults || d.query.MaxRows != MaxSearchResults {
		t.Fatalf("unbounded/unclean query: %+v", d.query)
	}
}

func TestSearchMemoryHydratesOnlyBoundedExactHits(t *testing.T) {
	first, _ := capture.SanitizeObservation("com.example.editor", "local", "review the launch checklist", 4096)
	second, _ := capture.SanitizeObservation("com.example.chat", "local", "launch notes", 4096)
	d := &testData{page: search.ExactPage{Hits: []search.IndexHit{{SourceID: first.ContentID(), Bytes: len(first.Text()) + len(first.AppID()) + len(first.Domain())}, {SourceID: second.ContentID(), Bytes: len(second.Text()) + len(second.AppID()) + len(second.Domain())}}}, observations: map[string]capture.SanitizedObservation{first.ContentID(): first, second.ContentID(): second}}
	results, err := NewClient(d).SearchMemory("launch")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Text != first.Text() || results[0].AppID != "com.example.editor" {
		t.Fatalf("exact sources not hydrated: %+v", results)
	}
}

func TestHandleMCPReadReturnsOnlyBoundedSanitizedMemory(t *testing.T) {
	largeText := strings.Repeat("launch detail ", 3500)
	first, err := capture.SanitizeObservation("com.example.editor", "local", largeText, capture.MaxObservationTextBytes)
	if err != nil {
		t.Fatal(err)
	}
	second, err := capture.SanitizeObservation("com.example.chat", "local", strings.Repeat("another launch detail ", 900), capture.MaxObservationTextBytes)
	if err != nil {
		t.Fatal(err)
	}
	indexBytes := func(o capture.SanitizedObservation) int { return len(o.Text()) + len(o.AppID()) + len(o.Domain()) }
	data := &testData{
		page:         search.ExactPage{Hits: []search.IndexHit{{SourceID: first.ContentID(), Bytes: indexBytes(first)}, {SourceID: second.ContentID(), Bytes: indexBytes(second)}}, HasMore: true},
		observations: map[string]capture.SanitizedObservation{first.ContentID(): first, second.ContentID(): second},
	}
	params, _ := json.Marshal(struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}{"launch", 2})
	got, err := NewClient(data).HandleMCPRead(context.Background(), "search_memory", params)
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Results   []SearchHit `json:"results"`
		Truncated bool        `json:"truncated"`
	}
	if err := json.Unmarshal(got, &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 2 || len(response.Results[0].Text) > 2048 || !response.Results[0].Truncated || !response.Truncated {
		t.Fatalf("MCP response was not bounded/truncated: count=%d textBytes=%d itemTruncated=%t responseTruncated=%t", len(response.Results), len(response.Results[0].Text), response.Results[0].Truncated, response.Truncated)
	}
	if data.query.TopK != 3 || data.query.MaxRows != 3 || len(data.readSourceIDs) != 2 || data.readSourceIDs[0] != first.ContentID() || data.readSourceIDs[1] != second.ContentID() {
		t.Fatalf("MCP read escaped candidate/source budget: query=%+v hydrated=%v", data.query, data.readSourceIDs)
	}
}

func TestHandleMCPReadReturnsPaginatedFullCaptureText(t *testing.T) {
	observation, err := capture.SanitizeObservation("com.example.editor", "local", "alpha beta gamma delta", 4096)
	if err != nil {
		t.Fatal(err)
	}
	data := &testData{observations: map[string]capture.SanitizedObservation{observation.ContentID(): observation}}
	client := NewClient(data)
	first, err := client.HandleMCPRead(context.Background(), localapi.RPCGetSnapshots, json.RawMessage(`{"source_ids":["`+observation.ContentID()+`"],"max_bytes_per_source":8}`))
	if err != nil {
		t.Fatal(err)
	}
	var page mcpSnapshotResponse
	if err := json.Unmarshal(first, &page); err != nil || len(page.Snapshots) != 1 {
		t.Fatalf("first snapshot page = %s, err=%v", first, err)
	}
	chunk := page.Snapshots[0]
	if chunk.Content != "alpha be" || chunk.OffsetBytes != 0 || chunk.NextOffsetBytes != 8 || chunk.Complete {
		t.Fatalf("first snapshot chunk = %+v", chunk)
	}
	second, err := client.HandleMCPRead(context.Background(), localapi.RPCGetSnapshots, json.RawMessage(`{"source_ids":["`+observation.ContentID()+`"],"offsets":{"`+observation.ContentID()+`":8},"max_bytes_per_source":8}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(second, &page); err != nil || len(page.Snapshots) != 1 || page.Snapshots[0].Content != "ta gamma" || page.Snapshots[0].NextOffsetBytes != 16 || page.Snapshots[0].Complete {
		t.Fatalf("second snapshot page = %s, err=%v", second, err)
	}
	last, err := client.HandleMCPRead(context.Background(), localapi.RPCGetSnapshots, json.RawMessage(`{"source_ids":["`+observation.ContentID()+`"],"offsets":{"`+observation.ContentID()+`":16},"max_bytes_per_source":8}`))
	if err != nil || json.Unmarshal(last, &page) != nil || len(page.Snapshots) != 1 || page.Snapshots[0].Content != " delta" || !page.Snapshots[0].Complete {
		t.Fatalf("final snapshot page = %s, err=%v", last, err)
	}
	if len(data.readSourceIDs) != 3 {
		t.Fatalf("source reads = %v, want one per page", data.readSourceIDs)
	}
}

func TestHandleMCPReadRejectsInvalidSnapshotOffsetsAndIDs(t *testing.T) {
	client := NewClient(&testData{})
	for _, raw := range []string{
		`{"source_ids":["not-a-source"]}`,
		`{"source_ids":["aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"],"offsets":{"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb":1}}`,
		`{"source_ids":["aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"],"max_bytes_per_source":3}`,
	} {
		if _, err := client.HandleMCPRead(context.Background(), localapi.RPCGetSnapshots, json.RawMessage(raw)); err == nil {
			t.Errorf("accepted invalid snapshot request %s", raw)
		}
	}
}

func TestHandleMCPReadReturnsSourceLinkedChronicle(t *testing.T) {
	start := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	const hourID = "hour:20261115T10Z"
	source, err := capture.SanitizeObservation("com.example.chat", "local", "Discussed the database migration rollout.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	summary := chronicle.Summary{HourID: hourID, Start: start, End: start.Add(time.Hour), Text: "Reviewed the database migration rollout.", SourceIDs: []string{source.ContentID()}}
	data := &testData{observations: map[string]capture.SanitizedObservation{source.ContentID(): source}}
	client := NewClient(data)
	client.chronicles = testChronicles{hourID: summary}
	wire, err := client.HandleMCPRead(context.Background(), localapi.RPCGetChronicle, json.RawMessage(`{"hour_id":"`+hourID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Found     bool      `json:"found"`
		HourID    string    `json:"hour_id"`
		Summary   string    `json:"summary"`
		SourceIDs []string  `json:"source_ids"`
		Start     time.Time `json:"start"`
		End       time.Time `json:"end"`
	}
	if err := json.Unmarshal(wire, &got); err != nil {
		t.Fatal(err)
	}
	if !got.Found || got.HourID != hourID || got.Summary != summary.Text || len(got.SourceIDs) != 1 || got.SourceIDs[0] != source.ContentID() || !got.Start.Equal(start) || !got.End.Equal(start.Add(time.Hour)) {
		t.Fatalf("chronicle response = %+v", got)
	}
}

func TestHandleMCPReadListsSourceLinkedChroniclesByRange(t *testing.T) {
	start := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	first, err := capture.SanitizeObservation("com.example.chat", "local", "Discussed the database migration rollout.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	second, err := capture.SanitizeObservation("com.example.editor", "local", "Updated the migration plan.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	rows := []chronicle.Summary{
		{HourID: "hour:20261115T10Z", Start: start, End: start.Add(time.Hour), Text: "Reviewed the database migration rollout.", SourceIDs: []string{first.ContentID()}},
		{HourID: "hour:20261115T11Z", Start: start.Add(time.Hour), End: start.Add(2 * time.Hour), Text: "Updated the migration plan.", SourceIDs: []string{second.ContentID()}},
	}
	client := NewClient(&testData{})
	client.chronicles = pagedTestChronicles{rows: rows}
	request := `{"start":"2026-11-15T10:00:00Z","end":"2026-11-15T13:00:00Z","limit":1}`
	wire, err := client.HandleMCPRead(context.Background(), localapi.RPCListChronicles, json.RawMessage(request))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Items []struct {
			HourID    string   `json:"hour_id"`
			Summary   string   `json:"summary"`
			SourceIDs []string `json:"source_ids"`
		} `json:"items"`
		Next      string `json:"next"`
		Truncated bool   `json:"truncated"`
	}
	if err := json.Unmarshal(wire, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 || got.Items[0].HourID != rows[0].HourID || got.Items[0].Summary != rows[0].Text || len(got.Items[0].SourceIDs) != 1 || got.Items[0].SourceIDs[0] != first.ContentID() || got.Next != rows[0].HourID || !got.Truncated {
		t.Fatalf("chronicle range response = %+v", got)
	}
	request = `{"start":"2026-11-15T10:00:00Z","end":"2026-11-15T13:00:00Z","after":"` + got.Next + `","limit":1}`
	wire, err = client.HandleMCPRead(context.Background(), localapi.RPCListChronicles, json.RawMessage(request))
	got = struct {
		Items []struct {
			HourID    string   `json:"hour_id"`
			Summary   string   `json:"summary"`
			SourceIDs []string `json:"source_ids"`
		} `json:"items"`
		Next      string `json:"next"`
		Truncated bool   `json:"truncated"`
	}{}
	if err != nil || json.Unmarshal(wire, &got) != nil || len(got.Items) != 1 || got.Items[0].HourID != rows[1].HourID || got.Next != "" || got.Truncated {
		t.Fatalf("chronicle continuation = %+v err=%v wire=%s", got, err, wire)
	}
}

func TestHandleMCPReadRejectsInvalidChronicleRangesAndEvidence(t *testing.T) {
	client := NewClient(&testData{})
	client.chronicles = pagedTestChronicles{}
	for _, raw := range []string{
		`{}`,
		`{"start":"2026-11-16T00:00:00Z","end":"2026-11-15T00:00:00Z","limit":1}`,
		`{"start":"2026-10-01T00:00:00Z","end":"2026-11-15T00:00:00Z","limit":1}`,
		`{"start":"2026-11-15T00:00:00Z","end":"2026-11-16T00:00:00Z","after":"not-an-hour","limit":1}`,
		`{"start":"2026-11-15T00:00:00Z","end":"2026-11-16T00:00:00Z","limit":9}`,
		`{"start":"2026-11-15T00:00:00Z","end":"2026-11-16T00:00:00Z","limit":1,"extra":true}`,
	} {
		if _, err := client.HandleMCPRead(context.Background(), localapi.RPCListChronicles, json.RawMessage(raw)); err == nil {
			t.Errorf("accepted invalid chronicle range %s", raw)
		}
	}
	start := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	client.chronicles = pagedTestChronicles{rows: []chronicle.Summary{{
		HourID: "hour:20261115T10Z", Start: start, End: start.Add(time.Hour), Text: "A recap with an invalid source reference.", SourceIDs: []string{"bad-source"},
	}}}
	if _, err := client.HandleMCPRead(context.Background(), localapi.RPCListChronicles, json.RawMessage(`{"start":"2026-11-15T10:00:00Z","end":"2026-11-15T12:00:00Z","limit":2}`)); err == nil {
		t.Fatal("accepted recap with invalid source provenance")
	}
}

func TestHandleMCPReadReturnsRecentWorkingMemoryWithVerifiedSources(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	recent, err := capture.SanitizeObservation("com.tinyspeck.slackmacgap", "", "#security-news\nCVE-2026-1234 affects Chrome; install the update.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	blurUI, err := capture.SanitizeObservation("com.hazcod.blur", "", "Blur settings are open.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	old, err := capture.SanitizeObservation("com.example.editor", "", "Yesterday's draft.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	data := &testData{
		observations:  map[string]capture.SanitizedObservation{recent.ContentID(): recent, blurUI.ContentID(): blurUI, old.ContentID(): old},
		observationAt: map[string]time.Time{recent.ContentID(): now.Add(-time.Hour), blurUI.ContentID(): now.Add(-time.Minute), old.ContentID(): now.Add(-8 * time.Hour)},
	}
	client := NewClient(data)
	client.askNow = func() time.Time { return now }
	wire, err := client.HandleMCPRead(context.Background(), localapi.RPCGetWorkingMemory, json.RawMessage(`{"hours":4,"limit":5}`))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Start string `json:"start"`
		End   string `json:"end"`
		Items []struct {
			SourceID   string    `json:"source_id"`
			AppID      string    `json:"app_id"`
			Content    string    `json:"content"`
			CapturedAt time.Time `json:"captured_at"`
		} `json:"items"`
		Truncated       bool `json:"truncated"`
		IndexingPending bool `json:"indexing_pending"`
	}
	if err := json.Unmarshal(wire, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 || got.Items[0].SourceID != recent.ContentID() || got.Items[0].AppID != recent.AppID() || !strings.Contains(got.Items[0].Content, "#security-news") || !got.Items[0].CapturedAt.Equal(now.Add(-time.Hour)) || got.End != now.Add(time.Nanosecond).Format(time.RFC3339Nano) {
		t.Fatalf("working memory = %+v", got)
	}
	if len(data.observationWindowRequests) != 1 {
		t.Fatalf("working memory made %d storage reads", len(data.observationWindowRequests))
	}
	request := data.observationWindowRequests[0]
	if !request.Start.Equal(now.Add(-4*time.Hour)) || !request.End.Equal(now.Add(time.Nanosecond)) || request.Limit != 5 || len(request.ExcludeAppIDs) != 1 || request.ExcludeAppIDs[0] != "com.hazcod.blur" {
		t.Fatalf("working memory scope = %+v", request)
	}
}

func TestHandleMCPReadBoundsWorkingMemoryRequest(t *testing.T) {
	client := NewClient(&testData{})
	for _, raw := range []string{`{}`, `{"hours":0}`, `{"hours":7}`, `{"hours":2,"limit":9}`, `{"hours":2,"unknown":true}`} {
		if _, err := client.HandleMCPRead(context.Background(), localapi.RPCGetWorkingMemory, json.RawMessage(raw)); err == nil {
			t.Errorf("accepted invalid working-memory request %s", raw)
		}
	}
}

func TestWorkingMemoryExcerptKeepsUTF8HeadAndTail(t *testing.T) {
	text := strings.Repeat("日本語の技術ページ。", 180) + "UNIQUE_END_OF_PAGE"
	excerpt, truncated := workingMemoryExcerpt(text)
	if !truncated || !utf8.ValidString(excerpt) || len(excerpt) > maxMCPWorkingExcerpt || !strings.HasPrefix(excerpt, "日本語") || !strings.Contains(excerpt, "middle omitted") || !strings.HasSuffix(excerpt, "UNIQUE_END_OF_PAGE") {
		t.Fatalf("working memory excerpt length=%d truncated=%t content=%q", len(excerpt), truncated, excerpt)
	}
}

func TestHandleMCPReadRejectsMalformedChronicleRequestAndEvidence(t *testing.T) {
	client := NewClient(&testData{})
	client.chronicles = testChronicles{}
	for _, raw := range []string{`{}`, `{"hour_id":"day:20261115"}`, `{"hour_id":"hour:20261115T10Z","other":true}`} {
		if _, err := client.HandleMCPRead(context.Background(), localapi.RPCGetChronicle, json.RawMessage(raw)); err == nil {
			t.Errorf("accepted chronicle request %s", raw)
		}
	}
	client.chronicles = testChronicles{"hour:20261115T10Z": {HourID: "hour:20261115T10Z", Start: time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC), End: time.Date(2026, 11, 15, 11, 0, 0, 0, time.UTC), Text: "unsupported", SourceIDs: []string{"not-a-source"}}}
	if _, err := client.HandleMCPRead(context.Background(), localapi.RPCGetChronicle, json.RawMessage(`{"hour_id":"hour:20261115T10Z"}`)); err == nil {
		t.Fatal("accepted chronicle with an invalid source reference")
	}
}

func TestHandleMCPReadReturnsRecentActivityWithCaptureSources(t *testing.T) {
	now := time.Date(2026, 11, 15, 14, 0, 0, 0, time.Local)
	sourceID := strings.Repeat("a", 64)
	olderStart := now.Add(-2 * time.Hour)
	newerStart := now.Add(-30 * time.Minute)
	segments := []activity.Segment{
		{ID: strings.Repeat("1", 32), WallStart: olderStart, WallEnd: olderStart.Add(20 * time.Minute), Duration: int64(20 * time.Minute), AppID: "com.example.editor", Topic: "Release notes", SourceIDs: []string{sourceID}},
		{ID: strings.Repeat("2", 32), WallStart: newerStart, WallEnd: newerStart.Add(10 * time.Minute), Duration: int64(10 * time.Minute), AppID: "com.example.chat", Topic: "Security news", SourceIDs: []string{sourceID}},
	}
	data := &testData{activity: segments}
	client := NewClient(data)
	client.askNow = func() time.Time { return now }
	wire, err := client.HandleMCPRead(context.Background(), localapi.RPCGetRecentActivity, json.RawMessage(`{"days_back":1,"limit":5}`))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Segments []struct {
			AppID     string    `json:"app_id"`
			Topic     string    `json:"topic"`
			Start     time.Time `json:"start"`
			SourceIDs []string  `json:"source_ids"`
		} `json:"segments"`
		Truncated bool `json:"truncated"`
	}
	if err := json.Unmarshal(wire, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Segments) != 2 || got.Segments[0].AppID != "com.example.chat" || got.Segments[0].Topic != "Security news" || !got.Segments[0].Start.Equal(newerStart) || len(got.Segments[0].SourceIDs) != 1 || got.Segments[1].AppID != "com.example.editor" || got.Truncated {
		t.Fatalf("recent activity = %+v", got)
	}
	if len(data.activityDates) == 0 {
		t.Fatal("recent activity did not query local activity partitions")
	}
}

func TestHandleMCPReadBoundsRecentActivityRequest(t *testing.T) {
	client := NewClient(&testData{})
	for _, raw := range []string{`{"days_back":8}`, `{"days_back":1,"limit":9}`, `{"days_back":1,"unknown":true}`} {
		if _, err := client.HandleMCPRead(context.Background(), localapi.RPCGetRecentActivity, json.RawMessage(raw)); err == nil {
			t.Errorf("accepted recent activity request %s", raw)
		}
	}
}

func TestHandleMCPReadSearchesEntityMemoryWithSourceLinks(t *testing.T) {
	const sourceID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	entity := knowledge.Entity{ID: "mira-person", Kind: knowledge.Person, Name: "Mira", Aliases: []string{"Mira L."}, State: knowledge.Confirmed, Facts: []knowledge.Fact{{ID: "rollback-owner", Text: "Mira owns rollback decisions.", State: knowledge.Confirmed, Sources: []knowledge.Source{{ID: sourceID}}}}}
	client := NewClient(&testData{knowledge: knowledge.Page{Entities: []knowledge.Entity{entity}}})
	wire, err := client.HandleMCPRead(context.Background(), localapi.RPCSearchWiki, json.RawMessage(`{"query":"Mira rollback","limit":3}`))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Entities  []knowledge.Entity `json:"entities"`
		Truncated bool               `json:"truncated"`
	}
	if err := json.Unmarshal(wire, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Entities) != 1 || got.Entities[0].Name != "Mira" || len(got.Entities[0].Facts) != 1 || got.Entities[0].Facts[0].Text != "Mira owns rollback decisions." || got.Entities[0].Facts[0].Sources[0].ID != sourceID || got.Truncated {
		t.Fatalf("Wiki search response = %+v", got)
	}
}

func TestHandleMCPReadBoundsWikiSearchRequest(t *testing.T) {
	client := NewClient(&testData{})
	for _, raw := range []string{`{"query":""}`, `{"query":"mira","limit":9}`, `{"query":"mira","unknown":true}`} {
		if _, err := client.HandleMCPRead(context.Background(), localapi.RPCSearchWiki, json.RawMessage(raw)); err == nil {
			t.Errorf("accepted Wiki search request %s", raw)
		}
	}
}

func TestHandleMCPReadListsOpenTasksWithSourceLinks(t *testing.T) {
	ctx := context.Background()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "mcp-tasks.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	task := tasks.Item{ID: strings.Repeat("b", 32), Revision: 1, Title: "Buy eggs", State: tasks.Open, CreatedAt: now, UpdatedAt: now, UserAuthored: true}
	if err := store.PutTask(ctx, task, 0); err != nil {
		t.Fatal(err)
	}
	client := NewClient(store)
	client.askNow = func() time.Time { return now }
	wire, err := client.HandleMCPRead(ctx, localapi.RPCListTasks, json.RawMessage(`{"state":"open","limit":5}`))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Items []struct {
			ID           string `json:"id"`
			Title        string `json:"title"`
			State        string `json:"state"`
			UserAuthored bool   `json:"user_authored"`
		} `json:"items"`
		HasMore bool `json:"has_more"`
	}
	if err := json.Unmarshal(wire, &got); err != nil || len(got.Items) != 1 || got.Items[0].ID != task.ID || got.Items[0].Title != task.Title || got.Items[0].State != string(tasks.Open) || !got.Items[0].UserAuthored || got.HasMore {
		t.Fatalf("task-list response = %s, err=%v", wire, err)
	}
}

func TestHandleMCPReadBoundsTaskListRequest(t *testing.T) {
	client := NewClient(&testData{})
	for _, raw := range []string{`{"state":"unknown"}`, `{"limit":9}`, `{"after":"not-an-id"}`, `{"unknown":true}`} {
		if _, err := client.HandleMCPRead(context.Background(), localapi.RPCListTasks, json.RawMessage(raw)); err == nil {
			t.Errorf("accepted task-list request %s", raw)
		}
	}
}

func TestHandleMCPReadRejectsUnsupportedOrInvalidRequestsBeforeSearch(t *testing.T) {
	client := NewClient(&testData{})
	for _, test := range []struct {
		method string
		params string
	}{{"delete_all", `{}`}, {"get_today", `{}`}, {"search_memory", `{"query":"safe","limit":9}`}, {"search_memory", `{"query":"safe","unknown":true}`}, {"search_memory", `{"query":""}`}} {
		if _, err := client.HandleMCPRead(context.Background(), test.method, json.RawMessage(test.params)); err == nil {
			t.Errorf("accepted MCP read method=%q params=%s", test.method, test.params)
		}
	}
	data := client.data.(*testData)
	if data.searchCalls != 0 || len(data.readSourceIDs) != 0 {
		t.Fatalf("invalid MCP requests reached memory: searches=%d reads=%d", data.searchCalls, len(data.readSourceIDs))
	}
}

func TestHandleMCPReadRejectsMemoryMutationWindowAndCancelledContext(t *testing.T) {
	data := &testData{}
	client := NewClient(data)
	client.NotifyMemoryChanging()
	params := json.RawMessage(`{"query":"launch","limit":1}`)
	if _, err := client.HandleMCPRead(context.Background(), "search_memory", params); err == nil {
		t.Fatal("MCP read returned data while memory mutation was active")
	}
	client.NotifyMemoryStable()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.HandleMCPRead(ctx, "search_memory", params); err == nil {
		t.Fatal("MCP read unexpectedly succeeded with cancelled context")
	}
	if data.searchCalls != 0 {
		t.Fatalf("unavailable MCP read reached storage %d times", data.searchCalls)
	}
}

func TestRetrieveAdaptsSanitizedSearchHitsToAssistantEvidence(t *testing.T) {
	observation, err := capture.SanitizeObservation("com.example.chat", "local", "launch decision", 4096)
	if err != nil {
		t.Fatal(err)
	}
	d := &testData{page: search.ExactPage{Hits: []search.IndexHit{{SourceID: observation.ContentID(), Bytes: len(observation.Text()) + len(observation.AppID()) + len(observation.Domain())}}}, observations: map[string]capture.SanitizedObservation{observation.ContentID(): observation}}
	got, err := NewClient(d).Retrieve(context.Background(), "launch")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != (assistant.Evidence{SourceID: observation.ContentID(), AppID: observation.AppID(), Domain: observation.Domain(), Text: observation.Text()}) {
		t.Fatalf("retrieved assistant evidence=%+v", got)
	}
	if d.query.TopK != assistant.MaxEvidenceSources || d.query.MaxRows != assistant.MaxEvidenceSources*16 {
		t.Fatalf("Ask retrieved more candidates than it can pack: %+v", d.query)
	}
}

func TestRetrieveUsesBoundedPartialTermFallbackOnlyAfterExactMiss(t *testing.T) {
	observation, err := capture.SanitizeObservation("com.example.chat", "local", "Anna confirmed the Q3 budget", 4096)
	if err != nil {
		t.Fatal(err)
	}
	hit := search.IndexHit{SourceID: observation.ContentID(), Bytes: len(observation.Text()) + len(observation.AppID()) + len(observation.Domain())}
	d := &testData{
		pages:        []search.ExactPage{{}, {Hits: []search.IndexHit{hit}}},
		observations: map[string]capture.SanitizedObservation{observation.ContentID(): observation},
	}
	got, err := NewClient(d).Retrieve(context.Background(), "What did Anna say about the Q3 budget?")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SourceID != observation.ContentID() {
		t.Fatalf("fallback evidence=%+v, want source %s", got, observation.ContentID())
	}
	if len(d.queries) != 2 || d.queries[0].MinTermMatches != 0 || d.queries[1].MinTermMatches != 2 {
		t.Fatalf("retrieval queries=%+v, want exact AND then a two-term fallback", d.queries)
	}
	if d.queries[1].MaxRows != assistant.MaxEvidenceSources*16 || strings.Join(d.queries[1].Terms, ",") != "anna,budget,q3" {
		t.Fatalf("fallback query=%+v, want the same terms and bounded row budget", d.queries[1])
	}
}

func TestRetrieveRejectsCancelledContextBeforeReadingStore(t *testing.T) {
	d := &testData{page: search.ExactPage{Hits: []search.IndexHit{{SourceID: "source", Bytes: 1}}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewClient(d).Retrieve(ctx, "launch"); err == nil {
		t.Fatal("cancelled retrieval unexpectedly succeeded")
	}
	if len(d.query.Terms) != 0 {
		t.Fatal("cancelled retrieval reached the encrypted store")
	}
}

type integrationTokenCounter struct{}

func (integrationTokenCounter) CountInputTokens(context.Context, string, string) (int, error) {
	return 800, nil
}

type integrationAnswerGenerator struct{ sourceID string }

func (g integrationAnswerGenerator) Generate(context.Context, assistant.PackedContext, int) (assistant.ModelAnswer, error) {
	return assistant.ModelAnswer{Text: "The launch decision was recorded.", SourceIDs: []string{g.sourceID}}, nil
}

func TestAskUsesLocalExactRetrievalAndReturnsCitedSources(t *testing.T) {
	observation, err := capture.SanitizeObservation("com.example.chat", "local", "The launch decision was recorded.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	bytes := len(observation.Text()) + len(observation.AppID()) + len(observation.Domain())
	data := &testData{
		page:         search.ExactPage{Hits: []search.IndexHit{{SourceID: observation.ContentID(), Score: 1, Bytes: bytes}}},
		observations: map[string]capture.SanitizedObservation{observation.ContentID(): observation},
	}
	service := assistant.NewService(NewClient(data), integrationAnswerGenerator{sourceID: observation.ContentID()}, integrationTokenCounter{}, assistant.ContextTokenLimit)
	got, err := service.Ask(context.Background(), "What was our launch decision?")
	if err != nil {
		t.Fatal(err)
	}
	if got.Answer.Text != "The launch decision was recorded." || len(got.Sources) != 1 || got.Sources[0].SourceID != observation.ContentID() || got.Sources[0].Text != observation.Text() {
		t.Fatalf("Ask did not return the exact cited local source: %+v", got)
	}
	if strings.Join(data.query.Terms, " ") != "decision launch" {
		t.Fatalf("natural-language question did not become a bounded exact query: %v", data.query.Terms)
	}
}

func TestAskMemoryUsesConfiguredLoopbackOllamaAndCitesLocalSource(t *testing.T) {
	observation, err := capture.SanitizeObservation("com.example.chat", "local", "Launch decision: Tuesday.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	server := httptest.NewServer(modeltest.LocalOllama("fixture-model", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		var request struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode local Ollama request: %v", err)
			return
		}
		if len(request.Messages) != 2 || request.Messages[0].Role != "system" || request.Messages[1].Role != "user" || !strings.Contains(request.Messages[1].Content, observation.Text()) {
			t.Errorf("local Ask omitted bounded evidence context: %+v", request.Messages)
			return
		}
		if strings.Contains(request.Messages[0].Content, "alternate search") && strings.Contains(request.Messages[0].Content, "Rank saved evidence") {
			_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": `{"queries":["launch decision","launch plan"],"matches":[{"ref":"S1","score":0.98}]}`}, "done": true, "prompt_eval_count": 40, "eval_count": 15})
			return
		}
		if strings.Contains(request.Messages[0].Content, "Rank saved evidence") {
			_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": `{"matches":[{"ref":"S1","score":0.98}]}`}, "done": true, "prompt_eval_count": 40, "eval_count": 15})
			return
		}
		content, _ := json.Marshal(assistant.ModelAnswer{Text: "The decision was Tuesday.", SourceIDs: []string{observation.ContentID()}})
		_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": string(content)}, "done": true, "prompt_eval_count": 40, "eval_count": 15})
	})))
	defer server.Close()
	settingsPath := filepath.Join(t.TempDir(), "local-ai.json")
	controller := newLocalAIController(settingsPath, notes.NewService(nil, nil, nil))
	defer func() { controller.Close() }()
	settings := LocalAISettings{Enabled: true, Provider: ProviderOllama, Endpoint: server.URL, Model: "fixture-model"}
	if err := controller.Configure(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	bytes := len(observation.Text()) + len(observation.AppID()) + len(observation.Domain())
	data := &testData{page: search.ExactPage{Hits: []search.IndexHit{{SourceID: observation.ContentID(), Score: 2, Bytes: bytes}}}, observations: map[string]capture.SanitizedObservation{observation.ContentID(): observation}}
	client := NewClient(data).WithLocalAI(controller)
	got, err := client.AskMemory(context.Background(), "What was our launch decision?")
	if err != nil {
		t.Fatal(err)
	}
	if !called || got.Answer.Text != "The decision was Tuesday." || len(got.Sources) != 1 || got.Sources[0].SourceID != observation.ContentID() {
		t.Fatalf("local Ask did not return its cited source: called=%t result=%+v", called, got)
	}
}

func TestRemoteAskRequiresExplicitConsentAndUsesBoundedLocalEvidence(t *testing.T) {
	observation, err := capture.SanitizeObservation("com.example.chat", "local", "Launch decision: Tuesday.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer ask-test-secret" {
			t.Errorf("remote Ask authorization = %q, want OS-stored credential", r.Header.Get("Authorization"))
			return
		}
		var request struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode compatible Ask request: %v", err)
			return
		}
		if len(request.Messages) != 2 || !strings.Contains(request.Messages[1].Content, observation.Text()) {
			t.Errorf("remote Ask did not receive the bounded retrieved evidence: %+v", request.Messages)
			return
		}
		if strings.Contains(request.Messages[0].Content, "alternate search") && strings.Contains(request.Messages[0].Content, "Rank saved evidence") {
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": `{"queries":["launch decision","launch plan"],"matches":[{"ref":"S1","score":0.98}]}`}}}})
			return
		}
		if strings.Contains(request.Messages[0].Content, "Rank saved evidence") {
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": `{"matches":[{"ref":"S1","score":0.98}]}`}}}})
			return
		}
		content, _ := json.Marshal(assistant.ModelAnswer{Text: "The decision was Tuesday.", SourceIDs: []string{observation.ContentID()}})
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": string(content)}}}})
	}))
	defer server.Close()

	settingsPath := filepath.Join(t.TempDir(), "local-ai.json")
	secrets := &bootstrapSecrets{values: make(map[string][]byte)}
	controller := newLocalAIController(settingsPath, notes.NewService(nil, nil, nil), secrets)
	defer func() { controller.Close() }()
	settings := LocalAISettings{Enabled: true, Provider: ProviderOpenAICompatible, Endpoint: server.URL + "/v1", Model: "fixture-model"}
	if err := controller.Configure(context.Background(), settings, "ask-test-secret"); err != nil {
		t.Fatal(err)
	}
	preferences, err := os.ReadFile(settingsPath)
	if err != nil || strings.Contains(string(preferences), "ask-test-secret") {
		t.Fatalf("provider credential was written to preferences: %q, err=%v", preferences, err)
	}
	bytes := len(observation.Text()) + len(observation.AppID()) + len(observation.Domain())
	data := &testData{page: search.ExactPage{Hits: []search.IndexHit{{SourceID: observation.ContentID(), Score: 1, Bytes: bytes}}}, observations: map[string]capture.SanitizedObservation{observation.ContentID(): observation}}
	client := NewClient(data).WithLocalAI(controller)
	if !client.CanAskMemory() {
		t.Fatal("local evidence lookup requires remote consent")
	}
	local, err := client.AskMemory(context.Background(), "What was our launch decision?")
	if err != nil || !local.Direct || local.General || local.SentItems != 0 || local.SentBytes != 0 || !strings.Contains(local.Answer.Text, observation.Text()) || len(local.Sources) != 1 || local.Sources[0].SourceID != observation.ContentID() {
		t.Fatalf("unconsented provider should leave local source lookup available: %+v, err=%v", local, err)
	}
	if requests.Load() != 0 {
		t.Fatalf("unconsented remote Ask issued %d network requests", requests.Load())
	}

	settings.RemoteAskEnabled = true
	if err := controller.Configure(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	controller.Close()
	controller = newLocalAIController(settingsPath, notes.NewService(nil, nil, nil), secrets)
	if controller.NeedsRepair() || !controller.Settings().RemoteAskEnabled {
		t.Fatal("separate remote Ask consent did not survive restart")
	}
	client = NewClient(data).WithLocalAI(controller)
	result, err := client.AskMemory(context.Background(), "What was our launch decision?")
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 || result.Answer.Text != "The decision was Tuesday." || len(result.Sources) != 1 || result.Sources[0].SourceID != observation.ContentID() {
		t.Fatalf("consented remote Ask result = %+v; requests = %d", result, requests.Load())
	}
	changedEndpoint := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer changedEndpoint.Close()
	settings.Endpoint = changedEndpoint.URL + "/v1"
	if err := controller.Configure(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	if controller.Settings().RemoteAskEnabled {
		t.Fatal("remote Ask consent followed a changed endpoint")
	}
}

func TestMemoryEpochChangesBeforeMutationSignalCanBeProcessed(t *testing.T) {
	client := NewClient(nil)
	started, ok := client.BeginMemoryRead()
	if !ok {
		t.Fatal("stable memory did not allow a read")
	}
	client.NotifyMemoryChanging()
	if client.MemoryReadCurrent(started) {
		t.Fatal("pre-mutation read remained publishable after mutation began")
	}
	mutating, ok := client.BeginMemoryRead()
	if ok || mutating == started {
		t.Fatal("memory epoch did not change synchronously when mutation began")
	}
	client.NotifyMemoryStable()
	if client.MemoryIsChanging() {
		t.Fatal("mutation window remained open")
	}
	if _, ok := client.BeginMemoryRead(); !ok {
		t.Fatal("stable memory did not allow a fresh read")
	}
}

func TestMemoryMutationRunsUIInvalidatorBeforeReturning(t *testing.T) {
	client := NewClient(nil)
	called := false
	client.SetMemoryInvalidator(func() {
		called = true
		if !client.MemoryIsChanging() {
			t.Error("UI invalidation ran before the mutation window opened")
		}
	})
	client.NotifyMemoryChanging()
	if !called {
		t.Fatal("memory mutation returned before the visible state was invalidated")
	}
	client.NotifyMemoryStable()
}

func TestMemoryPublicationAndMutationStartAreSerialized(t *testing.T) {
	client := NewClient(nil)
	epoch, ok := client.BeginMemoryRead()
	if !ok {
		t.Fatal("stable memory did not allow a read")
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	published := make(chan bool, 1)
	go func() {
		published <- client.PublishMemoryRead(epoch, func() {
			close(entered)
			<-release
		})
	}()
	<-entered
	mutationDone := make(chan struct{})
	go func() {
		client.NotifyMemoryChanging()
		close(mutationDone)
	}()
	select {
	case <-mutationDone:
		t.Fatal("mutation began during guarded UI publication")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if !<-published {
		t.Fatal("current read was not published")
	}
	select {
	case <-mutationDone:
	case <-time.After(time.Second):
		t.Fatal("mutation did not continue after publication")
	}
	if client.MemoryReadCurrent(epoch) {
		t.Fatal("read remained current after mutation started")
	}
	client.NotifyMemoryStable()
}

func TestAskMemoryIsCancelledAndSignalsWhenMemoryIsChanging(t *testing.T) {
	observation, err := capture.SanitizeObservation("com.example.chat", "local", "Launch decision: Tuesday.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 1)
	server := httptest.NewServer(modeltest.LocalOllama("fixture-model", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	})))
	defer server.Close()
	controller := newLocalAIController(filepath.Join(t.TempDir(), "local-ai.json"), notes.NewService(nil, nil, nil))
	defer controller.Close()
	if err := controller.Configure(context.Background(), LocalAISettings{Enabled: true, Provider: ProviderOllama, Endpoint: server.URL, Model: "fixture-model"}); err != nil {
		t.Fatal(err)
	}
	bytes := len(observation.Text()) + len(observation.AppID()) + len(observation.Domain())
	data := &testData{page: search.ExactPage{Hits: []search.IndexHit{{SourceID: observation.ContentID(), Bytes: bytes}}}, observations: map[string]capture.SanitizedObservation{observation.ContentID(): observation}}
	client := NewClient(data).WithLocalAI(controller)
	result := make(chan error, 1)
	go func() {
		_, err := client.AskMemory(context.Background(), "What was our launch decision?")
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("Ask did not reach the configured local model")
	}
	client.NotifyMemoryChanging()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Ask after memory mutation signal = %v, want canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("memory mutation did not cancel local generation")
	}
	select {
	case <-client.MemoryChanges():
	default:
		t.Fatal("memory mutation did not signal the desktop")
	}
	if !client.MemoryIsChanging() {
		t.Fatal("memory mutation window was not marked active")
	}
	if _, err := client.AskMemory(context.Background(), "What was decided?"); !errors.Is(err, ErrMemoryChanging) {
		t.Fatalf("new Ask during memory mutation = %v, want ErrMemoryChanging", err)
	}
	client.NotifyMemoryStable()
	if client.MemoryIsChanging() {
		t.Fatal("memory mutation window did not close")
	}
}

func TestTodayReturnsSnapshotWithoutTransformingEvidence(t *testing.T) {
	snapshot := today.Snapshot{Date: "2026-11-05", Zone: "Europe/Brussels", GeneratedAt: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC), GenerationID: "g1"}
	d := &testData{today: snapshot, found: true}
	got, err := NewClient(d).Today(snapshot.Date)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "ready" || got.Snapshot.GenerationID != "g1" {
		t.Fatalf("unexpected snapshot result: %+v", got)
	}
}

type noteAdder struct{ text string }

func (n *noteAdder) Add(_ context.Context, text string) (notes.Result, error) {
	n.text = text
	return notes.Result{SourceID: "source", Proposals: []notes.Proposal{{Kind: notes.Task, Text: "follow up", SourceID: "source"}}}, nil
}

func TestAddNoteForwardsUserTextAndReturnsProposals(t *testing.T) {
	adder := &noteAdder{}
	client := NewClient(nil).WithNotes(adder)
	got, err := client.AddNote("Talk to Alex tomorrow")
	if err != nil {
		t.Fatal(err)
	}
	if adder.text != "Talk to Alex tomorrow" || got.SourceID != "source" || len(got.Proposals) != 1 {
		t.Fatalf("note not forwarded or result lost: %+v", got)
	}
}

func TestCanAddNoteRequiresConfiguredNoteService(t *testing.T) {
	if NewClient(nil).CanAddNote() {
		t.Fatal("unconfigured client reports writable notes")
	}
	if !NewClient(&testData{}).WithNotes(&noteAdder{}).CanAddNote() {
		t.Fatal("configured note service not reported")
	}
}

func TestAddKnowledgeCreatesUserAuthoredEvidenceAndPersistsIt(t *testing.T) {
	data := &testKnowledgeData{testData: &testData{}}
	client := NewClient(data)
	if !client.CanAddKnowledge() {
		t.Fatal("durable knowledge writer was not detected")
	}
	entity, err := client.AddKnowledge(context.Background(), knowledge.Project, "  Aurora  ", "  Plan the launch  ")
	if err != nil {
		t.Fatal(err)
	}
	if entity.Kind != knowledge.Project || entity.Name != "Aurora" || entity.State != knowledge.Confirmed {
		t.Fatalf("unexpected user-created entity: %+v", entity)
	}
	if len(entity.Facts) != 1 || entity.Facts[0].Text != "Plan the launch" || entity.Facts[0].State != knowledge.Confirmed || len(entity.Facts[0].Sources) != 1 || !entity.Facts[0].Sources[0].UserAuthored {
		t.Fatalf("optional note is not explicitly user-authored: %+v", entity.Facts)
	}
	if err := entity.Validate(); err != nil {
		t.Fatalf("created entity is invalid: %v", err)
	}
	if data.entity.ID != entity.ID || data.entity.Name != entity.Name {
		t.Fatalf("entity was not persisted: %+v", data.entity)
	}
}

func TestAddKnowledgeRejectsInvalidInputAndUnavailableWriter(t *testing.T) {
	if NewClient(&testData{}).CanAddKnowledge() {
		t.Fatal("read-only data source reports writable knowledge")
	}
	client := NewClient(&testKnowledgeData{testData: &testData{}})
	for _, test := range []struct {
		kind knowledge.Kind
		name string
		note string
	}{
		{knowledge.Kind("unknown"), "Aurora", ""},
		{knowledge.Project, "  ", ""},
		{knowledge.Person, "Aurora", string(make([]byte, knowledge.MaxTextBytes+1))},
		{knowledge.Topic, string(make([]byte, knowledge.MaxTextBytes+1)), ""},
	} {
		if _, err := client.AddKnowledge(context.Background(), test.kind, test.name, test.note); err == nil {
			t.Fatalf("invalid knowledge input accepted: %+v", test)
		}
	}
	if _, err := client.AddKnowledge(context.Background(), knowledge.Project, "Aurora", ""); err != nil {
		t.Fatalf("empty optional note rejected: %v", err)
	}
	if _, err := NewClient(&testData{}).AddKnowledge(context.Background(), knowledge.Project, "Aurora", ""); err == nil {
		t.Fatal("knowledge write succeeded without a writer")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.AddKnowledge(ctx, knowledge.Project, "Aurora", ""); err == nil {
		t.Fatal("cancelled knowledge write accepted")
	}
}

func TestDeleteKnowledgeRequiresExplicitWriterAndValidID(t *testing.T) {
	data := &testKnowledgeData{testData: &testData{}}
	client := NewClient(data)
	if !client.CanDeleteKnowledge() {
		t.Fatal("configured knowledge deleter not reported")
	}
	if err := client.DeleteKnowledge(context.Background(), "entity-user-created"); err != nil {
		t.Fatal(err)
	}
	if data.deletedID != "entity-user-created" {
		t.Fatalf("deleted id=%q", data.deletedID)
	}
	for _, id := range []string{"", "bad/id", strings.Repeat("a", 129)} {
		if err := client.DeleteKnowledge(context.Background(), id); err == nil {
			t.Fatalf("invalid id %q was accepted", id)
		}
	}
	if NewClient(&testData{}).CanDeleteKnowledge() {
		t.Fatal("read-only data source reports deletable knowledge")
	}
	if err := NewClient(&testData{}).DeleteKnowledge(context.Background(), "entity-user-created"); err == nil {
		t.Fatal("delete succeeded without a deleter")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := client.DeleteKnowledge(ctx, "entity-user-created"); err == nil {
		t.Fatal("cancelled delete accepted")
	}
}

func TestUnmergeKnowledgeRequiresExplicitWriterAndValidIDs(t *testing.T) {
	data := &testKnowledgeData{testData: &testData{}}
	client := NewClient(data)
	if !client.CanUnmergeKnowledge() {
		t.Fatal("configured knowledge unmerger not reported")
	}
	if err := client.UnmergeKnowledge(context.Background(), "entity-source", "entity-target"); err != nil {
		t.Fatal(err)
	}
	if data.unmergedSourceID != "entity-source" || data.unmergedTargetID != "entity-target" {
		t.Fatalf("unmerge args source=%q target=%q", data.unmergedSourceID, data.unmergedTargetID)
	}
	for _, ids := range [][2]string{{"", "entity-target"}, {"bad/id", "entity-target"}, {"entity-source", ""}} {
		if err := client.UnmergeKnowledge(context.Background(), ids[0], ids[1]); err == nil {
			t.Fatalf("invalid unmerge ids accepted: %q, %q", ids[0], ids[1])
		}
	}
	if NewClient(&testData{}).CanUnmergeKnowledge() {
		t.Fatal("read-only data source reports unmerge support")
	}
}

func TestDeleteSourceRequiresConfiguredDeleterAndValidHash(t *testing.T) {
	if _, err := NewClient(&testData{}).DeleteSource("not-a-hash"); err == nil {
		t.Fatal("unconfigured delete accepted")
	}
	deleter := &testDeleter{}
	client := NewClient(&testData{}).WithDeletion(deleter)
	if !client.CanDeleteSource() {
		t.Fatal("configured deletion service not reported")
	}
	if _, err := client.DeleteSource("../invalid"); err == nil {
		t.Fatal("invalid source ID accepted")
	}
	id := strings.Repeat("a", 64)
	removed, err := client.DeleteSource(id)
	if err != nil || removed != 1 || deleter.calls != 1 || deleter.id != id {
		t.Fatalf("DeleteSource = (%d, %v), call=%d id=%q", removed, err, deleter.calls, deleter.id)
	}
}

func TestConfirmProposalValidatesAndForwardsExplicitConfirmation(t *testing.T) {
	d := &testData{}
	client := NewClient(d)
	if err := client.ConfirmProposal("2026-11-05", "proposal-1"); err != nil {
		t.Fatal(err)
	}
	if d.confirmedDate != "2026-11-05" || d.confirmedID != "proposal-1" {
		t.Fatalf("confirmation not forwarded: date=%q id=%q", d.confirmedDate, d.confirmedID)
	}
	if err := client.ConfirmProposal("bad-date", "proposal-1"); err == nil {
		t.Fatal("invalid date accepted")
	}
}

func TestQuestionAnswerAndDismissAreExplicitAndBounded(t *testing.T) {
	d := &testData{}
	client := NewClient(d)
	if err := client.AnswerQuestion("2026-11-05", "question-1", "Sam owns the draft"); err != nil {
		t.Fatal(err)
	}
	if d.answeredID != "question-1" || d.answer != "Sam owns the draft" {
		t.Fatalf("answer not forwarded: %+v", d)
	}
	if err := client.DismissQuestion("2026-11-05", "question-2"); err != nil {
		t.Fatal(err)
	}
	if d.dismissedID != "question-2" {
		t.Fatalf("dismissal not forwarded: %+v", d)
	}
	if err := client.AnswerQuestion("2026-11-05", "question-1", " \n "); err == nil {
		t.Fatal("empty answer accepted")
	}
}

type changingWriteData struct {
	*testData
	onRead func()
}

func (d *changingWriteData) GetTodaySnapshot(ctx context.Context, date string) (today.Snapshot, bool, error) {
	if d.onRead != nil {
		d.onRead()
	}
	return d.testData.GetTodaySnapshot(ctx, date)
}

func TestTodaySkipsPersistWhenMemoryChangedDuringRead(t *testing.T) {
	t.Setenv("TZ", "UTC")
	segments := []activity.Segment{{ID: "s1", WallStart: time.Date(2026, 11, 5, 1, 0, 0, 0, time.UTC), WallEnd: time.Date(2026, 11, 5, 1, 20, 0, 0, time.UTC), Duration: int64(20 * time.Minute), AppID: "com.example.editor", SourceIDs: []string{"capture-editor"}}}
	base := &testData{activity: segments}
	var client *Client
	data := &changingWriteData{testData: base}
	client = NewClient(data)
	data.onRead = func() { client.NotifyMemoryChanging(); client.NotifyMemoryStable() }
	got, err := client.Today("2026-11-05")
	if err != nil {
		t.Fatal(err)
	}
	if base.todayWrites != 0 {
		t.Fatalf("stale snapshot persisted after memory change: writes=%d", base.todayWrites)
	}
	if got.State != "ready" {
		t.Fatalf("fresh in-memory result not returned: %+v", got)
	}
	data.onRead = nil
	if _, err := client.Today("2026-11-05"); err != nil || base.todayWrites != 1 {
		t.Fatalf("stable read should persist: writes=%d err=%v", base.todayWrites, err)
	}
}

func TestResolveZoneName(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	if got := resolveZoneName("Europe/Brussels", "/nonexistent", now); got != "Europe/Brussels" {
		t.Fatalf("TZ env: %q", got)
	}
	link := filepath.Join(t.TempDir(), "localtime")
	// Use a platform-native target spelling: Windows Readlink returns backslashes.
	target := filepath.Join(t.TempDir(), "zoneinfo", "Asia", "Tokyo")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Logf("symlink fixture unavailable: %v", err)
	} else if got := resolveZoneName("", link, now); got != "Asia/Tokyo" {
		t.Fatalf("symlink: %q", got)
	}
	got := resolveZoneName("", "/nonexistent", now)
	if got == "Local" || got == "" {
		t.Fatalf("fallback: %q", got)
	}
	zone, err := loadZone("CET+0100")
	if err != nil {
		t.Fatal(err)
	}
	if _, off := now.In(zone).Zone(); off != 3600 {
		t.Fatalf("offset=%d", off)
	}
	if _, err := loadZone("bogus"); err == nil {
		t.Fatal("bogus zone accepted")
	}
}

func TestResolveZoneNameFromWindowsZoneinfoSymlink(t *testing.T) {
	link := filepath.Join(t.TempDir(), "localtime")
	if err := os.Symlink(`C:\zoneinfo\Asia\Tokyo`, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	if got := resolveZoneName("", link, now); got != "Asia/Tokyo" {
		t.Fatalf("Windows zoneinfo target resolved to %q", got)
	}
}

func TestClosedClientOperationsReturnPromptly(t *testing.T) {
	client := NewClient(&testData{})
	client.Close()
	done := make(chan error, 2)
	go func() { _, err := client.Today("2026-11-05"); done <- err }()
	go func() { done <- client.ConfirmProposal("2026-11-05", "p") }()
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if !errors.Is(err, ErrClientClosed) {
				t.Fatalf("err=%v want ErrClientClosed", err)
			}
		case <-time.After(time.Second):
			t.Fatal("operation after Close hung")
		}
	}
}
