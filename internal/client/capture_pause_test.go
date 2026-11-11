package client

import (
	"sync"
	"testing"
	"time"
)

func TestPauseUntilTomorrowAcrossMidnight(t *testing.T) {
	loc := time.UTC
	cases := []struct{ now, want time.Time }{
		{time.Date(2026, 11, 11, 23, 30, 0, 0, loc), time.Date(2026, 11, 12, 9, 0, 0, 0, loc)},
		{time.Date(2026, 11, 12, 0, 5, 0, 0, loc), time.Date(2026, 11, 12, 9, 0, 0, 0, loc)},
		{time.Date(2026, 11, 12, 9, 0, 0, 0, loc), time.Date(2026, 11, 13, 9, 0, 0, 0, loc)},
		{time.Date(2026, 12, 31, 15, 0, 0, 0, loc), time.Date(2027, 1, 1, 9, 0, 0, 0, loc)},
	}
	for _, c := range cases {
		if got := pauseUntilTomorrow(c.now); !got.Equal(c.want) {
			t.Errorf("pauseUntilTomorrow(%v) = %v, want %v", c.now, got, c.want)
		}
	}
}

func TestPauseUntilTomorrowAcrossDST(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("tzdata unavailable")
	}
	// Spring forward 2026-03-08: the night is 23h long.
	now := time.Date(2026, 3, 7, 20, 0, 0, 0, loc)
	got := pauseUntilTomorrow(now)
	if h, m, _ := got.Clock(); h != 9 || m != 0 || got.Day() != 8 {
		t.Fatalf("spring forward = %v, want 09:00 on the 8th", got)
	}
	if d := got.Sub(now); d != 12*time.Hour {
		t.Fatalf("spring-forward wait = %v, want 12h wall clock (13h-1h)", d)
	}
	// Fall back 2026-11-01: the night is 25h long.
	now = time.Date(2026, 10, 31, 20, 0, 0, 0, loc)
	got = pauseUntilTomorrow(now)
	if h, _, _ := got.Clock(); h != 9 || got.Day() != 1 {
		t.Fatalf("fall back = %v, want 09:00 on the 1st", got)
	}
	if d := got.Sub(now); d != 14*time.Hour {
		t.Fatalf("fall-back wait = %v, want 14h", d)
	}
}

func TestPauseDeadline(t *testing.T) {
	now := time.Date(2026, 11, 11, 13, 0, 0, 0, time.UTC)
	if got := pauseDeadline(now, 15*time.Minute); !got.Equal(now.Add(15 * time.Minute)) {
		t.Fatalf("15m deadline = %v", got)
	}
	if got := pauseDeadline(now, -1); !got.Equal(time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("tomorrow deadline = %v", got)
	}
}

type pauseHarness struct {
	mu      sync.Mutex
	running bool
	pauses  int
	resumes int
	s       *pauseScheduler
	clock   time.Time
}

func newPauseHarness() *pauseHarness {
	h := &pauseHarness{running: true, clock: time.Date(2026, 11, 11, 13, 0, 0, 0, time.UTC)}
	h.s = &pauseScheduler{
		now:     func() time.Time { h.mu.Lock(); defer h.mu.Unlock(); return h.clock },
		post:    func(f func()) { go f() },
		running: func() bool { h.mu.Lock(); defer h.mu.Unlock(); return h.running },
		pause:   func() { h.mu.Lock(); h.pauses++; h.running = false; h.mu.Unlock() },
		resume:  func() { h.mu.Lock(); h.resumes++; h.running = true; h.mu.Unlock() },
		refresh: func() {},
	}
	return h
}

func (h *pauseHarness) counts() (int, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.pauses, h.resumes
}

func TestPauseSchedulerResumesAfterDeadline(t *testing.T) {
	h := newPauseHarness()
	h.s.post = func(f func()) { f() }
	h.s.pauseFor(30 * time.Millisecond)
	h.s.observeCapture(false)
	if h.s.pausedUntil().IsZero() {
		t.Fatal("pausedUntil not recorded")
	}
	h.mu.Lock()
	h.clock = h.clock.Add(time.Second) // deadline passes on the injected clock too
	h.mu.Unlock()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, r := h.counts(); r == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if p, r := h.counts(); p != 1 || r != 1 {
		t.Fatalf("pauses=%d resumes=%d, want 1/1", p, r)
	}
	if !h.s.pausedUntil().IsZero() {
		t.Fatal("pausedUntil not cleared after resume")
	}
}

func TestPauseSchedulerManualResumeCancelsTimer(t *testing.T) {
	h := newPauseHarness()
	h.s.post = func(f func()) { f() }
	h.s.pauseFor(50 * time.Millisecond)
	h.s.observeCapture(false)
	h.mu.Lock()
	h.running = true // user resumed manually
	h.clock = h.clock.Add(time.Hour)
	h.mu.Unlock()
	h.s.observeCapture(true)
	if !h.s.pausedUntil().IsZero() {
		t.Fatal("manual resume left pausedUntil set")
	}
	time.Sleep(120 * time.Millisecond)
	if _, r := h.counts(); r != 0 {
		t.Fatalf("timer resumed after manual resume: %d", r)
	}
}

func TestPauseSchedulerCancelAndIgnoredWhenPaused(t *testing.T) {
	h := newPauseHarness()
	h.s.post = func(f func()) { f() }
	h.s.pauseFor(40 * time.Millisecond)
	h.s.cancel()
	time.Sleep(100 * time.Millisecond)
	if _, r := h.counts(); r != 0 || !h.s.pausedUntil().IsZero() {
		t.Fatalf("cancelled timer resumed=%d until=%v", r, h.s.pausedUntil())
	}
	h.mu.Lock()
	h.running = false
	h.mu.Unlock()
	h.s.pauseFor(time.Hour) // not running: nothing to pause
	if p, _ := h.counts(); p != 1 || !h.s.pausedUntil().IsZero() {
		t.Fatalf("pause while paused: pauses=%d until=%v", p, h.s.pausedUntil())
	}
}
