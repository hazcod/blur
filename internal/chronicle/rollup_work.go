package chronicle

import (
	"context"
	"time"
)

// A separate visit bound prevents blocked/current/no-op rows from monopolizing
// a pass. The durable cursor advances for every visit, independently of the six
// model calls allowed per pass.
const maxRollupWorkVisits = 64

func (w *Worker) processQueuedRollups(ctx context.Context, repo RollupRepository, work RollupWorkRepository, gen RollupGenerator, loc *time.Location, zone string, now time.Time) error {
	generated := 0
	draining := false
	passErr := func() error {
		status, err := work.ChronicleRollupWorkStatus(ctx)
		if err != nil {
			return err
		}
		if status.ErasurePending {
			return nil
		}
		levels := []string{RollupLevelDay, RollupLevelWeek, RollupLevelMonth, RollupLevelYear}
		for i, level := range levels {
			// A parent cannot know which legacy children are missing until the
			// historical scan is complete. Do not publish a partial week as full.
			if level == RollupLevelWeek && status.InputsPending {
				continue
			}
			if (level == RollupLevelMonth || level == RollupLevelYear) && status.BackfillPending {
				continue
			}
			// Reserve one opportunity per remaining level under continuous
			// child churn, without increasing the six-call total.
			budget := maxRollupsGeneratedPass - (len(levels) - i - 1)
			seen := map[string]bool{}
			for visits := 0; visits < maxRollupWorkVisits && generated < budget; visits++ {
				if err := ctx.Err(); err != nil {
					return err
				}
				item, found, err := work.NextChronicleRollupWork(ctx, level, zone)
				if err != nil {
					return err
				}
				if !found || seen[item.ID] {
					break
				}
				seen[item.ID] = true
				if item.CleanupPending {
					draining = true
					continue
				}
				row := item.Rollup
				var did bool
				if level == RollupLevelDay {
					if !dayGeneratable(row, now) {
						continue
					}
					did, err = w.refreshDayRollup(ctx, repo, gen, row, loc, zone)
				} else if level == RollupLevelWeek {
					did, err = w.refreshWeekRollup(ctx, repo, gen, row, loc, zone, now)
				} else {
					did, err = w.refreshCalendarRollup(ctx, repo, gen, row, loc, zone, now)
				}
				if err != nil {
					return err
				}
				if did {
					generated++
				}
			}
		}
		return nil
	}()
	status, err := work.ChronicleRollupWorkStatus(ctx)
	if err == nil {
		// These counts include historical queued work and periods waiting for
		// their end/children, rather than silently excluding old pending work.
		pending := status.Dirty
		if status.BackfillPending {
			pending++
		}
		w.setRollupStatus("", pending, status.Dirty)
		// Continue finite backfill/model batches promptly; waiting current
		// periods alone retain the ordinary fifteen-minute cadence.
		w.rollupContinue = passErr == nil && (status.BackfillPending || draining || generated >= maxRollupsGeneratedPass-3)
	} else if passErr == nil {
		passErr = err
	}
	return passErr
}
