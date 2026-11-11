package client

import (
	"time"

	"github.com/hazcod/blur/internal/chronicle"
	"github.com/hazcod/blur/internal/today"
)

// uiSnapshotQuickSettle is the settle time for quick-panel states; they
// resize and repaint but load nothing.
const uiSnapshotQuickSettle = 800 * time.Millisecond

const (
	uiSnapshotQuickQuestion = "What did I read about SQLite this morning?"
	uiSnapshotQuickAnswer   = "This morning you read an article about SQLite write-ahead logging. " +
		"It explains how WAL lets readers keep working while a single writer appends to the log. " +
		"It also covers checkpointing, which folds the log back into the main database file."
	uiSnapshotQuickNote = "Saved. Add a local model to process ordinary prose."
)

// uiSnapshotQuickState is one synthetic quick-panel state. Every string is
// invented for review; nothing is read from the user's store.
type uiSnapshotQuickState struct {
	name  string
	apply func(d *desktop)
}

func uiSnapshotQuickStates() []uiSnapshotQuickState {
	return []uiSnapshotQuickState{
		{"empty", func(d *desktop) {}},
		{"typing", func(d *desktop) { d.quickInput = uiSnapshotQuickQuestion }},
		{"pending", func(d *desktop) {
			d.askQuestion = uiSnapshotQuickQuestion
			d.quickAskPending, d.askLoading = true, true
		}},
		{"answer", func(d *desktop) {
			d.quickConversation = []quickConversationTurn{{
				question: uiSnapshotQuickQuestion,
				answer:   uiSnapshotQuickAnswer,
				sources:  3,
				general:  false,
			}}
		}},
		// The state right after a note is saved: input cleared, confirmation on.
		{"note-saved", func(d *desktop) { d.status = uiSnapshotQuickNote; d.quickUI.savedFlash = true }},
		// The hover recap: preview on and not pinned, the only combination in
		// which quickPanelShowsDayPreview renders the day.
		{"preview", func(d *desktop) {
			d.quickPreview, d.quickPanelPinned = true, false
			d.today = TodayView{State: "ready", Snapshot: uiSnapshotQuickToday()}
			d.hourlySummaries = uiSnapshotQuickSummaries()
		}},
	}
}

func uiSnapshotQuickToday() today.Snapshot {
	return today.Snapshot{
		ConfirmedTodos: []today.Todo{
			{ID: "snapshot-todo-1", Text: "Send the SQLite benchmark notes to the team", Confirmed: true},
			{ID: "snapshot-todo-2", Text: "Review the migration checklist", Confirmed: true},
		},
		OpenLoops: []today.OpenLoop{{Text: "Decide whether to enable WAL on the sync database"}},
	}
}

func uiSnapshotQuickSummaries() []chronicle.Summary {
	start := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	return []chronicle.Summary{{
		HourID: "snapshot-hour",
		Start:  start,
		End:    start.Add(time.Hour),
		Text:   "You read an article about SQLite write-ahead logging and compared it with your current journal mode.",
	}}
}

// resetQuickSnapshotState clears every field the quick panel renders from so
// each state starts from the empty panel.
func (d *desktop) resetQuickSnapshotState() {
	d.quickInput, d.status = "", ""
	d.quickConversation = nil
	d.quickAskPending, d.askLoading, d.askQuestion = false, false, ""
	d.quickNotePending = false
	d.quickUI.savedFlash, d.quickUI.expandedTurns = false, nil
	d.quickPreview, d.quickPanelPinned = false, true
	d.today, d.todayLoading, d.hourlySummaries = TodayView{}, false, nil
}

// uiSnapshotQuickSteps returns one snapshot step per quick-panel state. Each
// step sets the desktop's fields directly, then resizes the panel through the
// same path the panel itself uses before the settle delay elapses.
func (d *desktop) uiSnapshotQuickSteps() []func() (string, string) {
	var steps []func() (string, string)
	for _, state := range uiSnapshotQuickStates() {
		steps = append(steps, func() (string, string) {
			d.resetQuickSnapshotState()
			state.apply(d)
			d.resizeQuickPanel(d.quickPanelExpanded())
			d.app.RequestUpdate()
			return "quick-panel-" + state.name, "quick"
		})
	}
	return steps
}
