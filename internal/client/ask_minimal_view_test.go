package client

import (
	"strings"
	"testing"

	"github.com/golang-gui/goui/gui"
)

func TestAskOmitsExtraNavigationAndCaptureInformation(t *testing.T) {
	c, _, _, meeting := summaryFixture(t, 1)
	d := &desktop{app: &quickDashboardApp{}, client: c, page: "Ask", tokens: themePalette(false), status: "Capture is on for the currently focused app.", memoryUI: memoryViewState{loaded: true}}
	var labels []string
	var collect func(gui.Widget)
	collect = func(w gui.Widget) {
		if label, ok := w.(*gui.Label); ok {
			labels = append(labels, label.Text())
		}
		for _, child := range w.Children() {
			collect(child)
		}
	}
	collect(mountVoicePopupView(t, d.header()))
	body, _, _ := memoryWidgetTree(t, d)
	collect(body)
	d.selectMemoryMeeting(meeting)
	body, _, _ = memoryWidgetTree(t, d)
	collect(body)
	text := strings.Join(labels, "\n")
	for _, unwanted := range []string{"Quick ask", "New note", "Capture is on for the currently", "Back to results"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("Ask still shows %q", unwanted)
		}
	}
	d.status = "This note could not be saved. Your draft is kept."
	_, _, text = memoryWidgetTree(t, d)
	if !strings.Contains(text, d.status) {
		t.Fatal("simplifying Ask hid a real error")
	}
}

func TestAskSidebarReturnsFromVoiceDetailAndPreservesSuspendedDraft(t *testing.T) {
	d := &desktop{app: &quickDashboardApp{}, page: "Ask", memoryUI: memoryViewState{loaded: true, meetingDetail: true}}
	d.noteUI.draft = "An unfinished Markdown draft"
	d.memoryUI.recap.resumeEditor = true
	d.activateSidebarPage("Ask")
	if d.memoryUI.meetingDetail || !d.noteUI.open || d.memoryUI.recap.resumeEditor || d.noteUI.draft != "An unfinished Markdown draft" || d.noteUI.discardPending {
		t.Fatal("Ask sidebar did not restore the interrupted draft")
	}
	d.activateSidebarPage("Ask")
	if !d.noteUI.open || !d.noteUI.discardPending || d.noteUI.draft != "An unfinished Markdown draft" {
		t.Fatal("Ask sidebar discarded an edited note without confirmation")
	}
}
