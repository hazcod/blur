package client

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/knowledge/derive"
	"github.com/hazcod/blur/internal/knowledge/enrich"
	"github.com/hazcod/blur/internal/meetings"
	"github.com/hazcod/blur/internal/platform/darwin"
	"github.com/hazcod/blur/internal/storage/durable"
)

func TestLibraryMountedVoiceAttributionUsesSavedEventTimeAndKeepsSourceLinks(t *testing.T) {
	c, store, model, meeting := summaryFixture(t, 1)
	ctx := context.Background()
	page, err := store.MeetingRecords(ctx, meeting.ID, meetings.PageRequest{})
	if err != nil || len(page.Records) != 1 {
		t.Fatal(page, err)
	}
	id := page.Records[0].SourceID
	source, found, err := store.GetObservation(ctx, id)
	if err != nil || !found {
		t.Fatal(found, err)
	}
	if err := store.ApplyEnrichment(ctx, enrich.Chunk{SourceID: id, ModelKey: enrich.ModelKey("synthetic-attribution"), End: len(source.Text()), Next: len(source.Text()), Complete: true, Proposals: []derive.Proposal{{Kind: knowledge.Project, Name: "Aurora", Fact: "Aurora launches after review.", SourceID: id}}}); err != nil {
		t.Fatal(err)
	}
	library, err := c.Knowledge("")
	if err != nil || len(library.Entities) != 1 {
		t.Fatal(library, err)
	}
	wantTime := darwin.LocalizedDateTime(meeting.StartedAt.Add(time.Second), localZoneName())
	if wantTime == "" {
		t.Fatal("synthetic event time could not be formatted")
	}
	d := &desktop{client: c, libraryLoaded: true, library: library}
	text := mountedLibraryText(t, d.libraryView())
	for _, want := range []string{"Inferred from voice notes", wantTime, "Aurora launches after review.", "View source"} {
		if !strings.Contains(text, want) {
			t.Errorf("mounted Library omits %q:\n%s", want, text)
		}
	}
	for _, hidden := range []string{id, source.Domain(), ".meeting.local"} {
		if strings.Contains(text, hidden) {
			t.Errorf("Library attribution exposed internal source metadata %q", hidden)
		}
	}
	entity, err := c.InferredKnowledge(ctx, library.Entities[0].ID)
	if err != nil || len(entity.ActivitySourceIDs) != 1 || entity.ActivitySourceIDs[0] != id || entity.Facts[0].Sources[0].ID != id {
		t.Fatal("display attribution lost original source authority", entity, err)
	}
	d.libraryInspect = &entity
	if detail := mountedLibraryText(t, d.libraryView()); !strings.Contains(detail, wantTime) || !strings.Contains(detail, "Inspect source 1") {
		t.Fatal("subject details lost dated provenance or original source link", detail)
	}
	app := &libraryRefreshApp{posted: make(chan func(), 2)}
	d.app = app
	d.inspectLibrarySource(id)
	publishLibraryCallback(t, app)
	if detail := mountedLibraryText(t, d.libraryView()); strings.Contains(detail, source.Domain()) || !strings.Contains(detail, "Captured source") || !strings.Contains(detail, source.Text()) || !strings.Contains(detail, wantTime) {
		t.Fatal("opened voice source lost its event time or still exposes an internal key", detail)
	}
	if model.calls != 0 {
		t.Fatal("provenance rendering invoked a model")
	}
	stored, found, err := store.GetInferredKnowledge(ctx, entity.ID)
	if err != nil || !found || !stored.Inference.SourceTime.IsZero() {
		t.Fatal("display metadata changed stored provenance", stored, err)
	}
	// The existing source-time classifier marks retention baselines as unknown.
	// Even a nonzero stored baseline must not become a displayed event date.
	c.data = &libraryAttributionReadStore{Store: store, unknownTime: true}
	undated, err := c.InferredKnowledge(ctx, entity.ID)
	if err != nil {
		t.Fatal(err)
	}
	if text := mountedLibraryText(t, d.inferredEntityCard(undated)); strings.Contains(text, wantTime) || strings.Contains(text, source.Domain()) || !strings.Contains(text, "Inferred from voice notes") {
		t.Fatal("unknown source time became an invented date", text)
	}
}

type libraryAttributionReadStore struct {
	*durable.Store
	reads       atomic.Int32
	unknownTime bool
}

func (s *libraryAttributionReadStore) IsObservationTimeKnown(at time.Time) bool {
	return !s.unknownTime && s.Store.IsObservationTimeKnown(at)
}

func (s *libraryAttributionReadStore) GetSourceEvidenceWithCaptureTime(ctx context.Context, id string) (capture.Evidence, time.Time, bool, error) {
	s.reads.Add(1)
	return s.Store.GetSourceEvidenceWithCaptureTime(ctx, id)
}

func TestLibraryAttributionReadsOnlyVisibleSourcesOnceBeforeRendering(t *testing.T) {
	c, store, at := todayTaskClient(t)
	ctx := context.Background()
	for i := range 4 {
		text := fmt.Sprintf("Synthetic saved source %d", i)
		source, err := capture.SanitizeObservation("sh.blur.meetings", fmt.Sprintf("opaque-%d.meeting.local", i), text, 1024)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.PutObservationAt(ctx, source, at); err != nil {
			t.Fatal(err)
		}
		names := []string{fmt.Sprintf("Other %d", i)}
		if i == 0 {
			names = []string{"Chosen Aurora", "Chosen Mira"}
		}
		var proposals []derive.Proposal
		for _, name := range names {
			proposals = append(proposals, derive.Proposal{Kind: knowledge.Topic, Name: name, Fact: text, SourceID: source.ContentID()})
		}
		if err := store.ApplyEnrichment(ctx, enrich.Chunk{SourceID: source.ContentID(), ModelKey: enrich.ModelKey("synthetic-attribution"), End: len(text), Next: len(text), Complete: true, Proposals: proposals}); err != nil {
			t.Fatal(err)
		}
	}
	observed := &libraryAttributionReadStore{Store: store}
	c.data = observed
	page, err := c.libraryDirectoryPage(ctx, "Chosen", "", 0)
	if err != nil || len(page.Entities) != 2 {
		t.Fatal(page, err)
	}
	if reads := observed.reads.Load(); reads != 1 {
		t.Fatalf("visible subjects share one source, but background attribution made %d reads", reads)
	}
	d := &desktop{client: c, library: page.Page, libraryLoaded: true}
	text := mountedLibraryText(t, d.libraryView())
	if observed.reads.Load() != 1 || !strings.Contains(text, "Inferred from voice notes") {
		t.Fatal("rendering performed source reads or lost prepared attribution", observed.reads.Load(), text)
	}
}

func TestLibraryMountedUnknownVoiceTimeShowsSourceKindWithoutInventingDate(t *testing.T) {
	entity := knowledge.Entity{ID: "voice-topic", Name: "Aurora", Kind: knowledge.Project, State: knowledge.Proposed, Inference: &knowledge.Inference{AppID: "sh.blur.meetings", Domain: "opaque-internal-source.meeting.local"}}
	d := &desktop{}
	text := mountedLibraryText(t, d.inferredEntityCard(entity))
	if !strings.Contains(text, "Inferred from voice notes") || strings.Contains(text, entity.Inference.Domain) || strings.Contains(text, "2026") || strings.Contains(text, "0001") {
		t.Fatal("undated voice provenance showed an internal key or a fabricated date", text)
	}
}
