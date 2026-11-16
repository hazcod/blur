package client

import (
	"context"
	"strings"
	"time"

	"github.com/golang-gui/goui/layout"
	"github.com/golang-gui/goui/ui"
)

type logSettingsPresentation struct {
	Saving bool
	Status string
}
type logSettingsViewState struct {
	state ui.State[logSettingsPresentation]
}

func (d *desktop) logSettingsView() ui.View {
	return ui.Build(func() ui.View {
		state := d.logSettingsUI.state.Get()
		current := d.preferences.LogLevel
		if current == "" {
			current = "info"
		}
		buttons := make([]ui.View, 0, 4)
		for _, level := range []string{"debug", "info", "warn", "error"} {
			buttons = append(buttons, choiceButton(logLevelLabel(level), current == level).
				Enabled(d.client != nil && d.client.preferences != nil && !d.loading && !state.Saving).
				OnClick(func() { d.saveLogLevel(level) }))
		}
		rows := []ui.View{
			wrapLabel("Diagnostics").Style(styleSectionTitle),
			wrapLabel("Log level"),
			wrapLabel("Choose which application diagnostics Blur records. Changes apply as soon as they are saved.").Style(styleMeta),
			ui.HBox(buttons...).Spacing(6).CrossAlign(layout.CrossCenter),
			wrapLabel("Debug includes more detailed diagnostics. Crash reports and native runtime messages are recorded independently of this setting.").Style(styleMeta),
		}
		if d.client == nil || d.client.preferences == nil {
			rows = append(rows, wrapLabel("Log settings are unavailable until Blur can open its local settings.").Style(styleMeta))
		}
		if state.Status != "" {
			rows = append(rows, wrapLabel(state.Status).Style(styleMeta))
		}
		return surfaceBox(14, 10, rows...)
	}).ID("log-settings")
}

func logLevelLabel(level string) string {
	switch level {
	case "debug":
		return "Debug"
	case "warn":
		return "Warnings"
	case "error":
		return "Errors"
	default:
		return "Info"
	}
}

func (d *desktop) saveLogLevel(level string) {
	if d == nil || d.app == nil || d.client == nil || d.client.preferences == nil || d.loading || d.logSettingsUI.state.Peek().Saving {
		return
	}
	d.loading = true
	d.logSettingsUI.state.Set(logSettingsPresentation{Saving: true, Status: "Saving log level…"})
	d.app.RequestUpdate()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		saved, err := d.client.ConfigureLogLevel(ctx, level)
		cancel()
		d.app.Post(func() {
			d.loading = false
			state := logSettingsPresentation{Status: "Log level could not be saved. The previous level remains active. Try again."}
			if err == nil {
				// Other UI settings can have their own newer receipts. Never
				// replace them with this operation's complete config snapshot.
				d.preferences.LogLevel = saved.LogLevel
				state.Status = strings.TrimSuffix(logLevelLabel(saved.LogLevel), "s") + " logging is active."
			}
			d.logSettingsUI.state.Set(state)
			d.app.RequestUpdate()
		})
	}()
}
