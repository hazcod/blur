package client

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/golang-gui/goui/theme/modern"
	"github.com/golang-gui/goui/ui"

	"github.com/hazcod/blur/internal/chronicle"
)

// hourSoFarRefreshAfter is how stale the in-memory draft may get before the
// next render queues a refresh. The draft is extractive and costs no tokens.
const hourSoFarRefreshAfter = 2 * time.Minute

// hourSoFarState is the in-memory-only "This hour so far" draft. It is never
// persisted and holds no content in logs.
type hourSoFarState struct {
	mu         sync.Mutex
	draft      chronicle.Draft
	updatedAt  time.Time
	loading    bool
	generation uint64
}

// hourSoFarStates keeps draft state per desktop because the desktop struct is
// owned by another change. TODO(restyle): move hourSoFarState into the desktop
// struct and delete this temporary package-level map.
var hourSoFarStates sync.Map // map[*desktop]*hourSoFarState

func hourSoFarStateFor(d *desktop) *hourSoFarState {
	state, _ := hourSoFarStates.LoadOrStore(d, &hourSoFarState{})
	return state.(*hourSoFarState)
}

func (s *hourSoFarState) clear() {
	s.mu.Lock()
	s.draft, s.updatedAt = chronicle.Draft{}, time.Time{}
	s.mu.Unlock()
}

func hourSoFarStale(updatedAt, now time.Time) bool {
	return updatedAt.IsZero() || now.Sub(updatedAt) >= hourSoFarRefreshAfter
}

func hourSoFarUpdatedLabel(updatedAt, now time.Time) string {
	minutes := int(now.Sub(updatedAt) / time.Minute)
	switch {
	case minutes < 1:
		return "Updated just now"
	default:
		return fmt.Sprintf("Updated %d min ago", minutes)
	}
}

// hourSoFarView renders the model-free current-hour draft. It returns an empty
// VBox when capture is not running or there is nothing to show; the caller
// decides whether to show the section.
func (d *desktop) hourSoFarView() ui.View {
	if d == nil || d.client == nil {
		return ui.VBox()
	}
	state := hourSoFarStateFor(d)
	if d.capture == nil || !d.capture.Running() || d.client.MemoryIsChanging() {
		state.clear()
		return ui.VBox()
	}
	now := time.Now()
	state.mu.Lock()
	if state.generation != d.memoryGeneration {
		state.draft, state.updatedAt, state.generation = chronicle.Draft{}, time.Time{}, d.memoryGeneration
	}
	draft, updatedAt := state.draft, state.updatedAt
	startRefresh := !state.loading && hourSoFarStale(updatedAt, now)
	if startRefresh {
		state.loading = true
	}
	state.mu.Unlock()
	if startRefresh {
		d.refreshHourSoFar(state, now)
	}
	if len(draft.Lines) == 0 {
		return ui.VBox()
	}
	rows := make([]ui.View, 0, len(draft.Lines)+2)
	rows = append(rows, ui.Label("This hour so far").Style(styleSectionTitle))
	for _, line := range draft.Lines {
		rows = append(rows, ui.Label(line).Style(modern.MutedText))
	}
	rows = append(rows, ui.Label(hourSoFarUpdatedLabel(updatedAt, now)).Style(modern.MutedText))
	return ui.VBox(rows...).Spacing(4)
}

// hourSoFarHasLines reports whether the current-hour draft has anything to
// show, so the Today page adds the section (and its divider) only then.
func hourSoFarHasLines(d *desktop) bool {
	state := hourSoFarStateFor(d)
	state.mu.Lock()
	defer state.mu.Unlock()
	return len(state.draft.Lines) > 0
}

// refreshHourSoFar loads the draft off the UI thread, following loadToday's
// pattern: results are installed only if memory is unchanged since the start.
func (d *desktop) refreshHourSoFar(state *hourSoFarState, now time.Time) {
	memoryGeneration := d.memoryGeneration
	epoch, stable := d.client.BeginMemoryRead()
	if !stable {
		state.mu.Lock()
		state.loading = false
		state.mu.Unlock()
		return
	}
	go func() {
		draft, err := d.client.HourSoFar(context.Background(), now)
		d.app.Post(func() {
			installed := currentMemoryRead(memoryGeneration, d.memoryGeneration) && d.client.PublishMemoryRead(epoch, func() {
				state.mu.Lock()
				defer state.mu.Unlock()
				state.loading = false
				if err != nil {
					state.draft, state.updatedAt = chronicle.Draft{}, time.Time{}
					return
				}
				state.draft, state.updatedAt = draft, now
			})
			if !installed {
				state.mu.Lock()
				state.loading = false
				state.mu.Unlock()
				return
			}
			d.app.RequestUpdate()
		})
	}()
}
