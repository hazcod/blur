package client

import (
	"fmt"
	"strings"
	"time"

	"github.com/golang-gui/goui/layout"
	"github.com/golang-gui/goui/ui"
	"github.com/hazcod/blur/internal/model"
)

type backgroundWorkRow struct {
	Label, State, Detail, History, Problem string
}

// Fixed rows and absolute completion times keep layout and text stable between
// actual changes. No percent complete, unscanned source total, or made-up ETA.
type backgroundWorkPresentation struct {
	ShowModel bool
	Model     backgroundWorkRow
	Rows      [3]backgroundWorkRow
}

type backgroundWorkViewState struct {
	presentation ui.State[backgroundWorkPresentation]
	polling      bool
}

func (d *desktop) backgroundWorkView() ui.View {
	d.refreshBackgroundWorkStatus()
	d.ensureBackgroundWorkPolling()
	// Only this composition observes the snapshot. The timer never requests an
	// app rebuild, reloads saved sources, or replaces the Settings scroll view.
	return ui.Build(func() ui.View {
		value := d.backgroundWorkUI.presentation.Get()
		intro := "Live activity from Blur’s automatic processing."
		if value.ShowModel {
			intro = "Live activity. Your requests take priority over automatic Apple model work."
		}
		rows := []ui.View{wrapLabel("Background work").Style(styleSectionTitle), wrapLabel(intro).Style(styleMeta)}
		appendRow := func(row backgroundWorkRow) {
			body := []ui.View{
				ui.HBox(wrapLabel(row.Label).MainWeight(1), wrapLabel(row.State)).Spacing(12).CrossAlign(layout.CrossStart),
				wrapLabel(row.Detail).Style(styleMeta),
			}
			if row.History != "" {
				body = append(body, wrapLabel(row.History).Style(styleMeta))
			}
			if row.Problem != "" {
				body = append(body, wrapLabel(row.Problem))
			}
			rows = append(rows, hairline(), ui.VBox(body...).Spacing(4).CrossAlign(layout.CrossStretch))
		}
		if value.ShowModel {
			appendRow(value.Model)
		}
		for _, row := range value.Rows {
			appendRow(row)
		}
		rows = append(rows, wrapLabel("Counts may reset after changing AI settings or restarting Blur. Library pending counts include only discovered sources, including one currently being processed.").Style(styleMeta))
		return surfaceBox(14, 10, rows...)
	}).ID("background-work-status")
}

func (d *desktop) refreshBackgroundWorkStatus() {
	settings := LocalAISettings{}
	if d.client != nil {
		settings = d.client.LocalAISettings()
	}
	value := formatBackgroundWork(d.client.BackgroundWorkStatus(), effectiveProvider(settings) == ProviderApple, settings.Enabled, d.client != nil && d.client.data != nil)
	if value != d.backgroundWorkUI.presentation.Peek() {
		d.backgroundWorkUI.presentation.Set(value)
	}
}

func (d *desktop) ensureBackgroundWorkPolling() {
	if d == nil || d.app == nil || d.backgroundWorkUI.polling || !d.dashboardOpen || d.page != "Settings" || d.settingsGroup != settingsGroupAI {
		return
	}
	d.backgroundWorkUI.polling = true
	d.app.TimeoutFunc(time.Second, func() {
		d.backgroundWorkUI.polling = false
		if !d.dashboardOpen || d.page != "Settings" || d.settingsGroup != settingsGroupAI {
			return
		}
		d.refreshBackgroundWorkStatus()
		d.ensureBackgroundWorkPolling()
	})
}

func formatBackgroundWork(snapshot BackgroundWorkSnapshot, appleSelected, appleEnabled, storageReady bool) backgroundWorkPresentation {
	out := backgroundWorkPresentation{}
	ids := [3]string{"library", "recaps", "notes"}
	labels := [3]string{"Automatic Library", "Hourly recaps", "Note suggestions"}
	for i, id := range ids {
		group := BackgroundWorkGroup{ID: id, State: "unavailable"}
		for _, candidate := range snapshot.Groups {
			if candidate.ID == id {
				group = candidate
				break
			}
		}
		out.Rows[i] = formatBackgroundWorkGroup(group)
		out.Rows[i].Label = labels[i]
		if !storageReady {
			out.Rows[i] = backgroundWorkRow{Label: labels[i], State: "Unavailable", Detail: "Needs access to encrypted local storage. Resolve the storage error shown in Blur before background work can start."}
		}
	}
	out.ShowModel = appleSelected && storageReady
	if out.ShowModel {
		out.Model = formatAppleBackgroundWork(snapshot.Apple, appleEnabled)
	}
	return out
}

func formatBackgroundWorkGroup(group BackgroundWorkGroup) backgroundWorkRow {
	row := backgroundWorkRow{}
	switch group.State {
	case "disabled":
		row.State, row.Detail = "Off", "Automatic processing is off."
	case "idle":
		row.State, row.Detail = "Idle", "No known work is waiting."
		if group.ID == "recaps" {
			row.Detail = "Waiting for a completed hour with saved activity."
		}
	case "running":
		row.State, row.Detail = "Working", "Processing saved sources."
	case "queued":
		row.State, row.Detail = "Queued", "Waiting to process saved sources."
		if group.ID == "library" && group.Queued == 0 {
			row.Detail = "Checking saved sources for Library updates."
		}
	case "deferred":
		switch group.Reason {
		case "foreground":
			row.State, row.Detail = "Waiting for your request to finish", "Automatic work resumes after your model requests finish."
		case "policy":
			row.State, row.Detail = "Paused by power settings", "Change Background limits below to allow optional processing."
		default:
			row.State, row.Detail = "Waiting", "Processing is temporarily deferred."
		}
	case "unavailable":
		row.State, row.Detail = "Needs attention", backgroundWorkProblem(group.Reason)
		if group.Reason == "policy" {
			row.State, row.Detail = "Power limit reached", "The last attempt was skipped by power settings. Review Background limits below."
		}
	default:
		row.State, row.Detail = "Needs attention", backgroundWorkProblem(group.Reason)
	}
	if group.ID == "library" && group.Enabled && group.CoverageLimited {
		const declined = "The model declined one or more captures. Their saved text remains available in Ask and Search."
		if group.State == "idle" {
			row.State, row.Detail = "Partial coverage", declined
		} else {
			row.Detail += " " + declined
		}
	}
	if group.Queued > 0 {
		if group.ID == "library" {
			unit := "sources"
			if group.Queued == 1 {
				unit = "source"
			}
			row.Detail += fmt.Sprintf(" %d %s pending.", group.Queued, unit)
		} else {
			row.Detail += fmt.Sprintf(" %d queued.", group.Queued)
		}
	}
	if group.Completed > 0 {
		unit := "jobs"
		if group.ID == "library" {
			unit = "chunks"
		}
		if group.Completed == 1 {
			unit = strings.TrimSuffix(unit, "s")
		}
		row.History = fmt.Sprintf("%d %s completed.", group.Completed, unit)
	}
	if !group.LastCompletedAt.IsZero() {
		row.History = strings.TrimSpace(row.History + " Last completed " + backgroundWorkTime(group.LastCompletedAt) + ".")
	}
	if group.LastErrorCode != "" && !group.LastErrorAt.IsZero() {
		row.Problem = "Last issue " + backgroundWorkTime(group.LastErrorAt) + ": " + backgroundWorkProblem(group.LastErrorCode)
	}
	return row
}

func formatAppleBackgroundWork(snapshot model.AppleInferenceSnapshot, enabled bool) backgroundWorkRow {
	row := backgroundWorkRow{Label: "Apple model", State: "Idle", Detail: "No model request is running."}
	switch {
	case !enabled:
		row.State, row.Detail = "Off", "Enable the AI model below to use on-device processing."
		return row
	case snapshot.Retiring:
		row.State, row.Detail = "Yielding to your request", "Stopping the background model request before starting yours."
	case snapshot.Active == "interactive":
		row.State, row.Detail = "Working on your request", "Automatic model work waits while your request runs."
	case snapshot.Active == "background":
		row.State, row.Detail = "Background processing", "Running one automatic model request on this Mac."
	case snapshot.InteractiveWaiting > 0 || snapshot.BackgroundWaiting > 0:
		row.State, row.Detail = "Queued", "Waiting for the model’s next available slot."
	}
	if snapshot.InteractiveWaiting > 0 {
		row.Detail += fmt.Sprintf(" %d foreground %s waiting.", snapshot.InteractiveWaiting, backgroundRequestNoun(snapshot.InteractiveWaiting))
	}
	if snapshot.BackgroundWaiting > 0 {
		row.Detail += fmt.Sprintf(" %d background %s waiting.", snapshot.BackgroundWaiting, backgroundRequestNoun(snapshot.BackgroundWaiting))
	}
	return row
}

func backgroundRequestNoun(count int) string {
	if count == 1 {
		return "request"
	}
	return "requests"
}

func backgroundWorkTime(at time.Time) string { return at.In(time.Local).Format("Jan 2 at 15:04:05") }

// Only closed codes become user-facing copy. A backend error must never put a
// prompt, captured source, URL, or arbitrary provider message into Settings.
func backgroundWorkProblem(code string) string {
	switch code {
	case "policy":
		return "Paused by power settings. Review Background limits below."
	case "local_model_required":
		return "Select Apple’s on-device model or local Ollama below. Automatic Library and recaps require a local model."
	case "settings_invalid":
		return "AI settings could not be read. Save the model settings below."
	case "model_missing", "model_unavailable", "provider_unavailable":
		return "The saved model is unavailable. Check its status below."
	case "model_budget_exceeded", "budget", "input_budget":
		return "The request exceeded the model’s input limit. Saved text remains available in Search."
	case "capacity":
		return "Processing reached a limit. Coverage is incomplete; saved sources remain available in Search."
	case "timeout":
		return "Processing timed out. Saved text is still available; try the request again."
	case "queue_full":
		return "The background queue was full. The original note is saved; its suggestions were not processed."
	case "invalid_output":
		return "The model returned an unusable result. Saved text is still available."
	case "model_refused":
		return "The local model declined this request. Saved text remains available in Search."
	case "model_configuration":
		return "The model could not use this request format. Check the saved model below; saved text remains available in Search."
	case "source_unavailable":
		return "A saved source is no longer available."
	case "model_changed":
		return "The model changed during processing. Retry Library updates below."
	case "closed":
		return "This worker has stopped."
	default:
		return "Processing could not finish. Check the saved model below; your saved text remains available."
	}
}
