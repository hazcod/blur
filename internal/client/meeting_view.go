package client

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/golang-gui/goui/theme/modern"
	"github.com/golang-gui/goui/ui"
	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/meetings"
	"github.com/hazcod/blur/internal/platform/darwin"
)

type meetingViewState struct {
	overlayOpen          bool
	overlayMounted       bool
	overlayPresented     bool
	startOnShow          bool
	setupIssue           string
	setupEpoch           uint64
	stopRequested        bool
	stopDispatching      bool
	elapsedLabel         ui.State[string]
	livePhraseLabel      ui.State[string]
	optionsOpen          bool
	notesOpen            bool
	operationCancel      context.CancelFunc
	overlayTickRunning   bool
	summary              *assistant.Result
	summaryLoading       bool
	summaryCancel        context.CancelFunc
	live                 MeetingLiveView
	title                string
	locale               string
	microphone           bool
	system               bool
	draft                string
	noteID               string
	noteRecordedAt       time.Time
	busy                 bool
	probing              bool
	probedAt             time.Time
	status               string
	hotkeyIssue          string
	menuIssue            string
	menuConfigured       bool
	menuRecording        bool
	menuState            string
	menuBusy             bool
	overlayPinned        bool
	overlayHideArmed     bool
	overlayAutoHideEpoch uint64
	loaded               bool
	loading              bool
	page                 meetings.MeetingPage
	after                uint64
	previous             []uint64
	selected             string
	selectedTitle        string
	selectedAt           time.Time
	selectedState        meetings.State
	selectedRecordCount  uint64
	transcriptExpanded   bool
	transcript           meetings.RecordPage
	transcriptAt         uint64
	transcriptPrev       []uint64
	reading              bool
	browseSaved          bool
	request              uint64
	deletePending        string
}

func (d *desktop) initializeMeetings(done <-chan struct{}) {
	d.applySavedVoiceNotesSettings()
	d.app.Post(func() {
		if err := installMeetingShortcut(d.openMeetingsFromMenu); err != nil {
			d.meetingUI.hotkeyIssue = "Control–Option–M is in use by another app. Open Voice notes from the menu bar or sidebar."
		}
		d.updateMeetingMenu()
	})
	if !d.client.CanUseMeetings() {
		return
	}
	go func() {
		// Coalesce volatile speech updates; history scroll and editor content
		// remain stable, with no recurring reload of the meeting list.
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		dirty := false
		for {
			select {
			case <-done:
				return
			case <-d.client.MeetingChanges():
				dirty = true
			case <-ticker.C:
				if !dirty {
					continue
				}
				dirty = false
				epoch, stable := d.client.BeginMemoryRead()
				if !stable {
					continue
				}
				live := d.client.MeetingLive()
				d.app.Post(func() {
					d.client.PublishMemoryRead(epoch, func() {
						d.publishMeetingLive(live)
					})
				})
			}
		}
	}()
}

// Audio-only events repaint the meter; transcript, status and control changes
// still update the views. Keep new fields in this comparison by ignoring only
// the level samples themselves.
func meetingLiveContentChanged(previous, next MeetingLiveView) bool {
	previous.Levels, next.Levels = nil, nil
	return !reflect.DeepEqual(previous, next)
}

func meetingLiveOverlayControlsChanged(previous, next MeetingLiveView) bool {
	previous.Preview, next.Preview = nil, nil
	previous.Tail, next.Tail = nil, nil
	previous.LivePhrase, next.LivePhrase = "", ""
	return meetingLiveContentChanged(previous, next)
}

func (d *desktop) publishMeetingLive(live MeetingLiveView) {
	previous := d.meetingUI.live.State
	previousLive := d.meetingUI.live
	workspaceVisible := d.dashboardOpen && d.page == "Ask" && d.memoryUI.meetingDetail
	contentChanged := meetingLiveContentChanged(d.meetingUI.live, live)
	d.meetingUI.live = live
	if live.State == "running" && d.meetingUI.setupIssue != "" {
		d.meetingUI.setupIssue, d.meetingUI.status = "", ""
	}
	d.updateVoiceLiveLabels(time.Now())
	// A probe or operation completion can already have copied this live state.
	// Reconcile the native recording indicator against its own last applied
	// acknowledgement, rather than relying on the displayed state transition.
	d.updateMeetingMenu()
	d.armMeetingOverlayAutoHide()
	if live.Meeting.ID != "" && (d.meetingUI.selected == "" || previous != live.State && live.State == "starting") {
		d.meetingUI.selected, d.meetingUI.selectedTitle = live.Meeting.ID, live.Meeting.Title
		d.meetingUI.selectedAt = live.Meeting.StartedAt
		d.meetingUI.selectedState = live.Meeting.State
	}
	if d.meetingUI.selected == live.Meeting.ID {
		d.meetingUI.selectedState = live.Meeting.State
	}
	if previous != live.State {
		if live.State == "completed" || live.State == "failed" {
			d.meetingUI.loaded = false
			d.memoryUI.loaded = false
			if d.dashboardOpen && d.page == "Ask" && strings.TrimSpace(d.askQuestion) == "" && !d.memoryUI.loading {
				d.loadMemoryBrowse(memoryBrowseCursor{})
			}
		}
	}
	if live.State == "completed" && d.meetingUI.stopRequested {
		d.meetingUI.stopRequested, d.meetingUI.stopDispatching = false, false
		d.hideMeetingOverlay()
		id := live.Meeting.ID
		// Live publication can hold the memory fence. Begin recap reads only
		// after returning to the UI loop, and only for this explicit Stop.
		d.app.Post(func() { d.openFinishedVoiceRecap(id) })
		return
	}
	if live.State == "failed" {
		// A failed native Stop is terminal for that recording. Its dispatch
		// guard must not suppress Stop after the user explicitly retries.
		d.meetingUI.stopRequested, d.meetingUI.stopDispatching = false, false
	}
	if workspaceVisible || d.meetingUI.overlayOpen {
		if contentChanged && (workspaceVisible || meetingLiveOverlayControlsChanged(previousLive, live)) {
			d.app.RequestUpdate()
		} else {
			d.repaintMeetingAudioMeter()
		}
		if previous != live.State && d.meetingUI.overlayOpen {
			d.app.Post(func() { _ = resizeMeetingOverlay(d.app, float64(d.measureMeetingOverlayHeight())) })
		}
	}
	d.tickMeetingOverlay()
}

func (d *desktop) invalidateMeetingViews() {
	u := &d.meetingUI
	u.startOnShow = false
	u.stopRequested, u.stopDispatching, u.setupIssue = false, false, ""
	u.setupEpoch++
	if u.operationCancel != nil {
		u.operationCancel()
		u.operationCancel = nil
	}
	if u.summaryCancel != nil {
		u.summaryCancel()
	}
	u.summary, u.summaryCancel, u.summaryLoading = nil, nil, false
	u.live = MeetingLiveView{Issue: "memory_changed"}
	u.page, u.transcript = meetings.MeetingPage{}, meetings.RecordPage{}
	u.loaded, u.loading, u.reading = false, false, false
	u.selected, u.selectedTitle, u.deletePending = "", "", ""
	u.selectedState = ""
	u.previous, u.transcriptPrev = nil, nil
	u.after, u.transcriptAt = 0, 0
	u.request++
	d.updateMeetingMenu()
}

func (d *desktop) updateMeetingMenu() {
	recording := d.meetingUI.live.AudioRecording
	state, busy := d.meetingUI.live.State, d.meetingUI.busy
	if d.meetingUI.menuConfigured && d.meetingUI.menuRecording == recording && d.meetingUI.menuState == state && d.meetingUI.menuBusy == busy && d.meetingUI.menuIssue == "" {
		return
	}
	if err := updateMeetingMenu(d.meetingMenuAction, d.stopMeetingFromMenu, recording, voiceRecordingActive(state), state == "paused", busy); err != nil {
		d.meetingUI.menuIssue = "The recording indicator could not be updated. Open Voice notes in Settings to check the recording."
	} else {
		d.meetingUI.menuIssue = ""
		d.meetingUI.menuConfigured, d.meetingUI.menuRecording = true, recording
		d.meetingUI.menuState, d.meetingUI.menuBusy = state, busy
	}
}

func (d *desktop) meetingMenuAction() {
	if d == nil || d.app == nil {
		return
	}
	d.app.Post(func() {
		if d.client != nil {
			live := d.client.MeetingLive()
			if live.State == "running" || live.State == "paused" {
				d.publishMeetingLive(live)
				operation := "pause"
				if live.State == "paused" {
					operation = "resume"
				}
				d.meetingOperation(func(ctx context.Context) error { return d.client.ControlMeeting(ctx, operation) }, nil)
				return
			}
		}
		d.openMeetingsFromMenu()
	})
}

func (d *desktop) stopMeetingFromMenu() {
	if d == nil || d.app == nil || d.client == nil {
		return
	}
	d.app.Post(func() {
		d.publishMeetingLive(d.client.MeetingLive())
		d.stopMeetingFromOverlay()
	})
}

func (d *desktop) openMeetingsFromMenu() {
	d.openMeetingOverlay(false)
}

func (d *desktop) revealActiveVoiceOverlay() {
	d.openMeetingOverlay(true)
}

func (d *desktop) openMeetingOverlay(revealOnly bool) {
	if d == nil || d.app == nil {
		return
	}
	d.app.Post(func() {
		live := d.client.MeetingLive()
		if revealOnly && !voiceRecordingActive(live.State) {
			return
		}
		d.hideQuickPanel()
		d.quickUI.preferQuick = false
		newlyOpened := !d.meetingUI.overlayOpen
		d.meetingUI.overlayOpen, d.meetingUI.overlayMounted = true, true
		d.meetingUI.live = live
		if revealOnly {
			d.meetingUI.startOnShow = false
		}
		d.updateMeetingMenu()
		if !revealOnly && newlyOpened && !d.meetingUI.busy && d.client.CanUseMeetings() {
			switch live.State {
			case "starting", "running", "pausing", "paused", "stopping":
				// Reopening shows the current recording, including a deliberate pause.
			default:
				d.meetingUI.startOnShow = true
			}
		}
		liveActive := live.Meeting.ID != "" && live.State != "completed" && live.State != "failed"
		if liveActive || d.meetingUI.selectedState != meetings.Ready {
			d.meetingUI.selected, d.meetingUI.selectedTitle, d.meetingUI.selectedAt = live.Meeting.ID, live.Meeting.Title, live.Meeting.StartedAt
			d.meetingUI.selectedState = live.Meeting.State
			if live.Meeting.State == meetings.Ready {
				d.selectReadyMeeting(live.Meeting)
			}
		}
		if !liveActive && live.Meeting.State != meetings.Ready && d.meetingUI.selectedState != meetings.Ready {
			d.applySavedVoiceNotesSettings()
		}
		d.meetingUI.browseSaved = false
		d.visitMeetings()
		d.tickMeetingOverlay()
		d.app.RequestUpdate()
		d.app.Post(func() { d.presentMeetingOverlay(0) })
	})
}

func (d *desktop) visitMeetings() {
	if d.client == nil || !d.client.CanUseMeetings() {
		return
	}
	if !d.meetingUI.loaded && !d.meetingUI.loading {
		d.loadMeetingPage(0)
	}
	if !d.meetingUI.probing && time.Since(d.meetingUI.probedAt) > time.Minute {
		d.probeMeetingLanguage()
	}
}

func (d *desktop) probeMeetingLanguage() {
	if d.client == nil || d.meetingUI.probing {
		return
	}
	d.meetingUI.probing = true
	locale := d.meetingUI.locale
	epoch := d.meetingUI.setupEpoch
	go func() {
		err := d.client.ProbeMeetings(context.Background(), locale)
		d.app.Post(func() {
			d.meetingUI.probing = false
			if epoch != d.meetingUI.setupEpoch {
				d.app.RequestUpdate()
				return
			}
			if locale != d.meetingUI.locale {
				d.probeMeetingLanguage()
				return
			}
			d.meetingUI.probedAt = time.Now()
			if err != nil {
				d.meetingUI.status = meetingErrorCopy(err)
				var operation darwin.MeetingAudioOperationError
				d.meetingUI.setupIssue = ""
				if errors.As(err, &operation) {
					d.meetingUI.setupIssue = operation.Code
				}
				if errors.As(err, &operation) && operation.Code == "assets_missing" {
					d.meetingUI.probedAt = time.Now()
				}
			} else {
				d.meetingUI.setupIssue = ""
				d.meetingUI.live = d.client.MeetingLive()
				d.updateMeetingMenu()
			}
			d.resizeMeetingOverlayToFit()
		})
	}()
}

func (d *desktop) selectedMeetingTracks() []string {
	var tracks []string
	if d.meetingUI.microphone {
		tracks = append(tracks, "microphone")
	}
	if d.meetingUI.system {
		tracks = append(tracks, "system")
	}
	return tracks
}

func (d *desktop) meetingOperation(run func(context.Context) error, saved func(), changesMemory ...bool) {
	if d.meetingUI.busy || d.client == nil {
		return
	}
	// Compound setup must keep the original memory epoch across language
	// installation and Start, including gaps between separate Client calls.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	epoch, stable := d.client.BeginMemoryRead()
	mutation := len(changesMemory) > 0 && changesMemory[0]
	var operationID uint64
	if !mutation {
		var admitted bool
		if stable {
			operationID, admitted = d.client.registerAskEpoch(cancel, &epoch)
		}
		if !admitted {
			cancel()
			d.meetingUI.status = meetingErrorCopy(ErrMemoryChanging)
			d.app.RequestUpdate()
			return
		}
		d.meetingUI.operationCancel = cancel
	}
	d.meetingUI.busy, d.meetingUI.status, d.meetingUI.setupIssue = true, "", ""
	d.meetingUI.setupEpoch++
	generation := d.memoryGeneration
	d.app.RequestUpdate()
	go func() {
		defer cancel()
		if !mutation {
			defer d.client.unregisterAsk(operationID)
		}
		err := ctx.Err()
		if err == nil {
			err = run(ctx)
		}
		d.app.Post(func() {
			d.meetingUI.busy = false
			d.meetingUI.operationCancel = nil
			if generation != d.memoryGeneration || !mutation && !d.client.MemoryReadCurrent(epoch) {
				d.app.RequestUpdate()
				return
			}
			d.meetingUI.live = d.client.MeetingLive()
			if d.meetingUI.stopRequested && !d.meetingUI.stopDispatching && voiceRecordingActive(d.meetingUI.live.State) {
				// Stop may be clicked after native Start was admitted but before
				// this completion reached the UI. Canceling setup cannot stop the
				// detached recording; send a real Stop to that admitted session.
				d.stopMeetingFromOverlay()
				return
			}
			if d.meetingUI.stopRequested && errors.Is(err, context.Canceled) && !d.meetingUI.live.AudioRecording && !voiceRecordingActive(d.meetingUI.live.State) {
				d.meetingUI.stopRequested, d.meetingUI.stopDispatching, d.meetingUI.status = false, false, ""
				d.hideMeetingOverlay()
				return
			}
			if d.meetingUI.selected == "" && d.meetingUI.live.State == "ready" {
				// Header creation may have succeeded before a note append failed.
				// Retain it so retry appends to the same saved note.
				d.selectReadyMeeting(d.meetingUI.live.Meeting)
			}
			if err != nil {
				d.meetingUI.status = meetingErrorCopy(err)
				d.meetingUI.stopRequested, d.meetingUI.stopDispatching = false, false
			} else if saved != nil {
				saved()
			}
			var operation darwin.MeetingAudioOperationError
			if errors.As(err, &operation) {
				d.meetingUI.setupIssue = operation.Code
				if operation.Code == "assets_missing" {
					d.meetingUI.probedAt = time.Now()
				}
			}
			d.updateMeetingMenu()
			d.resizeMeetingOverlayToFit()
		})
	}()
}

func (d *desktop) selectReadyMeeting(meeting meetings.Meeting) {
	u := &d.meetingUI
	u.selected, u.selectedTitle, u.selectedAt, u.selectedState = meeting.ID, meeting.Title, meeting.StartedAt, meeting.State
	u.title = meeting.Title
	u.microphone, u.system = false, false
	for _, track := range meeting.Tracks {
		u.microphone = u.microphone || track == "microphone"
		u.system = u.system || track == "system"
	}
	if u.locale != meeting.Locale {
		d.setMeetingLocale(meeting.Locale)
	}
}

func (d *desktop) setMeetingLocale(value string) {
	d.meetingUI.locale = value
	d.meetingUI.setupIssue = ""
	d.meetingUI.setupEpoch++
	d.meetingUI.probedAt = time.Time{}
	d.meetingUI.status = ""
	d.app.TimeoutFunc(350*time.Millisecond, func() {
		if d.meetingUI.locale == value && (d.meetingUI.overlayOpen || d.page == "Voice notes") && !d.meetingUI.probing {
			d.probeMeetingLanguage()
		}
	})
}

func (d *desktop) startMeeting() {
	if d.voiceSetupDownloading() {
		d.meetingUI.status = "Speech files are downloading. Start Voice notes when the download finishes."
		d.resizeMeetingOverlayToFit()
		return
	}
	d.resetMeetingStopForNewSession()
	title, locale, tracks := d.meetingUI.title, d.meetingUI.locale, d.selectedMeetingTracks()
	id, savedAgenda := d.meetingUI.selected, d.meetingUI.selectedState == meetings.Ready
	d.meetingOperation(func(ctx context.Context) error {
		if savedAgenda && id != "" {
			return d.client.StartMeetingNotes(ctx, id)
		}
		return d.client.StartMeeting(ctx, title, locale, tracks)
	}, func() {
		live := d.client.MeetingLive()
		d.meetingUI.selected, d.meetingUI.selectedTitle = live.Meeting.ID, live.Meeting.Title
		d.meetingUI.selectedAt = live.Meeting.StartedAt
		d.meetingUI.browseSaved = false
		d.meetingUI.transcript = meetings.RecordPage{}
		d.meetingUI.transcriptAt, d.meetingUI.transcriptPrev = 0, nil
		d.meetingUI.loaded = false
	})
}

func (d *desktop) resetMeetingStopForNewSession() {
	if !d.meetingUI.busy && d.client != nil && !voiceRecordingActive(d.client.MeetingLive().State) {
		// Native failure can precede its coalesced UI update. Clear old intent
		// for a fresh session, retaining Stop during an admitted active Start.
		d.meetingUI.stopRequested, d.meetingUI.stopDispatching = false, false
		d.meetingUI.overlayPinned, d.meetingUI.overlayHideArmed = false, false
		d.meetingUI.overlayAutoHideEpoch++
	}
}

func (d *desktop) saveMeetingNote() {
	text, id := d.meetingUI.draft, d.meetingUI.selected
	if strings.TrimSpace(text) == "" {
		return
	}
	if d.meetingUI.noteID == "" {
		d.meetingUI.noteID = meetingID()
		d.meetingUI.noteRecordedAt = time.Now().UTC()
	}
	noteID := d.meetingUI.noteID
	recordedAt := d.meetingUI.noteRecordedAt
	title, locale, tracks := d.meetingUI.title, d.meetingUI.locale, d.selectedMeetingTracks()
	d.meetingOperation(func(ctx context.Context) error {
		if id == "" {
			var err error
			id, err = d.client.NewMeetingNotes(ctx, title, locale, tracks)
			if err != nil {
				return err
			}
		}
		return d.client.AddMeetingNote(ctx, id, meetings.ManualNoteInput{ID: noteID, RecordedAt: recordedAt, Text: text})
	}, func() {
		d.meetingUI.selected = id
		// A later draft typed while this write was running belongs to the user.
		if d.meetingUI.draft == text && d.meetingUI.noteID == noteID {
			d.meetingUI.draft, d.meetingUI.noteID = "", ""
		}
		d.meetingUI.status = "Note saved."
		d.meetingUI.loaded = false
		if d.client.MeetingLive().Meeting.ID != id {
			d.loadMeetingTranscript(0)
		}
	})
}

func (d *desktop) loadMeetingPage(after uint64) {
	if d.client == nil || d.meetingUI.loading {
		return
	}
	d.meetingUI.loading = true
	generation := d.memoryGeneration
	go func() {
		page, err := d.client.MeetingPage(context.Background(), after)
		d.app.Post(func() {
			d.meetingUI.loading = false
			if generation != d.memoryGeneration {
				return
			}
			if err != nil {
				d.meetingUI.status = meetingErrorCopy(err)
			} else {
				d.meetingUI.page, d.meetingUI.after, d.meetingUI.loaded = page, after, true
			}
			d.app.RequestUpdate()
		})
	}()
}

func (d *desktop) loadMeetingTranscript(after uint64) {
	if d.client == nil {
		return
	}
	id := d.meetingUI.selected
	if d.meetingUI.summaryCancel != nil {
		d.meetingUI.summaryCancel()
	}
	d.meetingUI.summary, d.meetingUI.summaryLoading = nil, false
	d.meetingUI.transcript = meetings.RecordPage{}
	d.meetingUI.reading = true
	d.meetingUI.request++
	request, generation := d.meetingUI.request, d.memoryGeneration
	go func() {
		page, read, err := d.client.readMeetingTranscript(context.Background(), id, after)
		d.app.Post(func() {
			if generation != d.memoryGeneration || request != d.meetingUI.request || id != d.meetingUI.selected {
				return
			}
			d.meetingUI.reading = false
			if err != nil {
				if errors.Is(err, ErrMemoryChanging) {
					d.meetingUI.status = voiceReadChangedCopy
				} else {
					d.meetingUI.status = meetingErrorCopy(err)
				}
			} else if !read.publish(d.client, func() { d.meetingUI.transcript, d.meetingUI.transcriptAt = page, after }) {
				d.meetingUI.status = voiceReadChangedCopy
			}
			d.app.RequestUpdate()
		})
	}()
}

func (d *desktop) summarizeMeetingPage() {
	u := &d.meetingUI
	if d.client == nil || u.summaryLoading || u.selected == "" || len(u.transcript.Records) == 0 {
		return
	}
	id, after, request := u.selected, u.transcriptAt, u.request
	read, stable := d.client.beginVoicePublication()
	if !stable {
		u.status = voiceReadChangedCopy
		d.app.RequestUpdate()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	u.summaryCancel, u.summaryLoading, u.summary = cancel, true, nil
	u.status = ""
	d.app.RequestUpdate()
	go func() {
		result, err := d.client.SummarizeMeetingPage(ctx, id, after)
		cancel()
		d.app.Post(func() {
			if u.selected != id || u.request != request {
				return
			}
			u.summaryLoading, u.summaryCancel = false, nil
			if !read.publish(d.client, func() {}) {
				u.summary, u.transcript, u.status = nil, meetings.RecordPage{}, voiceReadChangedCopy
				d.app.RequestUpdate()
				return
			}
			if err == nil && result.Publication != nil && !result.Publication.Publish(func() { u.summary = &result }) {
				u.transcript = meetings.RecordPage{}
				err = ErrMemoryChanging
			}
			if err != nil {
				if errors.Is(err, ErrMemoryChanging) {
					u.status = voiceReadChangedCopy
				} else {
					u.status = meetingSummaryErrorCopy(err, effectiveProvider(d.client.LocalAISettings()))
				}
			} else if result.Publication == nil {
				u.summary = &result
			}
			d.app.RequestUpdate()
		})
	}()
}

const voiceReadChangedCopy = "Memory is changing. Open this voice note again to load its current saved words."

func meetingErrorCopy(err error) string {
	var operation darwin.MeetingAudioOperationError
	if errors.As(err, &operation) {
		return meetingIssueCopy(operation.Code)
	}
	if errors.Is(err, darwin.ErrMeetingAudioUnavailable) {
		return "Local transcription is unavailable in this build. Install the complete macOS app; your typed notes still work."
	}
	if errors.Is(err, darwin.ErrMeetingAudioControl) || errors.Is(err, meetings.ErrState) {
		return "This voice note is changing state. Wait for its status before trying again."
	}
	if errors.Is(err, meetings.ErrDeleted) || errors.Is(err, ErrMemoryChanging) {
		return "Memory changed. Recording stopped; reload the saved note before continuing. Your draft is still here."
	}
	return "That note action could not finish. Saved text is kept, and your draft is still here. Try again."
}

func meetingIssueCopy(code string) string {
	switch code {
	case "":
		return ""
	case "assets_missing":
		return "This speech language needs a one-time download. Choose Download & start to install it on this Mac."
	case "locale_unsupported":
		return "Apple speech recognition does not support this language. Choose a supported language in Settings → Capture → Voice notes."
	case "speech_unavailable", "unsupported_os", "setup_unavailable":
		return "Local transcription requires macOS 26 or later and a supported speech language. Typed notes are available."
	case "microphone_permission":
		return "Microphone access is not allowed. Enable Blur in System Settings → Privacy & Security → Microphone, then start again."
	case "system_permission":
		return "System audio access is not allowed. Enable Blur in System Settings → Privacy & Security → Screen & System Audio Recording, then start again."
	case "memory_changed":
		return "Recording stopped because saved memory changed."
	case "save_failed":
		return "Transcription stopped because a segment could not be saved. Previously saved text remains available."
	case "audio_overflow", "audio_capture_overflow", "audio_analyzer_overflow":
		return "Audio processing could not keep up. Saved words remain available in Ask; start a new recording to continue."
	case "audio_interrupted", "audio_capture_stopped", "audio_analyzer_stopped", "device_changed", "device_failed", "device_unavailable":
		return "The audio source was interrupted. Saved words remain available in Ask; check the selected audio source in Settings and start again."
	case "transcription_failed":
		return "Speech recognition stopped. Saved words remain available in Ask; start a new recording to continue."
	case "event_overflow", "limit_reached":
		return "Transcription stopped at a processing limit. Saved segments remain available; start a new voice note to continue."
	default:
		return "Transcription stopped unexpectedly. Saved segments remain available; check audio permissions and start again."
	}
}

func meetingStateCopy(state string) string {
	switch state {
	case "starting":
		return "Starting local transcription…"
	case "running":
		return "Recording"
	case "pausing":
		return "Pausing…"
	case "paused":
		return "Paused · audio is off"
	case "stopping":
		return "Finishing the last words…"
	case "completed":
		return "Finished · saved locally"
	case "failed":
		return "Transcription stopped"
	case "ready":
		return "Notes only · audio is off"
	default:
		return "Audio is off"
	}
}

func (d *desktop) meetingsView() ui.View { return d.askView() }

func (d *desktop) meetingTranscriptView() ui.View {
	u := &d.meetingUI
	rows := []ui.View{}
	if u.selectedTitle != "" {
		rows = append(rows, wrapLabel(u.selectedTitle).Style(styleSectionTitle))
	}
	recapping := d.memoryUI.recap.id == u.selected && d.memoryUI.recap.generation == d.memoryGeneration && d.memoryUI.recap.loading
	active := u.selected == u.live.Meeting.ID && (u.live.AudioRecording || voiceRecordingActive(u.live.State))
	disclosure := "Show transcript"
	if u.transcriptExpanded {
		disclosure = "Hide transcript"
	}
	rows = append(rows, ui.HBox(
		primaryButton("Recap voice note").Enabled(u.selected != "" && !u.busy && !recapping && !active).OnClick(d.recapSelectedVoiceNote),
		flatButton(disclosure).OnClick(func() { u.transcriptExpanded = !u.transcriptExpanded; d.app.RequestUpdate() }),
	).Spacing(8))
	if u.transcriptExpanded {
		rows = append(rows, wrapLabel("Transcript").Style(styleSectionTitle))
		audioLabel := func(track string) string {
			if track == "microphone" {
				return "Microphone audio"
			}
			return "Recorded audio"
		}
		live := u.selected != "" && u.selected == u.live.Meeting.ID && !u.browseSaved
		records := u.transcript.Records
		if live {
			records = u.live.Tail
		}
		if live && u.live.TailOlder {
			rows = append(rows, wrapLabel("Earlier saved words are available in the full transcript.").Style(styleMeta))
		}
		if len(records) == 0 && !u.reading && (!live || !u.live.TailOlder) {
			rows = append(rows, wrapLabel("No saved words on this page.").Style(modern.MutedText))
		}
		if u.reading && !live {
			rows = append(rows, wrapLabel("Loading saved text…").Style(styleMeta))
		}
		for _, row := range records {
			text, label := row.Record.Event.Text, audioLabel(row.Record.Event.TrackID)
			at := row.Record.Event.Start
			if row.Record.Kind == meetings.UserNote {
				text, label, at = row.Record.Note.Text, "Your note", row.Record.Note.At
			}
			when := fmt.Sprintf("%02d:%02d · %s", int(at/time.Minute), int(at/time.Second)%60, label)
			if row.Record.Kind == meetings.UserNote && !row.Record.Note.RecordedAt.IsZero() {
				when = darwin.LocalizedDateTime(row.Record.Note.RecordedAt, localZoneName()) + " · Your note"
			}
			rows = append(rows, wrapLabel(when).Style(styleMeta), wrapLabel(text))
		}
		if live {
			for _, track := range []string{"microphone", "system"} {
				if text := u.live.Preview[track]; text != "" {
					rows = append(rows, wrapLabel(audioLabel(track)+" · live preview").Style(styleMeta), wrapLabel(text).Style(modern.MutedText))
				}
			}
			rows = append(rows, flatButton("Read saved transcript from the beginning").Enabled(!u.reading).OnClick(func() { u.browseSaved = true; d.loadMeetingTranscript(0) }))
		} else if u.selected != "" {
			rows = append(rows, ui.HBox(
				flatButton("Earlier").Enabled(!u.reading && len(u.transcriptPrev) > 0).OnClick(func() {
					at := u.transcriptPrev[len(u.transcriptPrev)-1]
					u.transcriptPrev = u.transcriptPrev[:len(u.transcriptPrev)-1]
					d.loadMeetingTranscript(at)
				}),
				flatButton("Later").Enabled(!u.reading && u.transcript.More).OnClick(func() {
					u.transcriptPrev = append(u.transcriptPrev, u.transcriptAt)
					d.loadMeetingTranscript(u.transcript.Next)
				}),
			).Spacing(8))
			rows = append(rows, flatButton("Summarize this page").Enabled(!u.reading && !u.summaryLoading && len(records) > 0).OnClick(d.summarizeMeetingPage))
			if u.summaryLoading {
				rows = append(rows, wrapLabel("Summarizing the saved text on this page…").Style(styleMeta))
			}
			if u.summary != nil {
				rows = append(rows, wrapLabel(u.summary.Answer.Text))
				if u.summary.CoverageLimited {
					rows = append(rows, wrapLabel("This summary covers excerpts from this page. Other parts of the note may be missing.").Style(styleMeta))
				}
			}
		}
	}
	if u.selected != "" {
		if u.deletePending == u.selected {
			rows = append(rows, wrapLabel("Delete this voice note and its saved transcript and notes?"), ui.HBox(
				flatButton("Cancel").OnClick(func() { u.deletePending = ""; d.app.RequestUpdate() }),
				primaryButton("Delete voice note").Enabled(!u.busy).OnClick(func() {
					id := u.selected
					d.meetingOperation(func(ctx context.Context) error { return d.client.DeleteMeeting(ctx, id) }, func() {
						u.selected, u.selectedTitle, u.deletePending = "", "", ""
						u.transcript = meetings.RecordPage{}
						d.memoryUI.meetingDetail, d.memoryUI.loaded = false, false
						d.memoryUI.previous = nil
						d.loadMemoryBrowse(memoryBrowseCursor{})
					}, true)
				}),
			).Spacing(8))
		} else {
			rows = append(rows, flatButton("Delete voice note…").Enabled(!u.busy).OnClick(func() { u.deletePending = u.selected; d.app.RequestUpdate() }))
		}
	}
	return surfaceBox(16, 10, rows...)
}
