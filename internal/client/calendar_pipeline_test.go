package client

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/chronicle"
	"github.com/hazcod/blur/internal/power"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

type calendarPipelinePolicy struct{}

func (calendarPipelinePolicy) Effective(context.Context) (power.Effective, error) {
	e, _ := power.Resolve(power.Config{Mode: power.Performance}, power.SystemState{})
	return e, nil
}

type calendarPipelineGenerator struct{}

func (calendarPipelineGenerator) CountInputTokens(context.Context, chronicle.Packed) (int, error) {
	return 10, nil
}
func (calendarPipelineGenerator) Generate(context.Context, chronicle.Packed, int) (chronicle.Summary, error) {
	panic("hour generation not expected")
}
func (calendarPipelineGenerator) CountRollupTokens(context.Context, chronicle.RollupPrompt) (int, error) {
	return 10, nil
}
func (calendarPipelineGenerator) GenerateRollup(context.Context, chronicle.RollupPrompt, int) (string, error) {
	return "- Reviewed the calendar release evidence.", nil
}

func TestCalendarPipelineNativeAskSearchMCPAndFollowup(t *testing.T) {
	t.Setenv("TZ", "UTC")
	ctx := context.Background()
	s, err := durable.OpenDurable(filepath.Join(t.TempDir(), "calendar.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.SetChronicleZone(func() string { return "UTC" })
	var sources []string
	for _, at := range []time.Time{time.Date(2024, 2, 29, 9, 0, 0, 0, time.UTC), time.Date(2024, 12, 31, 9, 0, 0, 0, time.UTC)} {
		o, err := capture.SanitizeObservation("com.apple.mail", "", "Calendar release evidence observed "+at.Format(time.RFC3339), 1024)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.PutObservationAt(ctx, o, at.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		sources = append(sources, o.ContentID())
		hour := chronicle.Summary{HourID: "hour:" + at.Format("20060102T15Z"), Start: at, End: at.Add(time.Hour), Text: "Reviewed calendar release evidence.", SourceIDs: []string{o.ContentID()}}
		if err := s.PutChronicle(ctx, hour); err != nil {
			t.Fatal(err)
		}
	}
	w := chronicle.NewWorker(s, calendarPipelinePolicy{}, calendarPipelineGenerator{}, chronicle.WorkerConfig{Enabled: true, Provider: power.LocalProvider, Now: func() time.Time { return time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC) }, Zone: func() string { return "UTC" }})
	defer w.Close()
	for pass := 0; pass < 32; pass++ {
		if err := w.ProcessRollups(ctx); err != nil {
			t.Fatal(err)
		}
		status, err := s.ChronicleRollupWorkStatus(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if status.Dirty == 0 && !status.BackfillPending {
			break
		}
	}
	c := NewClient(s)
	defer c.Close()
	c.askNow = func() time.Time { return time.Date(2025, 1, 2, 15, 0, 0, 0, time.UTC) }
	result, err := c.AskMemory(ctx, "Summarize my activity in 2024")
	if err != nil {
		t.Fatal(err)
	}
	retrieval, err := c.RetrievePlan(ctx, result.Plan)
	if err != nil {
		t.Fatal(err)
	}
	var annual assistant.Evidence
	for _, item := range retrieval.Evidence {
		if item.CapturedAt.Equal(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)) && item.EndAt.Equal(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)) {
			annual = item
		}
	}
	want := slices.Clone(sources)
	sort.Strings(want)
	if annual.SourceID == "" || !slices.Equal(annual.SourceIDs, want) || !slices.Contains(result.Answer.SourceIDs, annual.SourceID) {
		t.Fatalf("native long recall did not use its stored calendar year: %+v answer%+v", retrieval, result.Answer)
	}
	app := &meetingOperationTestApp{posted: make(chan func(), 16)}
	d := &desktop{app: app, client: c, page: "Ask", tokens: themePalette(false), memoryUI: memoryViewState{loaded: true}, askResult: &result}
	_, buttons, rendered := memoryWidgetTree(t, d)
	if !strings.Contains(rendered, "Year recap") || !strings.Contains(rendered, "2 saved sources") {
		t.Fatalf("native Ask did not render the calendar evidence and its provenance: %s", rendered)
	}
	clickMemoryButton(t, buttons, "Show saved sources")
	app.finish(t)
	_, buttons, rendered = memoryWidgetTree(t, d)
	for _, original := range []string{"Calendar release evidence observed 2024-02-29", "Calendar release evidence observed 2024-12-31"} {
		if !strings.Contains(rendered, original) {
			t.Fatalf("native recap action did not open its original evidence: %s", rendered)
		}
	}
	clickMemoryButton(t, buttons, "Hide saved sources")
	_, buttons, rendered = memoryWidgetTree(t, d)
	if d.recapSources.anchor.SourceID != "" || len(d.recapSources.page.Hits) != 0 || d.recapSources.full != nil || len(buttons["Hide saved sources"]) != 0 {
		t.Fatalf("closing recap originals retained expanded payloads: %s", rendered)
	}
	followup, err := c.RetrieveSources(ctx, result.Plan, []string{annual.SourceID})
	if err != nil || len(followup.Evidence) != 1 || followup.Evidence[0].SourceID != annual.SourceID {
		t.Fatalf("calendar derived source could not reopen: %+v %v", followup, err)
	}
	page, err := c.SearchMemoryPageContext(ctx, "calendar")
	if err != nil {
		t.Fatal(err)
	}
	for _, level := range []string{"month", "year"} {
		matched := false
		for _, summary := range page.Summaries {
			matched = matched || summary.Level == level
		}
		if !matched {
			t.Fatalf("native Search omitted %s summary indexes: %+v", level, page)
		}
		period := map[string]string{"month": "2024-12", "year": "2024"}[level]
		raw, err := c.HandleMCPRead(ctx, "get_chronicle", json.RawMessage(`{"level":"`+level+`","period":"`+period+`"}`))
		var response mcpRollupResponse
		if err != nil || json.Unmarshal(raw, &response) != nil || !response.Found || response.Level != level || len(response.SourceIDs) == 0 {
			t.Fatalf("MCP could not reopen calendar summary: %s %s %v", level, raw, err)
		}
	}
	if err := s.DeleteObservationRecord(ctx, sources[0]); err != nil {
		t.Fatal(err)
	}
	after, err := c.RetrieveSources(ctx, result.Plan, []string{annual.SourceID})
	if err != nil || len(after.Evidence) != 0 {
		t.Fatalf("erased calendar summary remained available to follow-up: %+v %v", after, err)
	}
}
