package client

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/deletion"
)

func recapSourceDesktop(t *testing.T) (*desktop, *meetingOperationTestApp, *recapSourceProbe, []string, map[string]string) {
	t.Helper()
	c, probe, anchors, ids, texts := recapSourcesFixture(t)
	app := &meetingOperationTestApp{posted: make(chan func(), 16)}
	result := &assistant.Result{Plan: assistant.Plan{Kind: assistant.KindRecap}, Sources: []assistant.Evidence{anchors[assistant.LayerYear]}, Answer: assistant.Answer{Text: "Saved release decision."}}
	d := &desktop{app: app, client: c, page: "Ask", tokens: themePalette(false), memoryUI: memoryViewState{loaded: true}, askResult: result}
	t.Cleanup(d.clearRecapSources)
	return d, app, probe, ids, texts
}

func nextRecapPublication(t *testing.T, app *meetingOperationTestApp) func() {
	t.Helper()
	select {
	case fn := <-app.posted:
		return fn
	case <-time.After(5 * time.Second):
		t.Fatal("source read did not queue native publication")
		return nil
	}
}

func TestNativeRecapOriginalActionsPageAndKeepOnlyOneFullPayload(t *testing.T) {
	d, app, _, ids, texts := recapSourceDesktop(t)
	_, buttons, _ := memoryWidgetTree(t, d)
	clickMemoryButton(t, buttons, "Show saved sources")
	app.finish(t)
	if len(d.recapSources.page.Hits) != 8 {
		t.Fatalf("first native page did not stay bounded: %d", len(d.recapSources.page.Hits))
	}
	_, buttons, _ = memoryWidgetTree(t, d)
	clickMemoryButton(t, buttons, "Load more saved sources")
	app.finish(t)
	var got []string
	for _, hit := range d.recapSources.page.Hits {
		got = append(got, hit.SourceID)
	}
	if !slices.Equal(got, ids) {
		t.Fatalf("native pagination lost original IDs: %v want%v", got, ids)
	}
	for n := 0; n < 2; n++ {
		_, buttons, _ = memoryWidgetTree(t, d)
		clickMemoryButton(t, buttons, "Read saved source")
		app.finish(t)
		full := d.recapSources.full
		if full == nil || full.Text != texts[full.SourceID] || len(full.Text) <= 4096 {
			t.Fatal("native original action did not read the selected full source")
		}
		for _, preview := range d.recapSources.page.Hits {
			if len(preview.Text) > 4096 {
				t.Fatal("full originals accumulated in preview rows")
			}
		}
	}
	_, buttons, rendered := memoryWidgetTree(t, d)
	if len(buttons["Show source preview"]) != 1 || !strings.Contains(rendered, "Unique end of original") {
		t.Fatal("selected original was not expanded in its recap")
	}
	clickMemoryButton(t, buttons, "Show source preview")
	_, buttons, rendered = memoryWidgetTree(t, d)
	if d.recapSources.full != nil || strings.Contains(rendered, "Unique end of original") {
		t.Fatal("returning to preview retained the full payload")
	}
	clickMemoryButton(t, buttons, "Hide saved sources")
	if d.recapSources.anchor.SourceID != "" || len(d.recapSources.page.Hits) != 0 {
		t.Fatal("closing sources retained native source payloads")
	}
}

func TestNativeRecapSourcePublicationCannotCrossSourceErasure(t *testing.T) {
	d, app, probe, ids, _ := recapSourceDesktop(t)
	deleter, err := deletion.NewService(probe.Store, nil)
	if err != nil {
		t.Fatal(err)
	}
	d.client.WithDeletion(deleter)
	_, buttons, _ := memoryWidgetTree(t, d)
	clickMemoryButton(t, buttons, "Show saved sources")
	publish := nextRecapPublication(t, app)
	if _, err := d.client.DeleteSource(ids[0]); err != nil {
		t.Fatal(err)
	}
	publish()
	_, _, rendered := memoryWidgetTree(t, d)
	if len(d.recapSources.page.Hits) != 0 || d.recapSources.full != nil || !strings.Contains(rendered, "Memory changed") {
		t.Fatalf("queued source read survived source erasure: %s", rendered)
	}
}

func TestNativeRecapSourcePublicationCannotCrossCloseOrNewAnswer(t *testing.T) {
	for _, action := range []string{"close", "new answer", "invalidate"} {
		t.Run(action, func(t *testing.T) {
			d, app, _, _, _ := recapSourceDesktop(t)
			_, buttons, _ := memoryWidgetTree(t, d)
			clickMemoryButton(t, buttons, "Show saved sources")
			publish := nextRecapPublication(t, app)
			switch action {
			case "close":
				d.clearRecapSources()
			case "new answer":
				d.clearRecapSources()
				d.askResult = &assistant.Result{Answer: assistant.Answer{Text: "A different answer."}}
			case "invalidate":
				d.invalidateAsk()
			}
			publish()
			if len(d.recapSources.page.Hits) != 0 || d.recapSources.full != nil || d.recapSources.anchor.SourceID != "" {
				t.Fatal("obsolete async sources repopulated a closed or replaced answer")
			}
		})
	}
}

func TestNativeRecapErasureWhileReadingShowsCurrentRecovery(t *testing.T) {
	d, app, probe, _, _ := recapSourceDesktop(t)
	probe.afterRead = func() {
		if err := probe.DeleteAllChronicleRollups(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	_, buttons, _ := memoryWidgetTree(t, d)
	clickMemoryButton(t, buttons, "Show saved sources")
	app.finish(t)
	_, buttons, rendered := memoryWidgetTree(t, d)
	if !strings.Contains(rendered, "Ask again to see its current sources") || len(buttons["Try sources again"]) != 0 || len(d.recapSources.page.Hits) != 0 {
		t.Fatalf("changed recap kept stale originals or misleading retry: %s", rendered)
	}
}

func TestRecapHourEvidenceCarriesSupportingOriginalsAndLegacyCoverage(t *testing.T) {
	_, _, anchors, ids, _ := recapSourcesFixture(t)
	hour := anchors[assistant.LayerHour]
	if !slices.Equal(hour.SourceIDs, ids) || !hour.CoverageLimited {
		t.Fatalf("hour recap lost its supporting IDs or unknown legacy coverage: %+v", hour)
	}
}

func TestNativeRecapQueuedPublicationRejectsReplacement(t *testing.T) {
	for _, full := range []bool{false, true} {
		t.Run(map[bool]string{false: "page", true: "full"}[full], func(t *testing.T) {
			d, app, probe, ids, texts := recapSourceDesktop(t)
			anchor := d.askResult.Sources[0]
			d.loadRecapSources(anchor, "")
			publish := nextRecapPublication(t, app)
			if full {
				publish()
				for _, id := range ids {
					if len(texts[id]) > 4096 {
						d.loadRecapOriginal(id)
						break
					}
				}
				publish = nextRecapPublication(t, app)
			}
			epoch, _ := d.client.BeginMemoryRead()
			row, found, err := probe.GetChronicleRollup(context.Background(), "year:2024")
			if err != nil || !found {
				t.Fatal(err)
			}
			row.SourceIDs = slices.Clone(ids[1:])
			row.InputHash = syntheticID("replacement while publication queued")
			if err := probe.PutChronicleRollup(context.Background(), row); err != nil {
				t.Fatal(err)
			}
			if !d.client.MemoryReadCurrent(epoch) {
				t.Fatal("unexpected client epoch change")
			}
			publish()
			if len(d.recapSources.page.Hits) != 0 || d.recapSources.full != nil {
				t.Fatalf("obsolete recap originals published after durable replacement: rows=%d full=%v", len(d.recapSources.page.Hits), d.recapSources.full != nil)
			}
		})
	}
}
func TestNativeRecapQueuedPublicationAfterNavigation(t *testing.T) {
	d, app, _, _, _ := recapSourceDesktop(t)
	d.loadRecapSources(d.askResult.Sources[0], "")
	publish := nextRecapPublication(t, app)
	d.navigatePage("Time")
	publish()
	if len(d.recapSources.page.Hits) != 0 {
		t.Fatalf("closed Ask view retained %d source payloads", len(d.recapSources.page.Hits))
	}
}
func TestNativeRecapBrowseReleasesExpandedOriginal(t *testing.T) {
	d, app, _, ids, texts := recapSourceDesktop(t)
	d.loadRecapSources(d.askResult.Sources[0], "")
	nextRecapPublication(t, app)()
	for _, id := range ids {
		if len(texts[id]) > 4096 {
			d.loadRecapOriginal(id)
			break
		}
	}
	nextRecapPublication(t, app)()
	if d.recapSources.full == nil {
		t.Fatal("fixture did not expand")
	}
	d.askQuestion = ""
	_, buttons, _ := memoryWidgetTree(t, d)
	clickMemoryButton(t, buttons, "Browse")
	if d.askResult != nil {
		t.Fatal("Browse did not dismiss answer")
	}
	if d.recapSources.full != nil || len(d.recapSources.page.Hits) != 0 {
		t.Fatalf("dismissed answer retained rows=%d full=%v", len(d.recapSources.page.Hits), d.recapSources.full != nil)
	}
}
