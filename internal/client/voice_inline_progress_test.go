package client

import (
	"testing"

	"github.com/golang-gui/goui/gui"
)

func TestVoiceLoadingProgressUsesTheTranscribedWordsSlot(t *testing.T) {
	for _, tc := range []struct {
		state, want string
		downloading bool
	}{
		{state: "starting", want: "Preparing local transcription…"},
		{state: "pausing", want: "Pausing…"},
		{state: "stopping", want: "Finishing the last words…"},
		{state: "failed", downloading: true, want: "Downloading speech files…"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			f := newControllerFixture(t)
			d := &desktop{client: f.c, app: &quickDashboardApp{}, meetingUI: meetingViewState{live: MeetingLiveView{State: tc.state, LivePhrase: "Stale spoken words"}}}
			d.setupChecks.downloading = tc.downloading
			found := 0
			var walk func(gui.Widget)
			walk = func(w gui.Widget) {
				if label, ok := w.(*gui.Label); ok {
					if label.Text() == "Stale spoken words" {
						t.Fatal("progress did not replace the transcribed words")
					}
					if label.Text() == tc.want {
						found++
						if _, inline := label.Parent().(*voicePhraseViewport); !inline {
							t.Fatal("loading text adds a separate row outside the phrase slot")
						}
					}
				}
				for _, child := range w.Children() {
					walk(child)
				}
			}
			walk(mountVoicePopupView(t, d.meetingOverlay()))
			if found != 1 {
				t.Fatal("inline progress is missing or duplicated", found)
			}
		})
	}
}
