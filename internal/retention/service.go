// Package retention applies the user's encrypted age-based memory policy.
package retention

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/hazcod/blur/internal/deletion"
	"github.com/hazcod/blur/internal/storage/durable"
)

var ErrInvalid = errors.New("retention: invalid policy, clock, or service")

const (
	SweepInterval      = 24 * time.Hour
	SweepRetryInterval = 15 * time.Minute
)

type Service struct {
	mu          sync.Mutex
	store       *durable.Store
	deleter     *deletion.Service
	beforeSweep func()
	afterSweep  func()
}

func NewService(store *durable.Store, deleter *deletion.Service) (*Service, error) {
	if store == nil || deleter == nil || !store.EncryptedAtRest() {
		return nil, ErrInvalid
	}
	return &Service{store: store, deleter: deleter}, nil
}

// SetBeforeSweep installs an optional observer invoked immediately before any
// sweep that may delete memory. The callback runs while the service is locked
// and must not call back into this service.
func (s *Service) SetBeforeSweep(callback func()) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.beforeSweep = callback
	s.mu.Unlock()
}

// SetAfterSweep installs an optional observer called when a sweep completes,
// including partial failures. Like SetBeforeSweep, it runs while locked.
func (s *Service) SetAfterSweep(callback func()) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.afterSweep = callback
	s.mu.Unlock()
}

func (s *Service) Policy(ctx context.Context) (durable.RetentionPolicy, error) {
	if s == nil || s.store == nil || ctx == nil {
		return durable.RetentionPolicy{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.store.GetRetentionPolicy(ctx)
}

func (s *Service) ConfigureDays(ctx context.Context, days int) error {
	if s == nil || s.store == nil || ctx == nil || !durable.ValidRetentionDays(days) {
		return ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.store.SetRetentionDays(ctx, days)
}

// RunIfDue makes one bounded-page sweep at most once per 24 hours. It marks
// completion only after every expired source and linked projection is deleted;
// failed partial sweeps are safe to retry.
func (s *Service) RunIfDue(ctx context.Context, now time.Time) (removed int, ran bool, err error) {
	if s == nil || s.store == nil || s.deleter == nil || ctx == nil || now.IsZero() || !durable.ValidCapturedAt(now) {
		return 0, false, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return 0, false, err
	}
	now = now.UTC()
	policy, err := s.store.GetRetentionPolicy(ctx)
	if err != nil {
		return 0, false, err
	}
	if policy.Days == 0 || !policy.LastSweepAt.IsZero() && now.Before(policy.LastSweepAt.Add(SweepInterval)) {
		return 0, false, nil
	}
	removed, err = s.sweep(ctx, policy, now)
	return removed, err == nil, err
}

// RunNow applies an enabled policy immediately after the user changes it.
func (s *Service) RunNow(ctx context.Context, now time.Time) (int, error) {
	if s == nil || s.store == nil || s.deleter == nil || ctx == nil || now.IsZero() || !durable.ValidCapturedAt(now) {
		return 0, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	policy, err := s.store.GetRetentionPolicy(ctx)
	if err != nil || policy.Days == 0 {
		return 0, err
	}
	return s.sweep(ctx, policy, now.UTC())
}

// RunPeriodic performs a cheap due check every interval. It is intended for a
// single process-owned worker and performs no model or network work.
func (s *Service) RunPeriodic(ctx context.Context, interval time.Duration) {
	if s == nil || ctx == nil || interval <= 0 {
		return
	}
	wait := time.Duration(0)
	for {
		if wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				return
			case <-timer.C:
			}
		}
		if ctx.Err() != nil {
			return
		}
		if _, _, err := s.RunIfDue(ctx, time.Now()); err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Error("retention sweep failed", "result", "failed")
			wait = SweepRetryInterval
		} else {
			wait = interval
		}
	}
}

func (s *Service) sweep(ctx context.Context, policy durable.RetentionPolicy, now time.Time) (int, error) {
	cutoff := now.Add(-time.Duration(policy.Days) * 24 * time.Hour)
	if s.beforeSweep != nil {
		s.beforeSweep()
	}
	if s.afterSweep != nil {
		defer s.afterSweep()
	}
	removed, err := s.deleter.DeleteBefore(ctx, cutoff)
	if err != nil {
		return removed, err
	}
	if err := s.store.MarkRetentionSweep(ctx, now); err != nil {
		return removed, err
	}
	return removed, nil
}
