package client

import (
	"strings"
	"testing"
	"time"

	"github.com/golang-gui/goui/ui"
	"github.com/hazcod/blur/internal/model"
)

func TestBackgroundWorkPresentationExplainsActualStatesAndCounts(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 30, 0, 0, time.UTC)
	snapshot := BackgroundWorkSnapshot{Groups: []BackgroundWorkGroup{
		{ID: "library", Enabled: true, State: "running", Running: 1, Queued: 2, Completed: 7, LastCompletedAt: now},
		{ID: "recaps", Enabled: true, State: "deferred", Reason: "foreground", Queued: 1},
		{ID: "notes", Enabled: true, State: "unavailable", Reason: "model_budget_exceeded", LastErrorCode: "model_budget_exceeded", LastErrorAt: now},
	}, Apple: model.AppleInferenceSnapshot{Active: "interactive", BackgroundWaiting: 2}}
	got := formatBackgroundWork(snapshot, true, true, true)
	if !got.ShowModel || got.Model.State != "Working on your request" || !strings.Contains(got.Model.Detail, "2 background requests waiting") {
		t.Fatal("model contention not explained", got.Model)
	}
	if got.Rows[0].State != "Working" || !strings.Contains(got.Rows[0].Detail, "2 sources pending") || !strings.Contains(got.Rows[0].History, "7 chunks completed") {
		t.Fatal("Library counts not exact", got.Rows[0])
	}
	if got.Rows[1].State != "Waiting for your request to finish" || !strings.Contains(got.Rows[1].Detail, "1 queued") {
		t.Fatal("foreground deferral not visible", got.Rows[1])
	}
	if got.Rows[2].State != "Needs attention" || !strings.Contains(got.Rows[2].Problem, "model’s input limit") {
		t.Fatal("actual processing error not visible", got.Rows[2])
	}
}

func TestBackgroundWorkPresentationNeverInventsProgressOrLeaksBackendText(t *testing.T) {
	for _, state := range []string{"disabled", "idle", "queued", "running", "deferred", "unavailable", "https://private.invalid/prompt"} {
		got := formatBackgroundWork(BackgroundWorkSnapshot{Groups: []BackgroundWorkGroup{{ID: "library", Enabled: true, State: state, Reason: "https://private.invalid/prompt", LastErrorCode: "secret-prompt", LastErrorAt: time.Now()}}}, false, false, true)
		row := got.Rows[0]
		text := row.State + row.Detail + row.History + row.Problem
		for _, forbidden := range []string{"https://", "secret-prompt", "%", "0 queued", "0 chunks completed", "up to date", "all sources"} {
			if strings.Contains(strings.ToLower(text), forbidden) {
				t.Fatalf("%q claims false progress or reveals private data: %q", state, text)
			}
		}
	}
	got := formatBackgroundWork(BackgroundWorkSnapshot{}, true, false, false)
	if got.ShowModel || got.Rows[0].State != "Unavailable" || !strings.Contains(got.Rows[0].Detail, "encrypted local storage") {
		t.Fatal("missing storage presented as idle/off", got)
	}
}

func TestBackgroundWorkPresentationDistinguishesPowerDeferralAndModelRetirement(t *testing.T) {
	got := formatBackgroundWork(BackgroundWorkSnapshot{Groups: []BackgroundWorkGroup{{ID: "recaps", Enabled: true, State: "deferred", Reason: "policy"}}, Apple: model.AppleInferenceSnapshot{Active: "background", Group: "library", Retiring: true}}, true, true, true)
	if got.Rows[1].State != "Paused by power settings" || got.Model.State != "Yielding to your request" {
		t.Fatal("distinct causes conflated", got)
	}
	got = formatBackgroundWork(BackgroundWorkSnapshot{Apple: model.AppleInferenceSnapshot{Active: "idle"}}, true, false, true)
	if got.Model.State != "Off" {
		t.Fatal("disabled Apple AI presented as available", got.Model)
	}
	got = formatBackgroundWork(BackgroundWorkSnapshot{Groups: []BackgroundWorkGroup{{ID: "recaps", Enabled: true, State: "unavailable", Reason: "policy", LastErrorCode: "policy", LastErrorAt: time.Now()}}}, false, false, true)
	if got.Rows[1].State != "Power limit reached" || !strings.Contains(got.Rows[1].Detail, "last attempt was skipped") || strings.Contains(got.Rows[1].Detail, "resume") {
		t.Fatal("skipped recap claimed a live paused/retained queue", got.Rows[1])
	}
}

func TestBackgroundWorkLibraryDeclineShowsPartialCoverageWithoutBlockingOtherWork(t *testing.T) {
	for _, state := range []string{"idle", "running", "queued", "disabled"} {
		t.Run(state, func(t *testing.T) {
			group := BackgroundWorkGroup{ID: "library", Enabled: state != "disabled", State: state, CoverageLimited: true, LastErrorCode: "model_refused", LastErrorAt: time.Now()}
			row := formatBackgroundWorkGroup(group)
			if state == "idle" && row.State != "Partial coverage" {
				t.Fatal("declined capture was presented as complete or blocked Library", row)
			}
			if state == "running" && row.State != "Working" || state == "queued" && row.State != "Queued" || state == "disabled" && row.State != "Off" {
				t.Fatal("declined capture hid actual worker state", row)
			}
			if state != "disabled" && !strings.Contains(row.Detail, "declined") {
				t.Fatal("incomplete Library coverage is hidden", row)
			}
			if !strings.Contains(row.Problem, "Last issue") {
				t.Fatal("refusal history disappeared", row)
			}
		})
	}
}

type backgroundWorkApp struct {
	quickDashboardApp
	timers  []func()
	updates int
}

func (a *backgroundWorkApp) TimeoutFunc(_ time.Duration, fn func()) *ui.Timer {
	a.timers = append(a.timers, fn)
	return nil
}
func (a *backgroundWorkApp) RequestUpdate() { a.updates++ }

func TestBackgroundWorkPollingUpdatesOnlyItsSubtreeAndStopsOutsideSettings(t *testing.T) {
	client := &Client{data: statusNoReadData{}, localAI: &localAIController{settings: LocalAISettings{Enabled: true, Provider: ProviderApple, AutomaticLibrary: true}}}
	app := &backgroundWorkApp{}
	d := &desktop{client: client, app: app, dashboardOpen: true, page: "Settings", settingsGroup: settingsGroupAI}
	d.backgroundWorkView()
	d.backgroundWorkView()
	if len(app.timers) != 1 {
		t.Fatal("duplicate polling timer", len(app.timers))
	}
	changes := 0
	handle := d.backgroundWorkUI.presentation.Connect(func() { changes++ })
	defer handle.Disconnect()
	app.timers[0]()
	if changes != 0 || app.updates != 0 || len(app.timers) != 2 {
		t.Fatal("unchanged snapshot rebuilt Settings", changes, app.updates, len(app.timers))
	}
	client.localAI.mu.Lock()
	client.localAI.settings.AutomaticLibrary = false
	client.localAI.mu.Unlock()
	app.timers[1]()
	if changes != 1 || app.updates != 0 {
		t.Fatal("status change did not stay local", changes, app.updates)
	}
	d.page = "Library"
	app.timers[2]()
	if len(app.timers) != 3 || d.backgroundWorkUI.polling {
		t.Fatal("polling continued in Library")
	}
	for _, visible := range []bool{false, true} {
		d.page, d.dashboardOpen, d.settingsGroup = "Settings", visible, settingsGroupCapture
		d.ensureBackgroundWorkPolling()
	}
	if len(app.timers) != 3 {
		t.Fatal("hidden or unrelated Settings started polling")
	}
}
