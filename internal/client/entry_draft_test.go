package client

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/intent"
	"github.com/hazcod/blur/internal/reminders"
)

func TestDelayedIntentAndAnswerPreserveNewerDraft(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "answer", true: "error"}[fail], func(t *testing.T) {
			entered, release, answerStarted, answerRelease := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
			var releaseOnce, answerReleaseOnce sync.Once
			defer func() {
				releaseOnce.Do(func() { close(release) })
				answerReleaseOnce.Do(func() { close(answerRelease) })
			}()
			intentCalls := 0
			g := &singleSearchModel{respond: func(_ context.Context, system, _ string) (string, error) {
				if strings.Contains(system, "Classify the requested operation") {
					intentCalls++
					if intentCalls == 1 {
						close(entered)
						<-release
						return `{"kind":"ask_general"}`, nil
					}
					return "", errors.New("question routing repeated native inference")
				}
				close(answerStarted)
				<-answerRelease
				if fail {
					return "", errors.New("synthetic answer unavailable")
				}
				return "A compiler translates source.", nil
			}}
			c := selectedEntryClient(t, ProviderApple, false, g)
			c.data = &testData{}
			c.localAI.askModel = meetingPasteAnswerModel{g}
			app := &meetingPasteApp{&pausedAskApp{posted: make(chan func(), 32)}}
			d := &desktop{app: app, client: c, quickInput: "Compiler examples"}
			d.submitQuickPanelInput()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("intent generation did not start")
			}
			d.setQuickTypedInput("new unsent draft")
			releaseOnce.Do(func() { close(release) })
			deadline := time.NewTimer(3 * time.Second)
			defer deadline.Stop()
			for d.quickEntryLoading {
				select {
				case publish := <-app.posted:
					publish()
				case <-deadline.C:
					t.Fatal("intent did not publish")
				}
			}
			select {
			case <-answerStarted:
			case <-time.After(3 * time.Second):
				t.Fatalf("resolved answer did not start: status=%q entryLoading=%t askLoading=%t calls=%d", d.status, d.quickEntryLoading, d.askLoading, intentCalls)
			}
			if d.quickInput != "new unsent draft" {
				t.Errorf("intent completion erased newer draft: %q", d.quickInput)
			}
			answerReleaseOnce.Do(func() { close(answerRelease) })
			for d.askLoading {
				select {
				case publish := <-app.posted:
					publish()
				case <-deadline.C:
					t.Fatal("answer did not publish")
				}
			}
			if d.quickInput != "new unsent draft" || len(d.quickVisibleTurns()) != 0 {
				t.Fatal("answer completion replaced the newer draft or revealed earlier history")
			}
		})
	}
}

func TestDelayedWriteResolutionPreservesDifferentAndRetypedDrafts(t *testing.T) {
	for _, kind := range []intent.Kind{intent.Note, intent.Task, intent.Reminder, intent.Fact, intent.Rule} {
		for _, same := range []bool{false, true} {
			for _, fail := range []bool{false, true} {
				name := string(kind) + "/" + map[bool]string{false: "different", true: "retyped"}[same] + "/" + map[bool]string{false: "saved", true: "error"}[fail]
				t.Run(name, func(t *testing.T) {
					c, store, now := todayTaskClient(t)
					app := &meetingPasteApp{&pausedAskApp{posted: make(chan func(), 64)}}
					const input = "Original submitted draft"
					d := &desktop{app: app, client: c, quickInput: input}
					revision := d.quickUI.draftRevision
					epoch, _ := c.BeginMemoryRead()
					// Resolution began against the original input; the user edits
					// while it is outstanding, including deleting/retyping it.
					d.setQuickTypedInput("new unsent draft")
					want := "new unsent draft"
					if same {
						d.setQuickTypedInput(input)
						want = input
					}
					r := EntryResolution{Epoch: epoch, Action: intent.Action{Kind: kind, Text: input}}
					if kind == intent.Reminder {
						draft, err := reminders.Parse("remind me tomorrow at 9am to "+input, now, "UTC")
						if err != nil || draft.Missing != "" {
							t.Fatal(draft, err)
						}
						r.Draft = &draft
					}
					if fail {
						if err := store.Close(); err != nil {
							t.Fatal(err)
						}
					}
					d.dispatchEntryResolution(input, r, "UTC", revision)
					deadline := time.NewTimer(3 * time.Second)
					defer deadline.Stop()
					for d.loading {
						select {
						case publish := <-app.posted:
							publish()
						case <-deadline.C:
							t.Fatalf("write did not finish: %s", d.status)
						}
					}
					if d.quickInput != want || d.quickUI.savedFlash || len(d.quickVisibleTurns()) != 0 {
						t.Fatalf("old write affected new draft: input=%q want=%q flash=%t visible=%d status=%q", d.quickInput, want, d.quickUI.savedFlash, len(d.quickVisibleTurns()), d.status)
					}
					if !fail && quickPanelShowsDraftStatus(d.quickInput, d.status) {
						t.Fatalf("old save receipt appeared beside a newer unsent draft: %q", d.status)
					}
					if fail && !strings.Contains(strings.ToLower(d.status), "earlier") {
						t.Fatalf("old write failure lost its original request identity: %q", d.status)
					}
				})
			}
		}
	}
}
