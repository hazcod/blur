package client

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/golang-gui/goui/core/geometry"
	"github.com/golang-gui/goui/gui"
	"github.com/golang-gui/goui/layout"
	"github.com/golang-gui/goui/platform/events"
	"github.com/golang-gui/goui/ui"
)

func TestQuickPanelMountedHistoryKeysPreserveFocusAndDispatchOnce(t *testing.T) {
	for _, draft := range []string{"", "unfinished question"} {
		t.Run(fmt.Sprintf("draft=%q", draft), func(t *testing.T) {
			d := &desktop{quickInput: draft, quickConversation: []quickConversationTurn{
				{question: "Earlier", answer: "Earlier answer"},
				{question: "Repeated", answer: "Identical answer"},
				{question: "Repeated", answer: "Identical answer"},
			}}
			d.quickUI.historyHidden = true
			app := &quickHistoryDispatchApp{widgets: make(map[string]gui.Widget)}
			d.app = app
			root := mountVoicePopupView(t, d.quickPanel())
			var editor *gui.TextInput
			var findInput func(gui.Widget)
			findInput = func(w gui.Widget) {
				if input, ok := w.(*gui.TextInput); ok {
					editor = input
				}
				for _, child := range w.Children() {
					findInput(child)
				}
			}
			findInput(root)
			if editor == nil {
				t.Fatal("regular popup did not mount its composer")
			}
			editor.SetSelection(gui.TextSelection{Anchor: 0, Caret: 0})
			selection := editor.Selection()
			app.widgets["quick-panel-root"] = root
			d.watchQuickPanelKeyboard()
			capture := d.quickUI.keyboardController
			d.watchQuickPanelKeyboard()
			if d.quickUI.keyboardController != capture {
				t.Fatal("showing the same popup installed another keyboard controller")
			}

			// The popup also registers window shortcuts. A consumed capture key
			// must never activate a second navigation handler during bubbling.
			fallbackCalls := 0
			fallback := gui.NewShortcutController()
			for _, key := range []gui.Key{gui.KeyArrowDown, gui.KeyArrowUp} {
				shortcut := gui.NewShortcut(gui.KeyGesture{Key: key})
				shortcut.ConnectActivate(func() { fallbackCalls++ })
				fallback.AddShortcut(shortcut)
			}
			root.AddEventController(fallback)
			host := &quickHistoryKeyHost{root: root, focused: editor}
			dispatch := &gui.EventDispatcher{}
			for _, step := range []struct {
				key    events.Key
				want   int
				repeat bool
			}{
				{events.KeyArrowDown, 2, false},
				{events.KeyArrowDown, 1, false},
				{events.KeyArrowDown, 0, true},
				{events.KeyArrowDown, 0, false},
				{events.KeyArrowUp, 1, false},
				{events.KeyArrowUp, 2, false},
			} {
				handled := false
				if err := dispatch.DispatchEvent(host, events.KeyEvent{EventType: events.KeyDown, Key: step.key, Repeat: step.repeat, Handled: &handled}); err != nil {
					t.Fatal(err)
				}
				if err := dispatch.DispatchEvent(host, events.KeyEvent{EventType: events.KeyUp, Key: step.key}); err != nil {
					t.Fatal(err)
				}
				if !handled || d.quickSelectedTurnIndex() != step.want || fallbackCalls != 0 {
					t.Fatalf("key %v: handled=%t selection=%d want=%d duplicate handlers=%d", step.key, handled, d.quickSelectedTurnIndex(), step.want, fallbackCalls)
				}
				if host.focused != editor || d.quickInput != draft || editor.Text() != draft || editor.Selection() != selection {
					t.Fatal("history navigation changed composer focus, draft or caret")
				}
			}

			// A declarative root replacement must detach the stale controller.
			replacement := mountVoicePopupView(t, d.quickPanel())
			app.widgets["quick-panel-root"] = replacement
			d.watchQuickPanelKeyboard()
			if slices.Contains(root.EventControllers(), gui.EventController(capture)) || !slices.Contains(replacement.EventControllers(), gui.EventController(d.quickUI.keyboardController)) {
				t.Fatal("keyboard controller remained attached to the previous popup root")
			}
		})
	}
}

func TestQuickPanelHistoryScrollRevealsAllTwentyRepeatedTurns(t *testing.T) {
	d := &desktop{}
	for range quickConversationLimit {
		d.quickConversation = appendQuickAction(d.quickConversation, "2+2", "4")
	}
	app := &quickHistoryDispatchApp{widgets: make(map[string]gui.Widget)}
	d.app = app
	content := gui.NewLinearBox(layout.DirectionVertical)
	for index := len(d.quickConversation) - 1; index >= 0; index-- {
		// Fixed row geometry keeps this scroll test independent of native font
		// metrics; mounted editor/event dispatch is exercised above.
		question := gui.NewLinearBox(layout.DirectionVertical)
		question.SetMinSize(geometry.Size{Width: 280, Height: 40})
		question.AddChild(gui.NewLabel(d.quickConversation[index].question))
		content.AddChild(question)
		app.widgets[fmt.Sprintf("quick-question-%d", index)] = question
	}
	scroll := gui.NewScrollView()
	scroll.SetDirection(gui.ScrollVertical)
	scroll.SetChild(content)
	scroll.Arrange(geometry.Rect(0, 0, 320, 120))
	app.widgets["quick-scroll"] = scroll
	for want := quickConversationLimit - 1; want >= 0; want-- {
		d.navigateQuickPanel(1)
		if got := d.quickSelectedTurnIndex(); got != want {
			t.Fatalf("Down selected row %d, want %d", got, want)
		}
		question := app.widgets[fmt.Sprintf("quick-question-%d", want)]
		rect, ok := quickRectInAncestor(question, content)
		if !ok || rect.Height <= 0 || rect.Y < scroll.ScrollY() || rect.Y+rect.Height > scroll.ScrollY()+scroll.Rect().Height {
			t.Fatalf("selected row %d is outside viewport: question=%+v scrollY=%v", want, rect, scroll.ScrollY())
		}
	}
	if scroll.ScrollY() <= 0 {
		t.Fatal("reaching the oldest history entry did not scroll")
	}
	for want := 1; want < quickConversationLimit; want++ {
		d.navigateQuickPanel(-1)
	}
	if d.quickSelectedTurnIndex() != quickConversationLimit-1 || scroll.ScrollY() != 0 {
		t.Fatal("Up did not return to the latest answer at the top")
	}
}

type quickHistoryDispatchApp struct {
	ui.App
	widgets map[string]gui.Widget
}

func (a *quickHistoryDispatchApp) FindWidget(_, id string) gui.Widget { return a.widgets[id] }
func (*quickHistoryDispatchApp) RequestUpdate()                       {}
func (*quickHistoryDispatchApp) Post(fn func())                       { fn() }
func (*quickHistoryDispatchApp) TimeoutFunc(time.Duration, func()) *ui.Timer {
	return nil
}

type quickHistoryKeyHost struct{ root, focused gui.Widget }

func (h *quickHistoryKeyHost) Widget() gui.Widget        { return h.root }
func (h *quickHistoryKeyHost) FocusedWidget() gui.Widget { return h.focused }
func (h *quickHistoryKeyHost) SetFocusedWidget(w gui.Widget) bool {
	h.focused = w
	return true
}
