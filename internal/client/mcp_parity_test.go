package client

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/appidentity"
	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/localapi"
	"github.com/hazcod/blur/internal/tasks"
)

func TestMCPParityRejectsInvalidRequestsBeforeReading(t *testing.T) {
	client := NewClient(&testData{})
	defer client.Close()
	day := int64(1763000000)
	for _, test := range []struct{ method, params string }{
		{localapi.RPCReadSession, `{}`},
		{localapi.RPCReadSession, fmt.Sprintf(`{"app":"","from":%d,"to":%d}`, day, day+60)},
		{localapi.RPCReadSession, fmt.Sprintf(`{"app":"Slack","from":%d,"to":%d}`, day, day+86401)},
		{localapi.RPCReadSession, fmt.Sprintf(`{"app":"Slack","from":%d,"to":%d}`, day+60, day)},
		{localapi.RPCReadSession, fmt.Sprintf(`{"app":"Slack","from":%d,"to":%d,"limit":21}`, day, day+60)},
		{localapi.RPCReadSession, fmt.Sprintf(`{"app":"Slack","from":%d,"to":%d,"after":"nope"}`, day, day+60)},
		{localapi.RPCReadSession, fmt.Sprintf(`{"app":"Slack","from":%d,"to":%d,"unknown":1}`, day, day+60)},
		{localapi.RPCListWiki, `{"type":"company"}`},
		{localapi.RPCListWiki, `{"limit":51}`},
		{localapi.RPCListWiki, `{"limit":-1}`},
		{localapi.RPCGetTimeReport, `{"period":"next_year"}`},
		{localapi.RPCGetTimeReport, `{"period":"today","from":"2026-11-01","to":"2026-11-02"}`},
		{localapi.RPCGetTimeReport, `{"from":"2026-11-01"}`},
		{localapi.RPCGetTimeReport, `{"from":"2026-13-01","to":"2026-13-02"}`},
		{localapi.RPCGetPromises, `{"limit":51}`},
		{localapi.RPCGetPromises, `{"query":"a\u0000b"}`},
		{localapi.RPCGetPromises, `{"extra":true}`},
	} {
		if _, err := client.HandleMCPRead(context.Background(), test.method, json.RawMessage(test.params)); err == nil {
			t.Errorf("accepted %s %s", test.method, test.params)
		}
	}
	if data := client.data.(*testData); data.searchCalls != 0 || len(data.readSourceIDs) != 0 || len(data.observationWindowRequests) != 0 {
		t.Fatalf("invalid requests reached memory: %+v", data)
	}
}

func TestMCPTimeReportRejectsOverlongCustomRange(t *testing.T) {
	zone, _ := time.LoadLocation("Europe/Brussels")
	now := time.Date(2026, 11, 16, 15, 30, 0, 0, zone)
	if _, _, err := mcpReportRange(mcpTimeReportParams{From: "2026-10-01", To: "2026-11-01"}, now, zone); err == nil {
		t.Fatal("32-day range accepted")
	}
	start, end, err := mcpReportRange(mcpTimeReportParams{From: "2026-10-02", To: "2026-11-01"}, now, zone)
	if err != nil || end.Sub(start) < 31*24*time.Hour-time.Hour {
		t.Fatalf("31-day range = %v %v err=%v", start, end, err)
	}
	for period, want := range map[string][2]string{
		"this_week": {"2026-11-16", "2026-11-22"}, "last_week": {"2026-11-09", "2026-11-15"},
		"yesterday": {"2026-11-15", "2026-11-15"}, "last_7_days": {"2026-11-10", "2026-11-16"},
	} {
		start, end, err := mcpReportRange(mcpTimeReportParams{Period: period}, now, zone)
		if err != nil || start.Format("2006-01-02") != want[0] || end.AddDate(0, 0, -1).Format("2006-01-02") != want[1] {
			t.Errorf("%s = %v..%v err=%v, want %v", period, start, end, err, want)
		}
	}
}

func TestHandleMCPReadSessionReturnsAppCapturesOldestFirstWithoutBlur(t *testing.T) {
	corpus := newScenarioCorpus(t)
	ctx := context.Background()
	from := time.Date(2026, 11, 14, 0, 0, 0, 0, corpus.loc).Unix()
	call := func(params string) (out struct {
		Captures []struct {
			SourceID        string `json:"source_id"`
			AppID           string `json:"app_id"`
			CapturedAt      string `json:"captured_at"`
			LastSeenAt      string `json:"last_seen_at"`
			Content         string `json:"content"`
			NextOffsetBytes int    `json:"next_offset_bytes"`
			Complete        bool   `json:"complete"`
		} `json:"captures"`
		HasMore bool   `json:"has_more"`
		Next    string `json:"next"`
	}, raw []byte) {
		t.Helper()
		raw, err := corpus.client.HandleMCPRead(ctx, localapi.RPCReadSession, json.RawMessage(params))
		if err != nil {
			t.Fatalf("%s: %v", params, err)
		}
		if len(raw) > localapi.MaxRPCSnapshotResponseBytes {
			t.Fatalf("response %d bytes exceeds the snapshot budget", len(raw))
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out, raw
	}
	// Display names resolve case-insensitively through KnownApps.
	out, _ := call(fmt.Sprintf(`{"app":"sLaCk","from":%d,"to":%d}`, from, from+86400))
	var labels []string
	for _, item := range out.Captures {
		labels = append(labels, corpus.labels[item.SourceID])
		if item.AppID != "com.tinyspeck.slackmacgap" || item.LastSeenAt == "" || item.CapturedAt == "" || !item.Complete || item.Content == "" {
			t.Errorf("capture = %+v", item)
		}
	}
	if got, want := strings.Join(labels, ","), "slack-samira-2,slack-dima-1,slack-dima-2"; got != want {
		t.Fatalf("session order = %s, want %s", got, want)
	}
	// App IDs match too, and paging continues from the last source ID.
	out, _ = call(fmt.Sprintf(`{"app":"COM.TINYSPECK.SLACKMACGAP","from":%d,"to":%d,"limit":2}`, from, from+86400))
	if len(out.Captures) != 2 || !out.HasMore || out.Next != out.Captures[1].SourceID {
		t.Fatalf("first page = %+v", out)
	}
	next, _ := call(fmt.Sprintf(`{"app":"slack","from":%d,"to":%d,"limit":2,"after":%q}`, from, from+86400, out.Next))
	if len(next.Captures) != 1 || corpus.labels[next.Captures[0].SourceID] != "slack-dima-2" || next.HasMore {
		t.Fatalf("second page = %+v", next)
	}
	// Long captures use the get_snapshots offset convention.
	orion := time.Date(2026, 11, 16, 0, 0, 0, 0, corpus.loc).Unix()
	long, _ := call(fmt.Sprintf(`{"app":"Orion","from":%d,"to":%d,"limit":1,"max_bytes_per_source":4096}`, orion, orion+86400))
	if len(long.Captures) != 1 || long.Captures[0].Complete || long.Captures[0].NextOffsetBytes != len(long.Captures[0].Content) || long.Captures[0].NextOffsetBytes == 0 {
		t.Fatalf("long capture page = %+v", long)
	}
	// Blur's own interface is never returned, even when named explicitly.
	for _, app := range []string{appidentity.BundleID, strings.ToUpper(appidentity.BundleID)} {
		self, raw := call(fmt.Sprintf(`{"app":%q,"from":%d,"to":%d}`, app, orion, orion+86400))
		if len(self.Captures) != 0 || strings.Contains(string(raw), corpus.ids["blur-self"]) {
			t.Fatalf("Blur self capture leaked: %s", raw)
		}
	}
	// Honest empty result for a quiet window.
	empty, raw := call(fmt.Sprintf(`{"app":"Slack","from":%d,"to":%d}`, from-86400*30, from-86400*30+3600))
	if len(empty.Captures) != 0 || !strings.Contains(string(raw), `"captures":[]`) {
		t.Fatalf("empty window = %s", raw)
	}
}

func TestHandleMCPListWikiOrdersBySupportAndTruncatesSummary(t *testing.T) {
	corpus := newScenarioCorpus(t)
	ctx := context.Background()
	user := []knowledge.Source{{UserAuthored: true}}
	long := strings.Repeat("é", 120)
	entities := []knowledge.Entity{
		{ID: "person-zed", Kind: knowledge.Person, Name: "Zed", State: knowledge.Confirmed, Facts: []knowledge.Fact{{ID: "f1", Text: "Owns the retest.", State: knowledge.Confirmed, Sources: user}}},
		{ID: "project-alpha", Kind: knowledge.Project, Name: "Alpha", State: knowledge.Confirmed, Aliases: []string{"A"}, Facts: []knowledge.Fact{
			{ID: "f2", Text: "proposed only", State: knowledge.Proposed, Sources: user},
			{ID: "f3", Text: long, State: knowledge.Confirmed, Sources: user},
			{ID: "f4", Text: "Second fact", State: knowledge.Confirmed, Sources: user}},
			OpenLoops: []knowledge.OpenLoop{{ID: "l1", Text: "Ship it", State: knowledge.Proposed, Sources: user}, {ID: "l2", Text: "gone", State: knowledge.Dismissed, Sources: user}}},
		{ID: "person-abe", Kind: knowledge.Person, Name: "Abe", State: knowledge.Confirmed, Facts: []knowledge.Fact{{ID: "f5", Text: "Likes tea.", State: knowledge.Confirmed, Sources: user}}},
	}
	for _, entity := range entities {
		if err := corpus.store.PutKnowledgeEntity(ctx, entity); err != nil {
			t.Fatal(err)
		}
	}
	type row struct {
		ID, Kind, Name, Summary string
		Aliases                 []string
		FactCount               int `json:"fact_count"`
		OpenLoopCount           int `json:"open_loop_count"`
	}
	var out struct {
		Entries   []row `json:"entries"`
		Truncated bool  `json:"truncated"`
	}
	raw, err := corpus.client.HandleMCPRead(ctx, localapi.RPCListWiki, json.RawMessage(`{}`))
	if err != nil || json.Unmarshal(raw, &out) != nil || len(out.Entries) < 3 {
		t.Fatalf("list_wiki = %s err=%v", raw, err)
	}
	if got := []string{out.Entries[0].ID, out.Entries[1].ID, out.Entries[2].ID}; got[0] != "project-alpha" || got[1] != "person-abe" || got[2] != "person-zed" {
		t.Fatalf("order = %v", got)
	}
	alpha := out.Entries[0]
	if alpha.FactCount != 2 || alpha.OpenLoopCount != 1 || len(alpha.Summary) > 160 || alpha.Summary == "" || strings.ContainsRune(alpha.Summary, '�') || len(alpha.Aliases) != 1 {
		t.Fatalf("alpha row = %+v", alpha)
	}
	raw, err = corpus.client.HandleMCPRead(ctx, localapi.RPCListWiki, json.RawMessage(`{"type":"person","limit":1}`))
	if err != nil || json.Unmarshal(raw, &out) != nil || len(out.Entries) != 1 || out.Entries[0].Kind != "person" || !out.Truncated {
		t.Fatalf("typed list = %s err=%v", raw, err)
	}
}

func TestHandleMCPTimeReportAggregatesTrackedTimeWithoutBlur(t *testing.T) {
	corpus := newScenarioCorpus(t)
	ctx := context.Background()
	type report struct {
		From         string `json:"from"`
		To           string `json:"to"`
		TotalSeconds int64  `json:"total_seconds"`
		Buckets      []struct {
			AppID   string           `json:"app_id"`
			Seconds int64            `json:"seconds"`
			ByDay   map[string]int64 `json:"by_day"`
			Domains []struct {
				Domain  string `json:"domain"`
				Seconds int64  `json:"seconds"`
			} `json:"domains"`
		} `json:"buckets"`
		TopSegments []struct {
			AppID     string   `json:"app_id"`
			Seconds   int64    `json:"seconds"`
			SourceIDs []string `json:"source_ids"`
		} `json:"top_segments"`
		Note string `json:"note"`
	}
	read := func(params string) report {
		t.Helper()
		raw, err := corpus.client.HandleMCPRead(ctx, localapi.RPCGetTimeReport, json.RawMessage(params))
		if err != nil {
			t.Fatalf("%s: %v", params, err)
		}
		if strings.Contains(string(raw), appidentity.BundleID) {
			t.Fatalf("Blur's own interface appears in %s", raw)
		}
		var out report
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	today := read(`{"period":"today"}`)
	if today.From != "2026-11-16" || today.To != "2026-11-16" || today.TotalSeconds <= 0 || !strings.Contains(today.Note, "not a timesheet") {
		t.Fatalf("today = %+v", today)
	}
	var sum int64
	var orion bool
	for _, bucket := range today.Buckets {
		sum += bucket.Seconds
		if bucket.ByDay["2026-11-16"] != bucket.Seconds || len(bucket.ByDay) != 1 {
			t.Errorf("by_day of %s = %v, want one local day totalling %d", bucket.AppID, bucket.ByDay, bucket.Seconds)
		}
		if bucket.AppID == "com.kagi.kagimacOS" {
			orion = len(bucket.Domains) > 0 && bucket.Domains[0].Domain == "tiiny.ai"
		}
	}
	if !orion || sum != today.TotalSeconds || len(today.TopSegments) == 0 {
		t.Fatalf("today buckets = %+v", today)
	}
	for _, top := range today.TopSegments {
		if top.AppID != "" && len(top.SourceIDs) == 0 {
			t.Errorf("top segment %+v has no source IDs", top)
		}
	}
	lastWeek := read(`{"period":"last_week"}`)
	if lastWeek.From != "2026-11-09" || lastWeek.To != "2026-11-15" || lastWeek.TotalSeconds <= 0 {
		t.Fatalf("last_week = %+v", lastWeek)
	}
	custom := read(`{"from":"2026-11-15","to":"2026-11-15"}`)
	if custom.TotalSeconds <= 0 || custom.TotalSeconds >= lastWeek.TotalSeconds+today.TotalSeconds*10 {
		t.Fatalf("custom = %+v", custom)
	}
	if empty := read(`{"from":"2026-01-01","to":"2026-01-02"}`); empty.TotalSeconds != 0 || len(empty.Buckets) != 0 {
		t.Fatalf("empty = %+v", empty)
	}
}

func TestHandleMCPPromisesCombinesOpenLoopsAndTasksNewestFirst(t *testing.T) {
	corpus := newScenarioCorpus(t)
	ctx := context.Background()
	source := corpus.ids["slack-dima-retest"]
	entity := knowledge.Entity{ID: "person-dima", Kind: knowledge.Person, Name: "Dima", State: knowledge.Confirmed, OpenLoops: []knowledge.OpenLoop{
		{ID: "loop-scope", Text: "Dima will send the retest scope", State: knowledge.Proposed, Sources: []knowledge.Source{{ID: source}}},
		{ID: "loop-done", Text: "Old retest question", State: knowledge.Dismissed, Sources: []knowledge.Source{{UserAuthored: true}}},
	}}
	if err := corpus.store.PutKnowledgeEntity(ctx, entity); err != nil {
		t.Fatal(err)
	}
	now := corpus.now.UTC()
	open := tasks.Item{ID: strings.Repeat("a", 32), Revision: 1, Title: "Send Dima the invoice", State: tasks.Open, CreatedAt: now.Add(-48 * time.Hour), UpdatedAt: now.Add(-48 * time.Hour), UserAuthored: true}
	reminder := tasks.Item{ID: strings.Repeat("b", 32), Revision: 1, Title: "Call Chloe", State: tasks.Open, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour), UserAuthored: true,
		Reminder: &tasks.Reminder{DueAt: now.Add(24 * time.Hour), Timezone: "UTC"}, Delivery: tasks.Delivery{State: tasks.NeedsSync, At: now}}
	done := tasks.Item{ID: strings.Repeat("c", 32), Revision: 1, Title: "Book Dima room", State: tasks.Completed, CreatedAt: now.Add(-72 * time.Hour), UpdatedAt: now.Add(-24 * time.Hour), UserAuthored: true,
		History: []tasks.Transition{{State: tasks.Completed, At: now.Add(-24 * time.Hour), Revision: 1}}}
	for _, item := range []tasks.Item{open, reminder, done} {
		if err := corpus.store.PutTask(ctx, item, 0); err != nil {
			t.Fatal(err)
		}
	}
	type item struct {
		Kind, ID, Text, State, Subject string
		SubjectKind                    string   `json:"subject_kind"`
		DueAt                          *string  `json:"due_at"`
		SourceIDs                      []string `json:"source_ids"`
		UpdatedAt                      string   `json:"updated_at"`
	}
	var out struct {
		Items   []item `json:"items"`
		HasMore bool   `json:"has_more"`
		Note    string `json:"note"`
	}
	read := func(params string) {
		t.Helper()
		out.Items = nil
		raw, err := corpus.client.HandleMCPRead(ctx, localapi.RPCGetPromises, json.RawMessage(params))
		if err != nil || json.Unmarshal(raw, &out) != nil {
			t.Fatalf("%s: %s err=%v", params, raw, err)
		}
	}
	read(`{}`)
	var kinds []string
	for _, it := range out.Items {
		kinds = append(kinds, it.Kind+":"+it.ID)
	}
	// The Dima loop was captured 2026-11-15 14:47 local, newer than the 2-day-old task.
	want := []string{"reminder:" + reminder.ID, "open_loop:loop-scope", "task:" + open.ID}
	if strings.Join(kinds, ",") != strings.Join(want, ",") || !strings.Contains(out.Note, "not verified outcomes") {
		t.Fatalf("open promises = %v note=%q", kinds, out.Note)
	}
	if loop := out.Items[1]; loop.Subject != "Dima" || loop.SubjectKind != "person" || loop.State != "proposed" || len(loop.SourceIDs) != 1 || loop.SourceIDs[0] != source || loop.UpdatedAt == "" || loop.DueAt != nil {
		t.Fatalf("loop = %+v", loop)
	}
	if out.Items[0].DueAt == nil {
		t.Fatalf("reminder lacks due_at: %+v", out.Items[0])
	}
	read(`{"include_closed":true,"query":"DIMA"}`)
	kinds = nil
	for _, it := range out.Items {
		kinds = append(kinds, it.Kind+":"+it.ID+":"+it.State)
	}
	if got := strings.Join(kinds, ","); !strings.Contains(got, "open_loop:loop-scope:proposed") || !strings.Contains(got, "open_loop:loop-done:dismissed") || !strings.Contains(got, "task:"+done.ID+":completed") || !strings.Contains(got, "task:"+open.ID) || strings.Contains(got, reminder.ID) {
		t.Fatalf("closed+query = %s", got)
	}
	read(`{"limit":1}`)
	if len(out.Items) != 1 || !out.HasMore {
		t.Fatalf("limited = %+v", out)
	}
}
