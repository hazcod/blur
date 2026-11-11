package client

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/hazcod/blur/internal/chronicle"
)

const maxChronicleRecoveryHours = 24

type closedHourReader interface {
	CaptureHourEvidence(context.Context, time.Time) (chronicle.Hour, error)
}
type closedHourScheduler interface {
	BackgroundSummariesEnabled() bool
	ScheduleClosedHour(context.Context, chronicle.Hour) error
}

// recoverClosedChronicles checks only the previous day of encrypted UTC-hour
// indexes, and only after explicit local-background consent. It never scans
// application history and does no work while consent is off.
func recoverClosedChronicles(ctx context.Context, now time.Time, reader closedHourReader, scheduler closedHourScheduler) error {
	if ctx == nil || reader == nil || scheduler == nil || !scheduler.BackgroundSummariesEnabled() {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	current := now.UTC().Truncate(time.Hour)
	for i := maxChronicleRecoveryHours; i > 0; i-- {
		if err := ctx.Err(); err != nil {
			return err
		}
		start := current.Add(-time.Duration(i) * time.Hour)
		hour, err := reader.CaptureHourEvidence(ctx, start)
		if err != nil {
			slog.Warn("chronicle recovery skipped", "result", "evidence_unavailable")
			continue
		}
		if len(hour.Evidence) == 0 {
			continue
		}
		if err := scheduler.ScheduleClosedHour(ctx, hour); errors.Is(err, chronicle.ErrWorkerBusy) {
			break
		} else if err != nil && !errors.Is(err, chronicle.ErrWorkerDuplicate) {
			slog.Debug("chronicle recovery hour skipped", "result", "not_scheduled")
		}
	}
	return nil
}
