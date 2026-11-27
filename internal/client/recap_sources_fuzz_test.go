package client

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/chronicle"
	"github.com/hazcod/blur/internal/deletion"
	"github.com/hazcod/blur/internal/localapi"
	"github.com/hazcod/blur/internal/power"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

// These are runtime histories against real encrypted memory, the actual worker,
// native Ask and mounted source controls. The local generator and GUI dispatch
// port are controlled; inserted IDs/text remain the independent oracle.
func FuzzRecapSourceNavigation(f *testing.F) {
	for action := byte(0); action < 8; action++ {
		f.Add([]byte{3, action, 4, 6, 2, 4, 7})
	}
	f.Add([]byte{3, 5, 4, 3, 64, 4, 7, 71})
	f.Add([]byte{7, 135, 199, 231, 4, 6})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 12 {
			data = data[:12]
		}
		seed := sha256.Sum256(data)
		year, month := 1992+int(seed[0]%33), time.Month(1+seed[1]%12)
		zone := "UTC"
		if seed[2]&1 != 0 {
			zone = "Asia/Kathmandu"
		}
		t.Setenv("TZ", zone)
		location, err := time.LoadLocation(zone)
		if err != nil {
			t.Fatal(err)
		}
		monthly := seed[3]&1 != 0
		from, to := time.Date(year, 1, 1, 0, 0, 0, 0, location), time.Date(year+1, 1, 1, 0, 0, 0, 0, location)
		layer := assistant.LayerYear
		question := fmt.Sprintf("Summarize my activity in %d", year)
		if monthly {
			from = time.Date(year, month, 1, 0, 0, 0, 0, location)
			to = from.AddDate(0, 1, 0)
			layer = assistant.LayerMonth
			question = fmt.Sprintf("Summarize my activity in %s %d", month.String(), year)
		}
		ctx, path, key := context.Background(), filepath.Join(t.TempDir(), "navigation.db"), securestore.OfflineFixtureKey()
		var store *durable.Store
		var c *Client
		var worker *chronicle.Worker
		open := func(mode durable.OpenMode) {
			var err error
			store, err = durable.OpenDurable(path, key, mode)
			if err != nil {
				t.Fatal(err)
			}
			store.SetChronicleZone(func() string { return zone })
			deleter, err := deletion.NewService(store, nil)
			if err != nil {
				t.Fatal(err)
			}
			c = NewClient(store).WithDeletion(deleter)
			c.askNow = func() time.Time { return time.Date(2026, 11, 21, 15, 0, 0, 0, time.UTC) }
			worker = chronicle.NewWorker(store, calendarPipelinePolicy{}, calendarPipelineGenerator{}, chronicle.WorkerConfig{Enabled: true, Provider: power.LocalProvider, Now: func() time.Time { return time.Date(2026, 11, 21, 15, 0, 0, 0, time.UTC) }, Zone: func() string { return zone }})
		}
		close := func() {
			worker.Close()
			c.Close()
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
		}
		open(durable.CreateNew)
		defer func() { close() }()
		var live [6]string
		originals := map[string]string{}
		appsByID := map[string]string{}
		generation := 0
		var trace []byte
		sourceHour := func(slot int) time.Time {
			m := month
			if !monthly {
				m = time.Month(1 + (int(month)+slot*2)%12)
			}
			return time.Date(year, m, 5+slot, 9, 0, 0, 0, time.UTC)
		}
		put := func(slot, step int) {
			if live[slot] != "" {
				if err := store.DeleteObservationRecord(ctx, live[slot]); err != nil {
					t.Fatal(err)
				}
			}
			topics := []string{"supplier invoice", "train booking", "research reading", "release decision", "school pickup", "home maintenance", "renewal approval", "design review"}
			apps := []string{"com.apple.mail", "com.tinyspeck.slackmacgap", "com.apple.Safari", "com.apple.Terminal"}
			text := fmt.Sprintf("Saved %s evidence for reference %x, slot %d, revision %d.", topics[int(seed[4+slot])%len(topics)], seed[:4], slot, step)
			if slot < 2 && seed[10]&16 != 0 {
				text += strings.Repeat("Document detail includes café 東京 🧭. ", 180) + "End of the original source."
			}
			observation, err := capture.SanitizeObservation(apps[int(seed[12+slot])%len(apps)], "memory.example", text, capture.MaxObservationTextBytes)
			if err != nil {
				t.Fatal(err)
			}
			at := sourceHour(slot)
			if err := store.PutObservationAt(ctx, observation, at.Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
			summary := chronicle.Summary{HourID: "hour:" + at.Format("20060102T15Z"), Start: at, End: at.Add(time.Hour), Text: "Reviewed saved personal and work evidence.", SourceIDs: []string{observation.ContentID()}}
			if err := store.PutChronicle(ctx, summary); err != nil {
				t.Fatal(err)
			}
			live[slot] = observation.ContentID()
			originals[observation.ContentID()] = observation.Text()
			appsByID[observation.ContentID()] = strings.ToLower(observation.AppID())
			generation++
		}
		erase := func(slot int, viaClient bool) {
			if live[slot] == "" {
				return
			}
			if viaClient {
				if _, err := c.DeleteSource(live[slot]); err != nil {
					t.Fatal(err)
				}
			} else if err := store.DeleteObservationRecord(ctx, live[slot]); err != nil {
				t.Fatal(err)
			}
			live[slot] = ""
			generation++
		}
		settle := func() {
			for pass := 0; pass < 48; pass++ {
				if err := worker.ProcessRollups(ctx); err != nil {
					t.Fatalf("worker %x trace%v: %v", data, trace, err)
				}
				state, err := store.ChronicleRollupWorkStatus(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if state.Dirty == 0 && !state.BackfillPending {
					return
				}
			}
			t.Fatalf("navigation history did not settle: %x trace%v", data, trace)
		}
		expected := func() []string {
			ids := []string{}
			for _, id := range live {
				if id != "" {
					ids = append(ids, id)
				}
			}
			sort.Strings(ids)
			return ids
		}
		current := func() (assistant.Evidence, bool) {
			// Independently inserted originals must remain searchable throughout
			// the full calendar scope, after every edit/erasure/reopen transition.
			period := fmt.Sprint(year)
			if monthly {
				period = fmt.Sprintf("%s %d", month.String(), year)
			}
			query := fmt.Sprintf(`find "reference %x" in %s`, seed[:4], period)
			searchWant := expected()
			if seed[21]&1 != 0 && len(searchWant) > 0 {
				app := appsByID[searchWant[0]]
				name := map[string]string{"com.apple.mail": "Mail", "com.tinyspeck.slackmacgap": "Slack", "com.apple.safari": "Safari", "com.apple.terminal": "Terminal"}[app]
				query += " in " + name
				searchWant = slices.DeleteFunc(searchWant, func(id string) bool { return appsByID[id] != app })
			}
			var searchGot []string
			if seed[20]&1 == 0 {
				page, err := c.SearchMemoryPageContext(ctx, query)
				if err != nil {
					t.Fatalf("native long Search %x trace%v: %v", data, trace, err)
				}
				for _, hit := range page.Hits {
					if hit.Relaxed || originals[hit.SourceID] != hit.Text {
						t.Fatal("long Search returned an unrelated original")
					}
					searchGot = append(searchGot, hit.SourceID)
				}
			} else {
				raw, _ := json.Marshal(mcpSearchParams{Query: query, Limit: 8})
				wire, err := c.HandleMCPRead(ctx, localapi.RPCSearchMemory, raw)
				if err != nil {
					t.Fatalf("MCP long Search %x trace%v: %v", data, trace, err)
				}
				var page mcpSearchResponse
				if err := json.Unmarshal(wire, &page); err != nil {
					t.Fatal(err)
				}
				for _, hit := range page.Results {
					if hit.PartialMatch {
						t.Fatal("exact long Search was relaxed")
					}
					searchGot = append(searchGot, hit.SourceID)
				}
			}
			sort.Strings(searchGot)
			if !slices.Equal(searchGot, searchWant) {
				t.Fatalf("long Search diverged from insertion ledger: query%q got%v want%v trace%v", query, searchGot, searchWant, trace)
			}
			got, err := c.RetrievePlan(ctx, assistant.Plan{Kind: assistant.KindRecap, Question: question, Scope: assistant.Scope{Start: from, End: to, Explicit: true}})
			if err != nil {
				t.Fatal(err)
			}
			var anchor assistant.Evidence
			for _, item := range got.Evidence {
				if item.Layer == layer {
					anchor = item
					break
				}
			}
			want := expected()
			if len(want) == 0 {
				if anchor.SourceID != "" {
					t.Fatal("empty source ledger retained a populated calendar recap")
				}
				return anchor, false
			}
			if anchor.SourceID == "" {
				t.Fatalf("live history lost its calendar recap: %x trace%v", data, trace)
			}
			var ids []string
			cursor := ""
			for pageNo := 0; pageNo < 6; pageNo++ {
				page, err := c.RecapSourcesPage(ctx, anchor, cursor, 2)
				if err != nil {
					t.Fatalf("current originals %x trace%v: %v", data, trace, err)
				}
				if page.Total != len(want) || len(page.Hits) > 2 {
					t.Fatal("current navigation broke its page bound or source count")
				}
				for _, hit := range page.Hits {
					if !slices.Contains(want, hit.SourceID) || !strings.HasPrefix(originals[hit.SourceID], hit.Text) || len(hit.Text) > 4096 {
						t.Fatalf("stale or unrelated original: %+v", hit)
					}
					ids = append(ids, hit.SourceID)
				}
				cursor = page.Next
				if cursor == "" {
					break
				}
			}
			if cursor != "" || !slices.Equal(ids, want) {
				t.Fatalf("navigation diverged from independent insertion ledger: %v want%v trace%v", ids, want, trace)
			}
			return anchor, true
		}
		for slot := range live {
			put(slot, -1)
		}
		settle()
		anchor, available := current()
		var cachedAnchor assistant.Evidence
		var cached RecapSourcePage
		cachedGeneration := -1
		for step, b := range data {
			action := b % 8
			trace = append(trace, action)
			slot := int(b/8) % len(live)
			switch action {
			case 0:
				put(slot, step)
			case 1:
				erase(slot, false)
			case 2:
				close()
				open(durable.OpenExisting)
			case 3:
				if available {
					cachedAnchor = anchor
					var err error
					cached, err = c.RecapSourcesPage(ctx, anchor, "", 1)
					if err != nil {
						t.Fatal(err)
					}
					cachedGeneration = generation
				}
			case 4:
				if cachedGeneration >= 0 {
					stale := cachedGeneration != generation
					if cached.Next != "" {
						page, err := c.RecapSourcesPage(ctx, cachedAnchor, cached.Next, 1)
						if stale && (err == nil || len(page.Hits) != 0) || !stale && err != nil {
							t.Fatalf("stale continuation policy failed, stale%v err%v trace%v", stale, err, trace)
						}
					}
					if len(cached.Hits) > 0 {
						full, err := c.RecapSource(ctx, cachedAnchor, cached.Receipt, cached.Hits[0].SourceID)
						if stale && (err == nil || full.Text != "") || !stale && (err != nil || full.Text != originals[cached.Hits[0].SourceID]) {
							t.Fatalf("stale full read policy failed, stale%v err%v trace%v", stale, err, trace)
						}
					}
				}
			case 5:
				if err := store.DeleteAllChronicleRollups(ctx); err != nil {
					t.Fatal(err)
				}
				generation++
			case 6:
				if available {
					page, err := c.RecapSourcesPage(ctx, anchor, "", 8)
					if err != nil {
						t.Fatal(err)
					}
					for _, hit := range page.Hits {
						full, err := c.RecapSource(ctx, anchor, page.Receipt, hit.SourceID)
						if err != nil || full.Text != originals[hit.SourceID] {
							t.Fatalf("current full original missing: %v", err)
						}
					}
				}
			case 7:
				if available {
					result, err := c.AskMemory(ctx, question)
					if err != nil {
						t.Fatal(err)
					}
					if !slices.ContainsFunc(result.Sources, func(item assistant.Evidence) bool { return item.SourceID == anchor.SourceID }) {
						handles := []string{}
						for _, e := range result.Sources {
							handles = append(handles, fmt.Sprintf("%d:%s:%s", e.Layer, e.SourceID, e.CapturedAt.Format(time.RFC3339)))
						}
						t.Fatalf("native Ask lost period handle: data%x trace%v zone%s q%q plan%+v want%d:%s got%v", data, trace, zone, question, result.Plan, anchor.Layer, anchor.SourceID, handles)
					}
					result.Sources = []assistant.Evidence{anchor}
					app := &meetingOperationTestApp{posted: make(chan func(), 4)}
					d := &desktop{app: app, client: c, page: "Ask", tokens: themePalette(false), memoryUI: memoryViewState{loaded: true}, askResult: &result}
					_, buttons, _ := memoryWidgetTree(t, d)
					clickMemoryButton(t, buttons, "Show saved sources")
					publish := nextRecapPublication(t, app)
					if b&16 != 0 {
						publish()
						for _, hit := range d.recapSources.page.Hits {
							if hit.Truncated {
								d.loadRecapOriginal(hit.SourceID)
								publish = nextRecapPublication(t, app)
								break
							}
						}
						// With only short sources, queue another page request.
						if d.recapSources.loading == "" {
							d.loadRecapSources(anchor, "")
							publish = nextRecapPublication(t, app)
						}
					}
					switch b >> 6 {
					case 1:
						for i, id := range live {
							if id != "" {
								erase(i, true)
								break
							}
						}
					case 2:
						// Change an actual hourly input. The store advances ancestor
						// revisions even if the controlled generator repeats its wording.
						// Merely toggling dirty metadata could restore an identical row,
						// which would legitimately retain the old provenance receipt.
						for i, id := range live {
							if id == "" {
								continue
							}
							row, found, err := store.GetChronicle(ctx, "hour:"+sourceHour(i).Format("20060102T15Z"))
							if err != nil || !found {
								t.Fatal("missing input for queued regeneration", err)
							}
							row.Text += fmt.Sprintf(" Reprocessed input at step %d.", step)
							if err := store.PutChronicle(ctx, row); err != nil {
								t.Fatal(err)
							}
							break
						}
						generation++
					case 3:
						if b&32 == 0 {
							d.navigatePage("Time")
						} else {
							_, buttons, _ := memoryWidgetTree(t, d)
							clickMemoryButton(t, buttons, "Browse")
						}
					}
					publish()
					if b>>6 == 0 {
						if len(d.recapSources.page.Hits) != len(expected()) {
							t.Fatal("native source control did not open current originals")
						}
					} else if len(d.recapSources.page.Hits) != 0 || d.recapSources.full != nil {
						t.Fatal("native queued originals survived mutation or dismissal")
					}
					d.clearRecapSources()
				}
			}
			settle()
			anchor, available = current()
		}
	})
}
