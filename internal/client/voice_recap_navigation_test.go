package client

import (
	"context"
	"testing"

	"github.com/hazcod/blur/internal/assistant"
)

func TestNewInputCancelsObsoleteRecapWithoutLosingSuspendedDraft(t *testing.T) {
	for _, surface := range []string{"Ask", "popup"} {
		t.Run(surface, func(t *testing.T) {
			c, _, _ := todayTaskClient(t)
			app := &meetingOperationTestApp{posted: make(chan func(), 32)}
			d := &desktop{app: app, client: c, page: "Ask", askQuestion: "2+2", quickInput: "2+2"}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			d.noteUI.draft = "An interrupted Markdown draft"
			d.memoryUI.recap = voiceRecapViewState{id: "old-note", request: 7, loading: true, resumeEditor: true, cancel: cancel, parts: []voiceRecapPart{{result: assistant.Result{Answer: assistant.Answer{Text: "A previously valid recap."}}}}}
			if surface == "Ask" {
				d.submitMemoryQuery()
			} else {
				d.submitQuickPanelInput()
			}
			if ctx.Err() == nil || d.memoryUI.recap.loading || d.memoryUI.recap.request == 7 || !d.memoryUI.recap.resumeEditor || len(d.memoryUI.recap.parts) != 1 || d.noteUI.draft != "An interrupted Markdown draft" {
				t.Fatal("new input waited behind a recap or lost the interrupted draft")
			}
			finishMemoryEntry(t, app, d)
			if d.status != "" || d.askLoading || d.loading {
				t.Fatal("a local calculation did not finish after canceling the recap", d.status)
			}
		})
	}
}
