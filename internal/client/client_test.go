package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/chronicle"
	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/knowledge/derive"
	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/today"
)

type testData struct {
	today         today.Snapshot
	found         bool
	query         search.ExactQuery
	queries       []search.ExactQuery
	page          search.ExactPage
	pages         []search.ExactPage
	searchCalls   int
	readSourceIDs []string
	knowledge     knowledge.Page
	confirmedDate string
	confirmedID   string
	answeredID    string
	answer        string
	dismissedID   string
	observations  map[string]capture.SanitizedObservation
	activity      []activity.Segment
	activityDates []string
	savedToday    today.Snapshot
	savedTodaySet bool
	todayWrites   int
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		proposal := fmt.Sprintf(`{"suggestions":[{"kind":"project","name":"Northstar","fact":"The project is written in Go.","source_id":%q}]}`, selected.ContentID())
		_, _ = fmt.Fprintf(w, `{"message":{"content":%q},"done":true,"prompt_eval_count":80,"eval_count":24}`, proposal)
	}))
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
	if _, err := client.DeleteAll(nil); err == nil {
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
	if d.searchCalls <= len(d.pages) {
		return d.pages[d.searchCalls-1], nil
	}
	return d.page, nil
}
func (d *testData) ListKnowledge(_ context.Context, _ knowledge.PageRequest) (knowledge.Page, error) {
	return d.knowledge, nil
}
func (d *testData) GetObservation(_ context.Context, id string) (capture.SanitizedObservation, bool, error) {
	d.readSourceIDs = append(d.readSourceIDs, id)
	o, ok := d.observations[id]
	return o, ok, nil
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
	if len(response.Results) != 1 || len(response.Results[0].Text) > 2048 || !response.Results[0].Truncated || !response.Truncated {
		t.Fatalf("MCP response was not bounded/truncated: count=%d textBytes=%d itemTruncated=%t responseTruncated=%t", len(response.Results), len(response.Results[0].Text), response.Results[0].Truncated, response.Truncated)
	}
	if data.query.TopK != 3 || data.query.MaxRows != 3 || len(data.readSourceIDs) != 1 || data.readSourceIDs[0] != first.ContentID() {
		t.Fatalf("MCP read escaped candidate/source budget: query=%+v hydrated=%v", data.query, data.readSourceIDs)
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		content, _ := json.Marshal(assistant.ModelAnswer{Text: "The decision was Tuesday.", SourceIDs: []string{observation.ContentID()}})
		_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": string(content)}, "done": true, "prompt_eval_count": 40, "eval_count": 15})
	}))
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
	if client.CanAskMemory() {
		t.Fatal("remote Ask became available without separate evidence-egress consent")
	}
	if _, err := client.AskMemory(context.Background(), "What was our launch decision?"); !errors.Is(err, assistant.ErrAskUnavailable) {
		t.Fatalf("remote Ask without consent = %v, want unavailable", err)
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
	if requests.Load() != 1 || result.Answer.Text != "The decision was Tuesday." || len(result.Sources) != 1 || result.Sources[0].SourceID != observation.ContentID() {
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	}))
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
	if err := os.Symlink("/var/db/timezone/zoneinfo/Asia/Tokyo", link); err != nil {
		t.Fatal(err)
	}
	if got := resolveZoneName("", link, now); got != "Asia/Tokyo" {
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
