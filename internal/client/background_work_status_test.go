package client

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/chronicle"
	"github.com/hazcod/blur/internal/knowledge/enrich"
	"github.com/hazcod/blur/internal/model"
	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/power"
)

func TestBackgroundWorkStatusIsBoundedContentFreeAndSafeBeforeConfiguration(t *testing.T) {
	var absent *Client
	for _, c := range []*Client{absent, {data: statusNoReadData{}}, {localAI: &localAIController{settings: LocalAISettings{Enabled: true, AutomaticLibrary: true, BackgroundSummaries: true, Provider: ProviderApple}, needsRepair: true}}} {
		got := c.BackgroundWorkStatus()
		if len(got.Groups) != 3 {
			t.Fatal("missing bounded job groups")
		}
		for _, group := range got.Groups {
			if group.Queued < 0 || group.Running < 0 {
				t.Fatal("invalid status counts")
			}
		}
	}
}

type statusLibraryFailure struct{ receiptLibraryGenerator }

func (*statusLibraryFailure) Identity(context.Context) (string, error) {
	return "", model.ErrAppleUnavailable
}

type statusRecapFailure struct{}

func (statusRecapFailure) CountInputTokens(context.Context, chronicle.Packed) (int, error) {
	return 0, model.ErrAppleUnavailable
}
func (statusRecapFailure) Generate(context.Context, chronicle.Packed, int) (chronicle.Summary, error) {
	return chronicle.Summary{}, model.ErrAppleUnavailable
}

type statusBackgroundPolicy struct{}

func (statusBackgroundPolicy) Effective(context.Context) (power.Effective, error) {
	return power.Resolve(power.Config{Mode: power.Balanced}, power.SystemState{})
}

func TestBackgroundWorkDisabledOverridesActualFailedWorkerState(t *testing.T) {
	f := newControllerFixture(t)
	ctx := context.Background()
	observation, err := capture.SanitizeObservation("editor.app", "synthetic.example", "Synthetic saved project notes", 4096)
	if err != nil {
		t.Fatal(err)
	}
	at := f.now.Add(time.Minute)
	if err := f.store.PutObservationAt(ctx, observation, at); err != nil {
		t.Fatal(err)
	}
	library, err := enrich.NewService(f.store, f.store, &statusLibraryFailure{}, enrich.Options{Policy: statusBackgroundPolicy{}})
	if err != nil {
		t.Fatal(err)
	}
	defer library.Close()
	if err := library.Backfill(ctx); err == nil {
		t.Fatal("fixture Library worker did not fail")
	}
	recaps := chronicle.NewWorker(f.store, statusBackgroundPolicy{}, statusRecapFailure{}, chronicle.WorkerConfig{Enabled: true, Provider: power.LocalProvider})
	defer recaps.Close()
	if err := recaps.Process(ctx, chronicle.Hour{Start: f.now, End: f.now.Add(time.Hour), Evidence: []chronicle.Evidence{{Observation: observation, At: at, Salience: 10}}}); err == nil {
		t.Fatal("fixture recap worker did not fail")
	}
	controller := &localAIController{settings: LocalAISettings{Enabled: true, Provider: ProviderApple, Model: AppleSystemModel}, libraryWorker: library, chronicleWorker: recaps}
	c := &Client{localAI: controller, data: statusNoReadData{}}
	for _, enabled := range []bool{false, true, false} {
		controller.mu.Lock()
		controller.settings.AutomaticLibrary, controller.settings.BackgroundSummaries = enabled, enabled
		controller.mu.Unlock()
		snapshot := c.BackgroundWorkStatus()
		for _, group := range snapshot.Groups[:2] {
			if enabled {
				if !group.Enabled || group.State != "unavailable" || group.Reason != "provider_unavailable" {
					t.Fatal("enabled worker lost actual failure", group)
				}
			} else if group.Enabled || group.State != "disabled" || group.Reason != "" {
				t.Fatal("explicit Off was overridden by stale worker failure", group)
			}
			if group.LastErrorCode != "provider_unavailable" || group.LastErrorAt.IsZero() {
				t.Fatal("Off erased truthful historical failure", group)
			}
		}
	}
}
func TestBackgroundWorkStatusSnapshotsNoteCountsWithoutSourceOrQueueMutation(t *testing.T) {
	secret := "https://private.invalid/source-secret"
	job := noteProcessingJob{key: noteProcessingKey{sourceID: secret}}
	worker := &noteProcessingWorker{jobs: []noteProcessingJob{job, job}, active: &job, status: NoteProcessingStatus{State: "processing"}, completed: map[noteProcessingKey]bool{}}
	c := &Client{noteProcessing: worker}
	got := c.BackgroundWorkStatus()
	var note BackgroundWorkGroup
	for _, g := range got.Groups {
		if g.ID == "notes" {
			note = g
		}
	}
	if note.Queued != 2 || note.Running != 1 {
		t.Fatal("actual bounded note counts lost", note)
	}
	encoded, err := json.Marshal(got)
	if err != nil || strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), "private.invalid") {
		t.Fatal("background telemetry leaked source content", err)
	}
	got.Groups[0].State = "mutated"
	if c.BackgroundWorkStatus().Groups[0].State == "mutated" || len(worker.jobs) != 2 || worker.active == nil {
		t.Fatal("snapshot mutated queue or aliases live state")
	}
}

func TestBackgroundWorkStatusTracksRealNoteCompletionAndDoesNoWorkOnRead(t *testing.T) {
	c, _, extractor, _ := noteProcessingFixture(t)
	if _, err := c.AddNote("After the meeting send the revised draft."); err != nil {
		t.Fatal(err)
	}
	select {
	case <-extractor.started:
	case <-time.After(time.Second):
		t.Fatal("fake extraction did not start")
	}
	for range 100 {
		snapshot := c.BackgroundWorkStatus()
		if snapshot.Groups[2].State != "running" || snapshot.Groups[2].Running != 1 || snapshot.Groups[2].Completed != 0 {
			t.Fatal("actual active note missing", snapshot.Groups[2])
		}
	}
	if extractor.calls.Load() != 1 {
		t.Fatal("status read triggered generation")
	}
	extractor.unblock()
	waitNoteProcessing(t, c, func(s NoteProcessingStatus) bool { return s.State == "ready" && s.Pending == 0 })
	group := c.BackgroundWorkStatus().Groups[2]
	if group.State != "idle" || group.Completed != 1 || group.LastCompletedAt.IsZero() {
		t.Fatal("real note completion not visible", group)
	}
}

// An unexpected repository read through this nonnil port panics. Status reads
// must not touch Data, including while a model setting is enabled.
type statusNoReadData struct{ Data }

func TestBackgroundWorkStatusRejectsUnknownReasonText(t *testing.T) {
	private := "https://private.invalid/model-prompt"
	c := &Client{data: statusNoReadData{}, localAI: &localAIController{settings: LocalAISettings{Enabled: true, Provider: ProviderApple}, service: notes.NewService(nil, &noteProcessingExtractor{}, nil)}, noteProcessing: &noteProcessingWorker{status: NoteProcessingStatus{State: "unavailable", ErrorCode: private}, work: noteWorkTelemetry{lastErrorCode: private}}}
	got := c.BackgroundWorkStatus()
	if got.Groups[2].Reason != "processing_failed" || got.Groups[2].LastErrorCode != "processing_failed" {
		t.Fatal("unknown backend status escaped closed vocabulary")
	}
}

func TestBackgroundWorkStatusDoesNotClaimIdleNotesWithoutSelectedExtractor(t *testing.T) {
	c := &Client{localAI: &localAIController{settings: LocalAISettings{Enabled: true, Provider: ProviderApple}}, noteProcessing: &noteProcessingWorker{status: NoteProcessingStatus{State: "ready"}, work: noteWorkTelemetry{completed: 3}}}
	group := c.BackgroundWorkStatus().Groups[2]
	if group.State != "unavailable" || group.Reason != "model_unavailable" || group.Completed != 3 {
		t.Fatal("retired or missing extractor claimed ready", group)
	}
}
