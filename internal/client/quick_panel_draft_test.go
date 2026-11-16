package client

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestQuickPanelLateAnswerPreservesNewDraft(t *testing.T) {
	for _, edit := range []string{"typing", "history", "completion"} {
		for _, fail := range []bool{false, true} {
			t.Run(edit+"/"+map[bool]string{false: "answer", true: "error"}[fail], func(t *testing.T) {
				started, release := make(chan struct{}), make(chan struct{})
				g := &singleSearchModel{respond: func(context.Context, string, string) (string, error) {
					close(started)
					<-release
					if fail {
						return "", errors.New("synthetic model failure")
					}
					return "A compiler translates source.", nil
				}}
				c := selectedEntryClient(t, ProviderApple, false, g)
				c.data = &testData{}
				c.localAI.askModel = meetingPasteAnswerModel{g}
				app := &meetingPasteApp{&pausedAskApp{posted: make(chan func(), 32)}}
				d := &desktop{app: app, client: c, quickAskPending: true, quickAskMode: "general", askQuestion: "Explain a compiler"}
				d.askMemory()
				select {
				case <-started:
				case <-time.After(3 * time.Second):
					close(release)
					t.Fatal("answer generation did not start")
				}
				switch edit {
				case "typing":
					d.setQuickTypedInput("new unsent draft")
				case "history":
					d.quickHistory.Record("new unsent draft")
					d.navigateQuickPanel(-1)
				case "completion":
					d.quickHistory.Record("new unsent draft")
					value, ok := d.quickHistory.Complete("new")
					if !ok {
						t.Fatal("fixture completion unavailable")
					}
					d.setQuickHistoryInput(value)
				}
				close(release)
				deadline := time.NewTimer(3 * time.Second)
				defer deadline.Stop()
				for d.askLoading {
					select {
					case publish := <-app.posted:
						publish()
					case <-deadline.C:
						t.Fatal("answer did not publish")
					}
				}
				if d.quickInput != "new unsent draft" || len(d.quickVisibleTurns()) != 0 {
					t.Fatal("late answer/error overwrote the new draft or revealed history")
				}
				if !fail && (!d.navigateQuickPanel(1) || len(d.quickVisibleTurns()) != 1) {
					t.Fatal("completed answer was not retained for Down")
				}
			})
		}
	}
}

func TestQuickPanelCancelPreservesNewDraft(t *testing.T) {
	d := &desktop{quickAskPending: true, askLoading: true, askQuestion: "Earlier question"}
	d.setQuickTypedInput("new draft")
	d.invalidateAsk()
	if d.quickInput != "new draft" {
		t.Fatal("canceling an earlier question replaced the new draft")
	}
}

func TestQuickPanelLateNoteSavePreservesNewDraft(t *testing.T) {
	d := &desktop{quickNotePending: true, quickInput: "submitted note"}
	revision := d.quickUI.draftRevision
	d.setQuickTypedInput("new note draft")
	if cleared := d.quickPanelNoteSaved("submitted note", revision); cleared || d.quickInput != "new note draft" || d.quickUI.savedFlash {
		t.Fatal("late note save cleared or dismissed the new draft")
	}
}
