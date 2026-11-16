package client

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/golang-gui/goui/core/geometry"
	"github.com/golang-gui/goui/gui"
	"github.com/golang-gui/goui/layout"
	"github.com/golang-gui/goui/platform/graphics"
	"github.com/golang-gui/goui/ui"
	"github.com/hazcod/blur/internal/meetings"
)

const (
	meetingOverlayWidth         = 400
	meetingOverlayInitialHeight = 148
	meetingOverlayAutoHideDelay = 10 * time.Second
)

func (d *desktop) hideMeetingOverlay() {
	d.meetingUI.overlayOpen = false
	d.meetingUI.overlayPresented = false
	d.meetingUI.startOnShow = false
	d.meetingUI.overlayAutoHideEpoch++
	d.meetingUI.overlayHideArmed = false
	if window := d.app.FindWindow("meeting"); window != nil && window.PlatformWindow() != nil {
		_ = window.PlatformWindow().Hide()
	}
	d.app.RequestUpdate()
}

func (d *desktop) presentMeetingOverlay(attempt int) {
	if !d.meetingUI.overlayOpen {
		return
	}
	window := d.app.FindWindow("meeting")
	if window == nil {
		if attempt < 6 {
			d.app.TimeoutFunc(25*time.Millisecond, func() { d.presentMeetingOverlay(attempt + 1) })
		}
		return
	}
	setQuickPanelHostBackground(window)
	_ = resizeMeetingOverlay(d.app, float64(d.meetingOverlayHeight()))
	if err := prepareDesktopTransient(d.app, "meeting"); err != nil {
		d.meetingUI.status = "The voice note overlay could not be shown. Open Voice notes from the sidebar."
		return
	}
	if err := window.Show(); err != nil {
		d.meetingUI.status = "The voice note overlay could not be shown. Open Voice notes from the sidebar."
		return
	}
	d.meetingUI.overlayPresented = true
	_ = resizeMeetingOverlay(d.app, float64(d.measureMeetingOverlayHeight()))
	d.armMeetingOverlayAutoHide()
	if d.meetingUI.startOnShow {
		d.meetingUI.startOnShow = false
		if !d.meetingUI.busy {
			if d.meetingUI.selectedState != meetings.Ready && !d.meetingUI.microphone && !d.meetingUI.system {
				d.meetingUI.status = "Choose an audio source in Settings → Capture → Voice notes."
				d.resizeMeetingOverlayToFit()
				return
			}
			d.startMeeting()
		}
	}
}

// Window declarations must not query or measure the currently mounted tree.
// The first hidden mount has no presented surface yet; fit it after Show.
func (d *desktop) meetingOverlayHeight() int {
	u := &d.meetingUI
	height := meetingOverlayInitialHeight + ceilPoints(quickPanelSafeTopInset())
	if u.status != "" || u.live.Issue != "" || d.meetingNeedsLanguage() {
		height += 86
	}
	return min(height, 520)
}

func (d *desktop) measureMeetingOverlayHeight() int {
	if d.meetingUI.overlayPresented && d.app != nil {
		if content := d.app.FindWidget("meeting", "meeting-overlay-content"); content != nil {
			measured := content.Measure(layout.Constraint{Max: geometry.Size{Width: meetingOverlayWidth, Height: layout.Inf}})
			if measured.Height > 0 && measured.Height < layout.Inf {
				return min(520, max(64, ceilPoints(measured.Height)))
			}
		}
	}
	return d.meetingOverlayHeight()
}

func (d *desktop) meetingOverlayWindow() ui.WindowView {
	// GoUI rejects changes to Size on a mounted window and stops the entire
	// desktop runtime. Keep creation geometry constant; resizeMeetingOverlay
	// applies changing content/notch geometry to the mounted native window.
	window := ui.Window("meeting").Title("Blur · Voice notes").
		Size(meetingOverlayWidth, meetingOverlayInitialHeight).MinSize(meetingOverlayWidth, 64).
		Chrome(ui.WindowChromeNone).Content(transientMount(pageScroll(d.meetingOverlay()).Style(styleQuickBackground), func() {
		_ = resizeMeetingOverlay(d.app, float64(d.meetingOverlayHeight()))
		if d.meetingUI.overlayOpen {
			_ = prepareDesktopTransient(d.app, "meeting")
		}
	}))
	window = addDesktopBackgroundClose(window, d.app, "meeting", func() {
		d.meetingUI.overlayOpen, d.meetingUI.overlayPresented, d.meetingUI.startOnShow = false, false, false
	})
	return window.Shortcuts(
		ui.Shortcut(ui.KeyEscape).OnActivate(d.hideMeetingOverlay),
		ui.Shortcut(ui.KeyW).Modifiers(ui.ModCommand).OnActivate(d.hideMeetingOverlay),
		ui.Shortcut(ui.KeyQ).Modifiers(ui.ModCommand).OnActivate(d.hideMeetingOverlay),
	)
}

func meetingOverlayButton(text string, primary bool) *ui.ButtonView {
	labelStyle := styleQuickRow
	if primary {
		labelStyle = styleQuickAccent
	}
	return ui.Button().Content(wrapLabel(text).Style(labelStyle)).Style(styleMeetingAction).
		MinHeight(28).Paddings(geometry.Insets{Left: 10, Right: 10, Top: 3, Bottom: 3})
}

func (d *desktop) meetingNeedsLanguage() bool {
	u := &d.meetingUI
	return u.setupIssue == "assets_missing" || u.live.Issue == "assets_missing" ||
		u.live.Availability.SpeechAvailable && !u.live.Availability.RequestedLocaleInstalled && !u.probedAt.IsZero()
}

func (d *desktop) downloadAndStartMeeting() {
	u := &d.meetingUI
	d.resetMeetingStopForNewSession()
	title, locale, tracks := u.title, u.locale, d.selectedMeetingTracks()
	id, savedAgenda := u.selected, u.selectedState == meetings.Ready
	d.meetingOperation(func(ctx context.Context) error {
		if err := d.client.InstallMeetingLanguage(ctx, locale); err != nil {
			return err
		}
		if savedAgenda && id != "" {
			return d.client.StartMeetingNotes(ctx, id)
		}
		return d.client.StartMeeting(ctx, title, locale, tracks)
	}, func() {
		live := d.client.MeetingLive()
		u.selected, u.selectedTitle, u.selectedAt = live.Meeting.ID, live.Meeting.Title, live.Meeting.StartedAt
		u.browseSaved = false
		u.operationCancel = nil
	})
}

func (d *desktop) meetingOverlay() ui.View {
	u := &d.meetingUI
	d.updateVoiceLiveLabels(time.Now())
	state := u.live.State
	active := state == "running" || state == "starting" || state == "paused" || state == "pausing" || state == "stopping"
	// A typed setup failure is authoritative even before a complete probe
	// snapshot arrives. Never hide its recovery action behind stale controls.
	needsDownload := d.meetingNeedsLanguage() && !u.live.AudioRecording && state != "running" && state != "paused" && state != "pausing" && state != "stopping"
	rows := []ui.View{ui.HBox(
		wrapLabel("Voice notes").Style(styleQuickAnswer).MainWeight(1),
		d.meetingAudioMeter(),
		ui.Build(func() ui.View { return ui.Label(u.elapsedLabel.Get()).Style(styleQuickMeta).MinWidth(52) }),
		d.voicePinButton(),
	).Spacing(10).CrossAlign(layout.CrossCenter)}
	progress := d.voiceOverlayProgress()
	if state != "running" && state != "paused" && progress == "" {
		rows = append(rows, wrapLabel(meetingStateCopy(state)).Style(styleQuickMeta))
	}
	if d.client == nil || !d.client.CanUseMeetings() {
		rows = append(rows, wrapLabel("Encrypted memory is unavailable. Open Settings → Memory to repair it.").Style(styleQuickRow))
		return ui.VBox(rows...).Paddings(quickPanelInsets()).Spacing(10).Style(styleQuickBackground)
	}
	if u.status != "" && !d.voiceSetupDownloading() {
		rows = append(rows, wrapLabel(u.status).Style(styleQuickRow))
	} else if issue := meetingIssueCopy(u.live.Issue); issue != "" {
		rows = append(rows, wrapLabel(issue).Style(styleQuickRow))
	}
	buttons := []ui.View{}
	if !d.voiceSetupDownloading() && (active && !needsDownload || u.busy || u.startOnShow) {
		pauseLabel, operation := "Pause", "pause"
		if state == "paused" {
			pauseLabel, operation = "Resume", "resume"
		}
		buttons = append(buttons,
			meetingOverlayButton(pauseLabel, false).Enabled(!u.busy && (state == "running" || state == "paused")).OnClick(func() {
				d.meetingOperation(func(ctx context.Context) error { return d.client.ControlMeeting(ctx, operation) }, nil)
			}),
			meetingOverlayButton("Stop", true).Enabled(!u.busy && state != "pausing" && state != "stopping" || u.busy && !u.live.AudioRecording && u.operationCancel != nil).OnClick(d.stopMeetingFromOverlay),
		)
	} else if !d.voiceSetupDownloading() && needsDownload {
		buttons = append(buttons, meetingOverlayButton("Download & start", true).Enabled(u.microphone || u.system).OnClick(d.downloadAndStartMeeting))
	} else if !d.voiceSetupDownloading() && state != "completed" {
		buttons = append(buttons, meetingOverlayButton("Try again", true).Enabled(u.microphone || u.system).OnClick(d.startMeeting))
	}
	if len(buttons) > 0 || progress != "" {
		buttons = append(buttons, d.voiceLivePhraseView())
		rows = append(rows, ui.HBox(buttons...).Spacing(8).CrossAlign(layout.CrossCenter))
	}
	return ui.VBox(rows...).Paddings(quickPanelInsets()).Spacing(8).CrossAlign(layout.CrossStretch).Style(styleQuickBackground).ID("meeting-overlay-content")
}

func (d *desktop) stopMeetingFromOverlay() {
	u := &d.meetingUI
	if u.busy {
		if !u.live.AudioRecording && u.operationCancel != nil {
			u.stopRequested = true
			u.operationCancel()
		}
		return
	}
	if u.startOnShow || u.live.State == "" || u.live.State == "completed" || u.live.State == "failed" || u.live.State == "ready" {
		d.hideMeetingOverlay()
		return
	}
	if u.stopDispatching {
		return
	}
	u.stopRequested = true
	u.stopDispatching = true
	d.meetingOperation(func(ctx context.Context) error { return d.client.ControlMeeting(ctx, "stop") }, nil)
}

func (d *desktop) resizeMeetingOverlayToFit() {
	d.app.RequestUpdate()
	if d.meetingUI.overlayOpen {
		d.app.Post(func() { _ = resizeMeetingOverlay(d.app, float64(d.measureMeetingOverlayHeight())) })
	}
}

func (d *desktop) meetingAudioMeter() ui.View {
	// A dB-scaled meter shows real signal energy, including quiet speech.
	// There is no idle waveform or timer pretending that sound was received.
	q := quickPalette()
	return ui.DrawingArea(func(p gui.Painter, size geometry.Size) {
		// Read current samples when painting, including the freshness deadline.
		// Meter refreshes need not rebuild either window or its text editors.
		strength := float32(meetingMeterBars(d.meetingUI.live.AudioLevel(time.Now()))) / 12
		for i, peak := range []float32{8, 14, 20, 14, 8} {
			brush := graphics.ColorOf(q.hairline)
			if strength > 0 {
				brush = graphics.ColorOf(q.amber)
			}
			height := 2 + (peak-2)*strength
			p.FillRect(geometry.Rect(9+float32(i)*6, (size.Height-height)/2, 3, height), brush)
		}
	}).OnShouldDraw(func(redraw *bool) {
		// RequestPaint schedules a frame but DrawingArea otherwise reuses its
		// cached pixels. Regenerate this small meter from current level samples.
		*redraw = true
	}).ID("meeting-audio-level").MinSize(48, 20).MaxSize(48, 20)
}

func (d *desktop) tickMeetingOverlay() {
	if d.meetingUI.overlayTickRunning || !d.meetingUI.overlayOpen || d.meetingUI.live.State != "running" {
		return
	}
	d.meetingUI.overlayTickRunning = true
	d.app.TimeoutFunc(125*time.Millisecond, func() {
		d.meetingUI.overlayTickRunning = false
		if d.meetingUI.overlayOpen && d.meetingUI.live.State == "running" {
			d.updateVoiceRecordingClock(time.Now())
			d.repaintMeetingAudioMeter()
			d.tickMeetingOverlay()
		}
	})
}

func (d *desktop) updateVoiceLiveLabels(now time.Time) {
	d.updateVoiceRecordingClock(now)
	u := &d.meetingUI
	phrase := d.voiceOverlayProgress()
	if phrase == "" {
		phrase = voiceLivePhrase(u.live)
	}
	if u.livePhraseLabel.Peek() != phrase {
		u.livePhraseLabel.Set(phrase)
	}
}

func (d *desktop) voiceOverlayProgress() string {
	u := &d.meetingUI
	if d.voiceSetupDownloading() {
		return "Downloading speech files…"
	}
	if u.live.State == "pausing" || u.live.State == "stopping" {
		return meetingStateCopy(u.live.State)
	}
	if u.busy && !u.live.AudioRecording || u.startOnShow || u.live.State == "starting" {
		return "Preparing local transcription…"
	}
	return ""
}

type voicePhraseView struct {
	ui.ComposeBase[voicePhraseView]
	desktop *desktop
}

func (d *desktop) voiceLivePhraseView() ui.View {
	v := &voicePhraseView{desktop: d}
	v.Self = v
	return v.ID("voice-live-phrase")
}

func (v *voicePhraseView) Build(ctx ui.BuildContext) ui.View {
	d := v.desktop
	phrase := d.meetingUI.livePhraseLabel.Get()
	visible := phrase != ""
	wasVisible, _ := ctx.State().(bool)
	if visible != wasVisible {
		ctx.SetState(visible)
		ctx.AfterUpdate(func() {
			if d.meetingUI.overlayOpen && d.meetingUI.overlayPresented {
				_ = resizeMeetingOverlay(d.app, float64(d.measureMeetingOverlayHeight()))
			}
		})
	}
	if !visible {
		return nil
	}
	return newVoicePhraseViewport(phrase)
}

func (d *desktop) toggleMeetingOverlayPin() {
	u := &d.meetingUI
	u.overlayPinned = !u.overlayPinned
	u.overlayAutoHideEpoch++
	u.overlayHideArmed = false
	if !u.overlayPinned {
		d.armMeetingOverlayAutoHide()
	}
	d.app.RequestUpdate()
}

func (d *desktop) armMeetingOverlayAutoHide() {
	u := &d.meetingUI
	if !u.overlayOpen || !u.overlayPresented || u.overlayPinned || u.overlayHideArmed || !u.live.AudioRecording && u.live.State != "paused" {
		return
	}
	u.overlayHideArmed = true
	u.overlayAutoHideEpoch++
	epoch := u.overlayAutoHideEpoch
	d.app.TimeoutFunc(meetingOverlayAutoHideDelay, func() {
		if epoch != u.overlayAutoHideEpoch {
			return
		}
		u.overlayHideArmed = false
		if u.overlayOpen && !u.overlayPinned && voiceRecordingActive(u.live.State) {
			d.hideMeetingOverlay()
		}
	})
}

func (d *desktop) updateVoiceRecordingClock(now time.Time) {
	u := &d.meetingUI
	clock := voiceRecordingClock(u.live.RecordingLength(now))
	if u.elapsedLabel.Peek() != clock {
		u.elapsedLabel.Set(clock)
	}
}

func voiceRecordingClock(duration time.Duration) string {
	seconds := int64(max(time.Duration(0), duration) / time.Second)
	if seconds >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", seconds/3600, seconds/60%60, seconds%60)
	}
	return fmt.Sprintf("%02d:%02d", seconds/60, seconds%60)
}

func voiceLivePhrase(live MeetingLiveView) string {
	if live.State == "pausing" || live.State == "paused" {
		return ""
	}
	phrase := live.LivePhrase
	if phrase == "" {
		phrase = live.Preview["microphone"]
	}
	if phrase == "" {
		phrase = live.Preview["system"]
	}
	phrase = strings.Join(strings.Fields(phrase), " ")
	characters := []rune(phrase)
	if len(characters) > 120 {
		phrase = "…" + string(characters[len(characters)-119:])
	}
	return phrase
}

func (d *desktop) repaintMeetingAudioMeter() {
	if d.meetingUI.overlayOpen {
		if widget := d.app.FindWidget("meeting", "meeting-audio-level"); widget != nil {
			widget.RequestPaint()
		}
	}
}

func meetingMeterBars(level float64) int {
	if !(!math.IsNaN(level) && !math.IsInf(level, 0) && level > 0) {
		return 0
	}
	decibels := 20 * math.Log10(min(level, 1))
	return min(12, max(0, int(math.Ceil((decibels+60)/60*12))))
}
