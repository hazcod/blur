package client

import (
	"context"
	"strings"
	"time"

	"github.com/golang-gui/goui/theme/modern"
	"github.com/golang-gui/goui/ui"
	"github.com/hazcod/blur/internal/meetings"
	"github.com/hazcod/blur/internal/preferences"
)

type voiceSettingsState struct {
	draft       preferences.VoiceNotesConfig
	baseline    preferences.VoiceNotesConfig
	initialized bool
	saving      bool
	status      string
}

// ConfigureVoiceNotes changes only voice defaults in the latest canonical
// preferences. It neither starts capture nor touches an existing recording.
func (c *Client) ConfigureVoiceNotes(ctx context.Context, next preferences.VoiceNotesConfig) (preferences.Config, error) {
	if c == nil || c.preferences == nil {
		return preferences.Config{}, ErrNotConfigured
	}
	normalized, err := preferences.NormalizeVoiceNotes(next)
	if err != nil {
		return preferences.Config{}, err
	}
	ctx, done, err := c.beginOperation(ctx, 8*time.Second)
	if err != nil {
		return preferences.Config{}, err
	}
	defer done()
	c.preferencesConfigMu.Lock()
	defer c.preferencesConfigMu.Unlock()
	return c.preferences.Update(ctx, func(latest preferences.Config) (preferences.Config, error) {
		latest.VoiceNotes = normalized
		return latest, nil
	})
}

func (d *desktop) savedVoiceNotesSettings() preferences.VoiceNotesConfig {
	if d != nil {
		if config, err := preferences.NormalizeVoiceNotes(d.preferences.VoiceNotes); err == nil {
			return config
		}
	}
	return preferences.DefaultVoiceNotesConfig()
}

// A selected notes-only agenda owns its original recording configuration.
// New defaults apply only to a genuinely new recording, never to active audio.
func (d *desktop) applySavedVoiceNotesSettings() {
	if d == nil || d.meetingUI.busy || d.meetingUI.live.AudioRecording || d.meetingUI.selectedState == meetings.Ready || voiceRecordingActive(d.meetingUI.live.State) || d.meetingUI.live.State == "ready" {
		return
	}
	config := d.savedVoiceNotesSettings()
	if d.meetingUI.locale != config.Locale {
		d.meetingUI.probedAt = time.Time{}
	}
	d.meetingUI.locale = config.Locale
	d.meetingUI.microphone = config.Source != preferences.VoiceSourceSystem
	d.meetingUI.system = config.Source != preferences.VoiceSourceMicrophone
}

func voiceRecordingActive(state string) bool {
	switch state {
	case "starting", "running", "pausing", "paused", "stopping":
		return true
	default:
		return false
	}
}

func (d *desktop) voiceSettingsEditable() bool {
	return d != nil && d.app != nil && d.client != nil && d.client.preferences != nil && !d.meetingUI.live.AudioRecording && !d.loading && !d.voiceSettings.saving && !d.voiceSetupDownloading() && !d.meetingUI.busy && !d.meetingUI.probing && !voiceRecordingActive(d.meetingUI.live.State)
}

// Draft initialization reads the already-loaded UI snapshot only. Building this
// card never reads a file, probes speech, requests access, or starts audio.
func (d *desktop) ensureVoiceSettingsDraft() {
	saved := d.savedVoiceNotesSettings()
	u := &d.voiceSettings
	if !u.initialized || (!u.saving && u.draft == u.baseline && u.baseline != saved) {
		u.draft, u.baseline, u.initialized = saved, saved, true
		u.status = ""
	}
}

func (d *desktop) setVoiceSettingsSource(source string) {
	if !d.voiceSettingsEditable() {
		return
	}
	switch source {
	case preferences.VoiceSourceBoth, preferences.VoiceSourceMicrophone, preferences.VoiceSourceSystem:
	default:
		return
	}
	d.ensureVoiceSettingsDraft()
	d.voiceSettings.draft.Source = source
	d.voiceSettings.status = ""
	d.app.RequestUpdate()
}

func (d *desktop) setVoiceSettingsLocale(locale string) {
	if !d.voiceSettingsEditable() {
		return
	}
	d.ensureVoiceSettingsDraft()
	d.voiceSettings.draft.Locale = locale
	d.voiceSettings.status = ""
	d.app.RequestUpdate()
}

func (d *desktop) saveVoiceSettings() {
	if d == nil || d.app == nil || !d.voiceSettingsEditable() {
		return
	}
	d.ensureVoiceSettingsDraft()
	draft := d.voiceSettings.draft
	if strings.TrimSpace(draft.Locale) == "" {
		d.voiceSettings.status = "Use a language code such as en-US, nl-BE, or fr-FR."
		d.app.RequestUpdate()
		return
	}
	normalized, err := preferences.NormalizeVoiceNotes(draft)
	if err != nil {
		d.voiceSettings.status = "Use a language code such as en-US, nl-BE, or fr-FR."
		d.app.RequestUpdate()
		return
	}
	if draft == d.savedVoiceNotesSettings() {
		return
	}
	d.voiceSettings.saving = true
	d.loading = true
	d.voiceSettings.status = "Saving voice settings…"
	d.app.RequestUpdate()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		saved, err := d.client.ConfigureVoiceNotes(ctx, normalized)
		cancel()
		d.app.Post(func() {
			d.loading = false
			d.voiceSettings.saving = false
			if err != nil {
				d.voiceSettings.status = "Voice settings could not be saved. Your saved choices are unchanged. Try again."
			} else {
				d.preferences = saved
				d.voiceSettings.draft = saved.VoiceNotes
				d.voiceSettings.baseline = saved.VoiceNotes
				d.voiceSettings.status = "Saved for the next new recording."
				d.applySavedVoiceNotesSettings()
				if d.setupChecksVisible() {
					d.refreshVoiceSetupCheck(false)
				}
			}
			d.app.RequestUpdate()
		})
	}()
}

func (d *desktop) voiceSettingsView() ui.View {
	d.ensureVoiceSettingsDraft()
	editable := d.voiceSettingsEditable()
	rows := []ui.View{
		wrapLabel("Voice notes").Style(styleSectionTitle),
		wrapLabel("Choose the audio sources and transcription language for your next new recording. Existing notes keep their original settings.").Style(modern.MutedText),
		wrapLabel("Audio sources"),
	}
	var choices []ui.View
	for _, option := range []struct{ source, label string }{{preferences.VoiceSourceBoth, "Microphone + system"}, {preferences.VoiceSourceMicrophone, "Microphone"}, {preferences.VoiceSourceSystem, "System audio"}} {
		choices = append(choices, choiceButton(option.label, d.voiceSettings.draft.Source == option.source).Enabled(editable).OnClick(func() { d.setVoiceSettingsSource(option.source) }))
	}
	rows = append(rows, ui.HBox(choices...).Spacing(8), wrapLabel("Transcription language"), ui.TextInput().Text(d.voiceSettings.draft.Locale).Placeholder("en-US").Enabled(editable).OnText(d.setVoiceSettingsLocale), wrapLabel("Use a language code such as en-US, nl-BE, or fr-FR. Speech support and downloads are checked when you start recording.").Style(modern.MutedText))
	if voiceRecordingActive(d.meetingUI.live.State) || d.meetingUI.busy || d.meetingUI.probing || d.voiceSetupDownloading() {
		rows = append(rows, wrapLabel("Finish the current recording or voice operation to change these settings.").Style(modern.MutedText))
	}
	rows = append(rows, primaryButton("Save voice settings").Enabled(editable && d.voiceSettings.draft != d.savedVoiceNotesSettings()).OnClick(d.saveVoiceSettings))
	if d.voiceSettings.status != "" {
		rows = append(rows, wrapLabel(d.voiceSettings.status).Style(modern.MutedText))
	}
	return surfaceBox(14, 9, rows...)
}
