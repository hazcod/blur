package client

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
	"unsafe"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/chronicle"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

type recapSourceProbe struct {
	*durable.Store
	reads     []string
	afterRead func()
	decoded   map[string]string
}

func (p *recapSourceProbe) GetSourceEvidence(ctx context.Context, id string) (capture.Evidence, bool, error) {
	p.reads = append(p.reads, id)
	evidence, found, err := p.Store.GetSourceEvidence(ctx, id)
	if found && p.decoded != nil {
		p.decoded[id] = evidence.Text()
	}
	if p.afterRead != nil {
		fn := p.afterRead
		p.afterRead = nil
		fn()
	}
	return evidence, found, err
}

func recapSourcesFixture(t *testing.T) (*Client, *recapSourceProbe, map[assistant.Layer]assistant.Evidence, []string, map[string]string) {
	t.Helper()
	return recapSourcesFixtureInZone(t, "UTC")
}

func recapSourcesFixtureInZone(t *testing.T, zone string) (*Client, *recapSourceProbe, map[assistant.Layer]assistant.Evidence, []string, map[string]string) {
	t.Helper()
	t.Setenv("TZ", zone)
	location, err := time.LoadLocation(zone)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s, err := durable.OpenDurable(filepath.Join(t.TempDir(), "originals.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	s.SetChronicleZone(func() string { return zone })
	start := time.Date(2024, 2, 6, 9, 0, 0, 0, time.UTC)
	texts := map[string]string{}
	var ids []string
	for n := 0; n < 10; n++ {
		text := fmt.Sprintf("Original number %d confirms the saved release decision.", n)
		if n < 2 {
			text += strings.Repeat("東京 café 🧭 ", 700) + fmt.Sprintf("Unique end of original %d.", n)
		}
		o, err := capture.SanitizeObservation([]string{"com.apple.mail", "com.apple.Safari", "com.tinyspeck.slackmacgap"}[n%3], "source.example", text, capture.MaxObservationTextBytes)
		if err != nil {
			t.Fatal(err)
		}
		o = o.WithTitle(fmt.Sprintf("Saved original %d", n))
		if err := s.PutObservationAt(ctx, o, start.Add(time.Duration(n)*time.Minute)); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, o.ContentID())
		texts[o.ContentID()] = o.Text()
	}
	sort.Strings(ids)
	hour := chronicle.Summary{HourID: "hour:20240206T09Z", Start: start, End: start.Add(time.Hour), Text: "Reviewed the saved release decision.", SourceIDs: slices.Clone(ids)}
	if err := s.PutChronicle(ctx, hour); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ id, child string }{
		{"day:2024-02-06", hour.HourID}, {"week:2024-W06", "day:2024-02-06"}, {"month:2024-02", "day:2024-02-06"}, {"year:2024", "month:2024-02"},
	} {
		row, err := chronicle.NewRollupPlaceholder(tc.id, zone)
		if err != nil {
			t.Fatal(err)
		}
		if prior, found, err := s.GetChronicleRollup(ctx, tc.id); err != nil {
			t.Fatal(err)
		} else if found {
			row.Epoch, row.Revision = prior.Epoch, prior.Revision
		}
		row.Text = "- Reviewed the saved release decision."
		row.ChildIDs = []string{tc.child}
		row.SourceIDs = slices.Clone(ids)
		row.InputHash = syntheticID("original-fixture", tc.id)
		row.Model = "controlled-local"
		row.GeneratedAt = time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC)
		row.Dirty = false
		row.CoverageVersion = chronicle.RollupCoverageVersion
		if err := s.PutChronicleRollup(ctx, row); err != nil {
			t.Fatalf("fixture %s: %v", tc.id, err)
		}
	}
	probe := &recapSourceProbe{Store: s}
	c := NewClient(probe)
	t.Cleanup(c.Close)
	c.askNow = func() time.Time { return time.Date(2025, 1, 2, 15, 0, 0, 0, time.UTC) }
	anchors := map[assistant.Layer]assistant.Evidence{}
	for _, tc := range []struct {
		layer    assistant.Layer
		from, to string
	}{
		{assistant.LayerHour, "2024-02-06T09:00:00Z", "2024-02-06T10:00:00Z"},
		{assistant.LayerDay, "2024-02-06T00:00:00Z", "2024-02-07T00:00:00Z"},
		{assistant.LayerWeek, "2024-02-05T00:00:00Z", "2024-02-12T00:00:00Z"},
		{assistant.LayerMonth, "2024-02-01T00:00:00Z", "2024-03-01T00:00:00Z"},
		{assistant.LayerYear, "2024-01-01T00:00:00Z", "2025-01-01T00:00:00Z"},
	} {
		from, _ := time.Parse(time.RFC3339, tc.from)
		to, _ := time.Parse(time.RFC3339, tc.to)
		if tc.layer != assistant.LayerHour {
			from = time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, location)
			to = time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, location)
		}
		got, err := c.RetrievePlan(ctx, assistant.Plan{Kind: assistant.KindRecap, Question: "Summarize saved activity", Scope: assistant.Scope{Start: from, End: to, Explicit: true}})
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range got.Evidence {
			if item.Layer == tc.layer {
				anchors[tc.layer] = item
				break
			}
		}
		if anchors[tc.layer].SourceID == "" {
			t.Fatalf("fixture missing recap layer %d: %+v", tc.layer, got)
		}
	}
	probe.reads = nil
	return c, probe, anchors, ids, texts
}

func TestRecapOriginalsPageUsesLiveMembershipAndBoundsHydration(t *testing.T) {
	c, probe, anchors, ids, texts := recapSourcesFixture(t)
	ctx := context.Background()
	for _, layer := range []assistant.Layer{assistant.LayerHour, assistant.LayerDay, assistant.LayerWeek, assistant.LayerMonth, assistant.LayerYear} {
		anchor := anchors[layer]
		anchor.SourceIDs = []string{strings.Repeat("f", 64)} // cached answer metadata is not authority
		anchor.Text = "Untrusted cached answer prose."
		cursor, receipt := "", ""
		var gotIDs []string
		for pageNo := 0; pageNo < 4; pageNo++ {
			before := len(probe.reads)
			page, err := c.RecapSourcesPage(ctx, anchor, cursor, 3)
			if err != nil {
				t.Fatalf("layer%d page%d: %v", layer, pageNo, err)
			}
			if page.Total != 10 || len(page.Hits) > 3 || len(probe.reads)-before != len(page.Hits) || page.Receipt == "" {
				t.Fatalf("unbounded or incomplete original page: %+v reads%d", page, len(probe.reads)-before)
			}
			if receipt != "" && receipt != page.Receipt {
				t.Fatal("unchanged recap changed its page receipt")
			}
			receipt = page.Receipt
			for _, hit := range page.Hits {
				original, found := texts[hit.SourceID]
				if !found || !strings.HasPrefix(original, hit.Text) || len(hit.Text) > 4096 || !utf8.ValidString(hit.Text) || hit.WindowTitle == "" {
					t.Fatalf("original not faithfully hydrated: %+v", hit)
				}
				if (len(original) > 4096) != hit.Truncated {
					t.Fatalf("original preview completeness mislabeled: %+v", hit)
				}
				gotIDs = append(gotIDs, hit.SourceID)
			}
			cursor = page.Next
			if cursor == "" {
				break
			}
		}
		if cursor != "" || !slices.Equal(gotIDs, ids) {
			t.Fatalf("original paging skipped, repeated or forged IDs: %v want%v next%q", gotIDs, ids, cursor)
		}
		for id, text := range texts {
			if len(text) > 4096 {
				full, err := c.RecapSource(ctx, anchor, receipt, id)
				if err != nil || full.Text != text || full.Truncated {
					t.Fatalf("full original not accessible: %+v %v", full, err)
				}
			}
		}
	}
}

func TestRecapOriginalCursorAndFullReadRejectSameTextNewProvenance(t *testing.T) {
	c, probe, anchors, ids, _ := recapSourcesFixture(t)
	ctx := context.Background()
	anchor := anchors[assistant.LayerYear]
	page, err := c.RecapSourcesPage(ctx, anchor, "", 3)
	if err != nil {
		t.Fatal(err)
	}
	row, _, err := probe.GetChronicleRollup(ctx, "year:2024")
	if err != nil {
		t.Fatal(err)
	}
	row.SourceIDs = slices.Clone(ids[1:])
	row.InputHash = syntheticID("changed-inputs")
	if err := probe.PutChronicleRollup(ctx, row); err != nil {
		t.Fatal(err)
	}
	before := len(probe.reads)
	if next, err := c.RecapSourcesPage(ctx, anchor, page.Next, 3); err == nil || len(next.Hits) != 0 {
		t.Fatalf("stale cursor admitted a mixed original list: %+v %v", next, err)
	}
	if _, err := c.RecapSource(ctx, anchor, page.Receipt, ids[1]); err == nil {
		t.Fatal("stale full-read receipt survived provenance replacement")
	}
	if len(probe.reads) != before {
		t.Fatal("rejected provenance still hydrated original payloads")
	}
	fresh, err := c.RecapSourcesPage(ctx, anchor, "", 3)
	if err != nil || fresh.Total != 9 || fresh.Receipt == page.Receipt {
		t.Fatalf("fresh same-text recap did not refresh its supporting originals: %+v %v", fresh, err)
	}
	if _, err := c.RecapSource(ctx, anchor, fresh.Receipt, ids[0]); err == nil {
		t.Fatal("former supporting source remained readable through the recap")
	}
}

func TestRecapOriginalsRejectErasureDuringHydration(t *testing.T) {
	c, probe, anchors, _, _ := recapSourcesFixture(t)
	ctx := context.Background()
	anchor := anchors[assistant.LayerYear]
	probe.afterRead = func() {
		if err := probe.DeleteAllChronicleRollups(ctx); err != nil {
			t.Fatal(err)
		}
	}
	page, err := c.RecapSourcesPage(ctx, anchor, "", 3)
	if err == nil || len(page.Hits) != 0 {
		t.Fatalf("recap invalidated during hydration still published originals: %+v %v", page, err)
	}
}

func TestRecapOriginalsRejectForeignCursorAndMalformedRequests(t *testing.T) {
	c, _, anchors, _, _ := recapSourcesFixture(t)
	ctx := context.Background()
	anchor := anchors[assistant.LayerYear]
	page, err := c.RecapSourcesPage(ctx, anchor, "", 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.RecapSourcesPage(ctx, anchors[assistant.LayerMonth], page.Next, 3); err == nil {
		t.Fatal("year cursor replayed against month recap")
	}
	for _, limit := range []int{0, 9} {
		if _, err := c.RecapSourcesPage(ctx, anchor, "", limit); err == nil {
			t.Fatalf("accepted unbounded page%d", limit)
		}
	}
	for _, cursor := range []string{"bad", page.Receipt + ":-1", page.Receipt + ":03", page.Receipt + ":999", strings.Repeat("f", 64) + ":3"} {
		if _, err := c.RecapSourcesPage(ctx, anchor, cursor, 3); err == nil {
			t.Fatalf("accepted malformed or stale cursor%q", cursor)
		}
	}
	forged := anchor
	forged.SourceID = strings.Repeat("e", 64)
	if _, err := c.RecapSourcesPage(ctx, forged, "", 3); err == nil {
		t.Fatal("forged derived handle opened originals")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.RecapSourcesPage(cancelled, anchor, "", 3); err == nil {
		t.Fatal("cancelled original read succeeded")
	}
}

func TestRecapOriginalsResolveLocalYearBeforeUTCNewYear(t *testing.T) {
	c, _, anchors, ids, _ := recapSourcesFixtureInZone(t, "Asia/Kathmandu")
	anchor := anchors[assistant.LayerYear]
	if got := anchor.CapturedAt.UTC().Format(time.RFC3339); got != "2023-12-31T18:15:00Z" {
		t.Fatalf("fixture did not cross UTC year: %s", got)
	}
	encoded, err := json.Marshal(anchor)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &anchor); err != nil {
		t.Fatal(err)
	} // fixed-offset roundtrip preserves local period
	page, err := c.RecapSourcesPage(context.Background(), anchor, "", 8)
	if err != nil {
		t.Fatal(err)
	}
	last, err := c.RecapSourcesPage(context.Background(), anchor, page.Next, 8)
	got := []string{}
	for _, hit := range append(page.Hits, last.Hits...) {
		got = append(got, hit.SourceID)
	}
	if err != nil || !slices.Equal(got, ids) {
		t.Fatalf("UTC conversion opened the wrong local calendar year: %v %v", got, err)
	}
}

func TestNativeRecapPlanningUsesResolvedZoneAfterProcessZoneWasCached(t *testing.T) {
	_, offset := time.Now().Zone() // force Go's process-local zone to initialize first
	zone, want := "Asia/Kathmandu", "2023-12-31T18:15:00Z"
	if offset == 20700 {
		zone, want = "UTC", "2024-01-01T00:00:00Z"
	}
	c, _, anchors, _, _ := recapSourcesFixtureInZone(t, zone)
	c.askNow = nil // actual runtime clock, not a fixture clock with an inconsistent zone
	result, err := c.AskMemory(context.Background(), "Summarize my activity in 2024")
	if err != nil {
		t.Fatal(err)
	}
	if result.Plan.Scope.Start.UTC().Format(time.RFC3339) != want || !slices.ContainsFunc(result.Sources, func(item assistant.Evidence) bool { return item.SourceID == anchors[assistant.LayerYear].SourceID }) {
		t.Fatalf("native planning kept the cached process zone instead of resolved %s: %+v", zone, result.Plan.Scope)
	}
}

func TestRecapOriginalPreviewReleasesUnshownCapturePartText(t *testing.T) {
	c, probe, anchors, _, _ := recapSourcesFixture(t)
	ctx := context.Background()
	builder, err := capture.NewCaptureBuilder("com.apple.Safari", "source.example", strings.Repeat("b", 64), time.Date(2024, 2, 6, 9, 20, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	defer builder.Abort()
	if err := probe.BeginCapture(ctx, builder.Header()); err != nil {
		t.Fatal(err)
	}
	emit := func(ctx context.Context, parts []capture.CapturePart) error {
		return probe.AppendCaptureParts(ctx, builder.Header(), parts)
	}
	if err := builder.Write(ctx, strings.Repeat("Reviewed release decision source document. ", 700), emit); err != nil {
		t.Fatal(err)
	}
	summary, err := builder.Finish(ctx, capture.CaptureComplete, emit)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := probe.PublishCaptureWithSourceReceipt(ctx, summary)
	if err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 32; n++ {
		more, err := probe.IndexPendingCaptures(ctx, 8)
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
		if n == 31 {
			t.Fatal("capture index did not settle")
		}
	}
	row, found, err := probe.GetChronicleRollup(ctx, "year:2024")
	if err != nil || !found {
		t.Fatal("missing year fixture", err)
	}
	row.SourceIDs = []string{receipt.SourceID}
	row.InputHash = syntheticID("multipart-original-preview")
	row.Sampled = true
	if err := probe.PutChronicleRollup(ctx, row); err != nil {
		t.Fatal(err)
	}
	probe.decoded = map[string]string{}
	page, err := c.RecapSourcesPage(ctx, anchors[assistant.LayerYear], "", 8)
	if err != nil || len(page.Hits) != 1 {
		t.Fatalf("multipart original unavailable: %+v %v", page, err)
	}
	original := probe.decoded[receipt.SourceID]
	if len(original) <= 4096 {
		t.Fatal("fixture did not hydrate a large capture part")
	}
	if unsafe.StringData(page.Hits[0].Text) == unsafe.StringData(original) {
		t.Fatal("bounded preview retains the unshown capture-part backing string")
	}
}
