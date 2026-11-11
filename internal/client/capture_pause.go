package client

import (
	"log/slog"
	"sync"
	"time"
)

// pauseResumeHour is the local wall-clock hour "Until tomorrow" resumes at.
const pauseResumeHour = 9

// pauseUntilTomorrow returns the next local 09:00 strictly after now. Before
// 09:00 that is later the same day; otherwise it is 09:00 on the next calendar
// day. Calendar arithmetic (not +24h) keeps it correct across DST changes.
func pauseUntilTomorrow(now time.Time) time.Time {
	y, m, d := now.Date()
	target := time.Date(y, m, d, pauseResumeHour, 0, 0, 0, now.Location())
	if !target.After(now) {
		target = time.Date(y, m, d+1, pauseResumeHour, 0, 0, 0, now.Location())
	}
	return target
}

// pauseDeadline converts a menu duration (darwin.PauseUntilTomorrow is any
// negative value) into an absolute resume time.
func pauseDeadline(now time.Time, d time.Duration) time.Time {
	if d < 0 {
		return pauseUntilTomorrow(now)
	}
	return now.Add(d)
}

// pauseScheduler owns the single timed-pause timer. All state is touched on the
// UI goroutine except the timer callback, which only posts to it.
type pauseScheduler struct {
	now     func() time.Time
	post    func(func()) // run on the UI goroutine
	running func() bool  // capture currently running
	pause   func()       // existing pause path (UI goroutine)
	resume  func()       // existing resume path (UI goroutine)
	refresh func()       // refresh menu state (UI goroutine)

	mu        sync.Mutex
	timer     *time.Timer
	until     time.Time
	gen       uint64
	sawPaused bool
}

// pauseFor pauses capture now and schedules a resume after d.
func (s *pauseScheduler) pauseFor(d time.Duration) {
	if s == nil {
		return
	}
	s.post(func() {
		if !s.running() {
			return
		}
		until := pauseDeadline(s.now(), d)
		s.arm(until)
		slog.Info("timed capture pause requested", "resume_at", until.Format(time.RFC3339))
		s.pause()
		s.refresh()
	})
}

func (s *pauseScheduler) arm(until time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked()
	s.gen++
	gen := s.gen
	s.until, s.sawPaused = until, false
	s.timer = time.AfterFunc(until.Sub(s.now()), func() { s.post(func() { s.fire(gen) }) })
}

func (s *pauseScheduler) stopLocked() {
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	s.until, s.sawPaused = time.Time{}, false
	s.gen++
}

// cancel drops any pending timed pause (manual resume, quit).
func (s *pauseScheduler) cancel() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.stopLocked()
	s.mu.Unlock()
}

func (s *pauseScheduler) fire(gen uint64) {
	s.mu.Lock()
	if gen != s.gen || s.until.IsZero() {
		s.mu.Unlock()
		return
	}
	if remaining := s.until.Sub(s.now()); remaining > 0 { // woke early (clock change)
		s.timer = time.AfterFunc(remaining, func() { s.post(func() { s.fire(gen) }) })
		s.mu.Unlock()
		return
	}
	s.stopLocked()
	s.mu.Unlock()
	if !s.running() {
		slog.Info("timed capture pause ended; resuming")
		s.resume()
	}
	s.refresh()
}

// pausedUntil reports the active timed-pause deadline (zero when none).
func (s *pauseScheduler) pausedUntil() time.Time {
	if s == nil {
		return time.Time{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.until
}

// observeCapture is fed every capture state refresh. A running->paused edge
// confirms the timed pause took effect; a later paused->running edge is a
// manual resume and cancels the timer.
func (s *pauseScheduler) observeCapture(running bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.until.IsZero() {
		return
	}
	switch {
	case !running:
		s.sawPaused = true
	case s.sawPaused:
		s.stopLocked()
	}
}

// ensurePauseScheduler installs the timed-pause scheduler for d. It must be
// called on the UI goroutine before the menu bar item is configured so the
// "Pause for" submenu is offered.
func (d *desktop) ensurePauseScheduler() *pauseScheduler {
	if d == nil || d.app == nil || d.capture == nil {
		return nil
	}
	if d.pause != nil {
		return d.pause
	}
	d.pause = &pauseScheduler{
		now:     time.Now,
		post:    d.app.Post,
		running: func() bool { return d.capture != nil && d.capture.Running() },
		pause: func() {
			if !d.loading {
				d.toggleCapture()
			}
		},
		resume:  d.toggleCapture,
		refresh: d.updateMenuBarCaptureState,
	}
	return d.pause
}

// cancelPauseTimer stops any timed pause; call when the application quits.
func (d *desktop) cancelPauseTimer() {
	if d != nil {
		d.pause.cancel()
	}
}
