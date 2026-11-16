package client

import (
	"strings"
	"testing"
)

func TestVoiceNotesSidebarOpensHistoryWithoutStartingRecording(t *testing.T) {
	app := &voiceMeterApp{}
	d := &desktop{app: app, page: "Settings", dashboardOpen: true}
	d.activateSidebarPage("Voice notes")
	if d.page != "Ask" || d.meetingUI.overlayOpen || d.meetingUI.startOnShow || d.meetingUI.busy {
		t.Fatal("Voice notes did not open history independently of the recorder")
	}
}

func TestSavedVoiceTranscriptStartsCollapsedAndKeepsOriginalPaging(t *testing.T) {
	c, _, model, meeting := summaryFixture(t, 25)
	meeting.RecordCount = 25
	app := &meetingOperationTestApp{posted: make(chan func(), 128)}
	d := &desktop{app: app, client: c, page: "Ask", tokens: themePalette(false)}
	d.openMemoryMeeting(meeting)
	app.finish(t)
	_, buttons, text := memoryWidgetTree(t, d)
	if strings.Contains(text, "Saved decision 0:") || len(buttons["Show transcript"]) != 1 || len(buttons["Recap voice note"]) != 1 || len(buttons["Delete voice note…"]) != 1 {
		t.Fatal("saved voice note exposed transcript by default or hid its main actions", text)
	}
	clickMemoryButton(t, buttons, "Show transcript")
	_, buttons, text = memoryWidgetTree(t, d)
	if !strings.Contains(text, "Saved decision 0:") || !strings.Contains(text, "Saved decision 19:") || strings.Contains(text, "Saved decision 20:") || !strings.Contains(text, "Microphone audio") || len(buttons["Hide transcript"]) != 1 {
		t.Fatal("expanded transcript lost the original page or honest source label", text)
	}
	clickMemoryButton(t, buttons, "Later")
	app.finish(t)
	_, buttons, text = memoryWidgetTree(t, d)
	if !strings.Contains(text, "Saved decision 24:") {
		t.Fatal("expanded transcript lost saved paging", text)
	}
	clickMemoryButton(t, buttons, "Hide transcript")
	_, buttons, text = memoryWidgetTree(t, d)
	if strings.Contains(text, "Saved decision 24:") || len(buttons["Later"]) != 0 {
		t.Fatal("collapsed transcript still rendered raw words or page controls", text)
	}
	clickMemoryButton(t, buttons, "Show transcript")
	_, _, text = memoryWidgetTree(t, d)
	if !strings.Contains(text, "Saved decision 24:") {
		t.Fatal("disclosure reset the original transcript cursor", text)
	}
	d.openMemoryMeeting(meeting)
	app.finish(t)
	_, buttons, text = memoryWidgetTree(t, d)
	if strings.Contains(text, "Saved decision") || len(buttons["Show transcript"]) != 1 || model.calls != 0 || c.MeetingLive().AudioRecording {
		t.Fatal("reopening voice note retained disclosure or started inference/recording", text)
	}
}

func TestSavedVoiceTranscriptLabelsSystemAudioWithoutInventingSpeakers(t *testing.T) {
	f, _ := savedVoiceVideoFixture(t)
	page, err := f.c.MeetingPage(t.Context(), 0)
	if err != nil || len(page.Meetings) != 1 {
		t.Fatal("synthetic recorded-audio note was not saved", err)
	}
	app := &meetingOperationTestApp{posted: make(chan func(), 128)}
	d := &desktop{app: app, client: f.c, page: "Ask", tokens: themePalette(false)}
	d.openMemoryMeeting(page.Meetings[0])
	app.finish(t)
	_, buttons, _ := memoryWidgetTree(t, d)
	clickMemoryButton(t, buttons, "Show transcript")
	_, _, text := memoryWidgetTree(t, d)
	if !strings.Contains(text, "00:00 · Recorded audio") || strings.Contains(text, "· system") || strings.Contains(text, "Speaker 1") || strings.Contains(text, "Microphone audio") {
		t.Fatal("recorded-audio transcript invented a speaker or exposed an internal track label", text)
	}
}
