package client

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/golang-gui/goui/theme/modern"
	"github.com/golang-gui/goui/ui"
	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/meetings"
	"github.com/hazcod/blur/internal/model"
)

const voiceRecapMaxPages = 64
const voiceRecapTimeout = 10 * time.Minute

type voiceRecapPart struct {
	result      assistant.Result
	first, last uint64
}

type voiceRecapViewState struct {
	id                         string
	request, generation        uint64
	cancel                     context.CancelFunc
	loading, complete, partial bool
	resumeEditor               bool
	processed, total, skipped  uint64
	invalid                    uint64
	parts                      []voiceRecapPart
	status                     string
}

func (d *desktop) invalidateVoiceRecap() {
	u := &d.memoryUI.recap
	request := u.request + 1
	if u.cancel != nil {
		u.cancel()
	}
	*u = voiceRecapViewState{request: request}
}

// The explicit Stop-completed callback posts this action after leaving its
// publication fence. Opening an archive, app startup and failed stops never
// call it; it also rejects an unrelated session or a currently running capture.
func (d *desktop) openFinishedVoiceRecap(id string) {
	if d == nil || d.client == nil || d.app == nil || id == "" || d.meetingUI.live.State != "completed" || d.meetingUI.live.Meeting.ID != id {
		return
	}
	d.beginVoiceRecap(d.meetingUI.live.Meeting, true)
}

// An archive is summarized only after this explicit action. It stays in the
// current Ask detail and cannot open or arm the recorder or replace an editor.
func (d *desktop) recapSelectedVoiceNote() {
	if d == nil || d.client == nil || d.app == nil || d.page != "Ask" || d.noteUI.open || !d.memoryUI.meetingDetail || d.meetingUI.selected == "" || d.memoryUI.recap.loading {
		return
	}
	u := &d.meetingUI
	if u.selected == u.live.Meeting.ID && (u.live.AudioRecording || voiceRecordingActive(u.live.State)) {
		return
	}
	d.beginVoiceRecap(meetings.Meeting{ID: u.selected, Title: u.selectedTitle, StartedAt: u.selectedAt, State: u.selectedState, RecordCount: u.selectedRecordCount}, false)
}

func (d *desktop) beginVoiceRecap(meeting meetings.Meeting, reveal bool) {
	if d == nil || d.client == nil || d.app == nil || meeting.ID == "" {
		return
	}
	id := meeting.ID
	epoch, stable := d.client.BeginMemoryRead()
	if !stable {
		return
	}
	resumeEditor := d.noteUI.open || d.memoryUI.recap.resumeEditor
	d.invalidateVoiceRecap()
	u := &d.memoryUI.recap
	if reveal {
		d.hideQuickPanel()
	}
	started := false
	if !d.client.PublishMemoryRead(epoch, func() {
		if !reveal && (d.page != "Ask" || d.noteUI.open || !d.memoryUI.meetingDetail || d.meetingUI.selected != id) {
			return
		}
		u.id, u.loading, u.generation = id, true, d.memoryGeneration
		u.total = meeting.RecordCount
		u.status = "Preparing a recap from the saved words…"
		u.resumeEditor = resumeEditor
		if reveal {
			d.noteUI.open = false // Keep an interrupted Markdown draft for Ask navigation.
			d.page, d.dashboardOpen, d.quickUI.preferQuick = "Ask", true, false
			d.selectMemoryMeeting(meeting)
		}
		started = true
	}) {
		return
	}
	if !started {
		return
	}
	if reveal {
		d.loadMeetingTranscript(0)
	}
	ctx, cancel := context.WithTimeout(context.Background(), voiceRecapTimeout)
	u.cancel = cancel
	request, generation := u.request, u.generation
	d.app.RequestUpdate()
	if reveal {
		d.app.Post(func() {
			if d.voiceRecapCurrent(id, request, generation) {
				d.presentDashboard(0)
			}
		})
	}
	go func() {
		defer cancel()
		d.runFinishedVoiceRecap(ctx, id, epoch, request, generation, voiceRecapMaxPages)
	}()
}

func (d *desktop) voiceRecapCurrent(id string, request, generation uint64) bool {
	u := &d.memoryUI.recap
	return u.id == id && u.request == request && generation == d.memoryGeneration && u.generation == generation
}

func (d *desktop) runFinishedVoiceRecap(ctx context.Context, id string, epoch, request, generation uint64, pageLimit int) {
	var after uint64
	for step := 0; step < pageLimit; step++ {
		result, page, err := d.client.SummarizeLocalMeetingPage(ctx, id, after)
		insufficient := errors.Is(err, assistant.ErrSummaryInsufficientEvidence)
		invalid := errors.Is(err, assistant.ErrSummaryInvalidResponse)
		unusable := insufficient || invalid
		if err != nil && !unusable {
			d.finishVoiceRecap(id, epoch, request, generation, err)
			return
		}
		if unusable && len(page.Records) == 0 {
			d.finishVoiceRecap(id, epoch, request, generation, err)
			return
		}
		if len(page.Records) == 0 || page.Next <= after {
			d.finishVoiceRecap(id, epoch, request, generation, ErrInvalidRequest)
			return
		}
		if !unusable && (result.Answer.InsufficientEvidence || result.Answer.Text == "" || result.Direct || result.General || result.GenerationIssue != "" || len(result.Sources) == 0) {
			d.finishVoiceRecap(id, epoch, request, generation, assistant.ErrAnswer)
			return
		}
		part := voiceRecapPart{result: result, first: page.Records[0].Sequence, last: page.Records[len(page.Records)-1].Sequence}
		more, count := page.More, uint64(len(page.Records))
		d.app.Post(func() {
			if !d.voiceRecapCurrent(id, request, generation) {
				return
			}
			if !d.client.PublishMemoryRead(epoch, func() {
				u := &d.memoryUI.recap
				if unusable {
					u.skipped += count
					if invalid {
						u.invalid += count
					}
				} else {
					u.parts = append(u.parts, part)
				}
				u.processed += count
				u.partial = u.partial || part.result.CoverageLimited || unusable
				u.status = fmt.Sprintf("Recapping saved passages · %d of %d read", u.processed, max(u.processed, u.total))
				if !more {
					u.loading, u.complete, u.cancel = false, len(u.parts) > 0, nil
					u.status = ""
					if u.skipped > 0 {
						u.status = fmt.Sprintf("The model could not summarize %d saved passages. Those words remain in the transcript below.", u.skipped)
					}
					if len(u.parts) == 0 {
						u.status = meetingSummaryErrorCopy(assistant.ErrSummaryInsufficientEvidence, effectiveProvider(d.client.LocalAISettings()))
					}
					if u.invalid > 0 {
						u.status = fmt.Sprintf("The model returned an unverifiable recap for %d saved passages. Those words remain in the transcript below.", u.invalid)
					}
				}
			}) {
				return
			}
			d.app.RequestUpdate()
		})
		if !more {
			return
		}
		after = page.Next
	}
	d.finishVoiceRecap(id, epoch, request, generation, errVoiceRecapLimit)
}

var errVoiceRecapLimit = errors.New("voice recap processing limit")

func (d *desktop) finishVoiceRecap(id string, epoch, request, generation uint64, err error) {
	d.app.Post(func() {
		if !d.voiceRecapCurrent(id, request, generation) {
			return
		}
		if !d.client.PublishMemoryRead(epoch, func() {
			u := &d.memoryUI.recap
			u.loading, u.partial = false, true
			if u.cancel != nil {
				u.cancel()
				u.cancel = nil
			}
			switch {
			case errors.Is(err, errVoiceRecapLimit), errors.Is(err, context.DeadlineExceeded):
				u.status = "This recap reached its processing limit. Later saved passages are not included; the full transcript remains below."
			default:
				u.status = meetingSummaryErrorCopy(err, effectiveProvider(d.client.LocalAISettings()))
			}
		}) {
			return
		}
		d.app.RequestUpdate()
	})
}

func meetingSummaryErrorCopy(err error, provider string) string {
	switch {
	case errors.Is(err, assistant.ErrAskUnavailable), errors.Is(err, model.ErrAppleUnavailable):
		return "An on-device recap is unavailable with the current model. The saved transcript remains below."
	case errors.Is(err, assistant.ErrSummaryInsufficientEvidence):
		return "The saved words did not give the model enough understandable content to summarize. The transcript remains below."
	case errors.Is(err, assistant.ErrSummaryInvalidResponse), errors.Is(err, assistant.ErrAnswer), errors.Is(err, assistant.ErrGeneratorResponse), errors.Is(err, model.ErrAppleResponse), errors.Is(err, model.ErrOllamaResponse), errors.Is(err, model.ErrCompatibleResponse), errors.Is(err, model.ErrAnthropicResponse):
		return "The model returned a recap that could not be verified. The saved transcript remains below."
	case errors.Is(err, assistant.ErrTokenBudget), errors.Is(err, model.ErrAppleBudget):
		return "This transcript could not fit the model’s available context. The saved transcript remains below."
	case errors.Is(err, model.ErrAppleRefused):
		return "The on-device model declined to summarize this transcript. The saved words remain below."
	default:
		return askErrorMessage(err, provider)
	}
}

func (d *desktop) voiceRecapViews() []ui.View {
	u := &d.memoryUI.recap
	if u.id == "" || u.id != d.meetingUI.selected || u.generation != d.memoryGeneration {
		return nil
	}
	rows := []ui.View{wrapLabel("Voice note recap").Style(styleSectionTitle)}
	if u.status != "" {
		rows = append(rows, wrapLabel(u.status).Style(modern.MutedText))
	}
	for _, part := range u.parts {
		if text := part.result.Answer.Text; text != "" && !part.result.Answer.InsufficientEvidence && !part.result.Direct && !part.result.General && part.result.GenerationIssue == "" && len(part.result.Sources) > 0 {
			rows = append(rows, wrapLabel(text), wrapLabel(fmt.Sprintf("Read passages %d–%d", part.first, part.last)).Style(styleMeta))
		}
	}
	if u.complete && !u.partial {
		rows = append(rows, wrapLabel(fmt.Sprintf("All %d saved passages were read for this recap. The original transcript follows.", u.processed)).Style(styleMeta))
	} else if u.partial && len(u.parts) > 0 {
		rows = append(rows, wrapLabel("This recap covers selected excerpts. Some text did not fit the model or could not be summarized.").Style(styleMeta))
	}
	return append(rows, hairline())
}
