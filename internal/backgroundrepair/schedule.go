// Package backgroundrepair keeps periodic repair independent from incremental
// notifications and continuation retries. It performs no work or clock reads.
package backgroundrepair

import "time"

// Schedule tracks an absolute repair deadline. Only an admitted repair advances
// it; a notification, transient refusal, or continuation retry cannot do so.
type Schedule struct {
	next  time.Time
	every time.Duration
}

func New(now time.Time, every time.Duration) Schedule {
	return Schedule{next: now.Add(every), every: every}
}

func (s Schedule) Due(now time.Time) bool { return !now.Before(s.next) }

func (s *Schedule) Admitted(now time.Time) {
	if s.Due(now) {
		s.next = now.Add(s.every)
	}
}

// Delay waits no longer than a future repair deadline. A refused overdue repair
// retains the ordinary retry delay, avoiding a busy loop while policy is paused.
func (s Schedule) Delay(now time.Time, retry time.Duration) time.Duration {
	if remaining := s.next.Sub(now); remaining > 0 && remaining < retry {
		return remaining
	}
	return retry
}
