package client

import (
	"context"
	"errors"
	"time"

	"github.com/hazcod/blur/internal/activity"
)

const searchActivityPageRows = 64

var errSearchWalkStopped = errors.New("search scope walk stopped")

type searchActivityPager interface {
	ListActivitySegmentsForDate(context.Context, string, string, int) ([]activity.Segment, string, error)
}

// Every callback owns one small metadata page. There is no aggregate source-ID
// or page-count ceiling for an accepted query scope.
func (c *Client) walkSearchActivity(ctx context.Context, window searchScopeWindow, supplied []activity.Segment, consume func([]activity.Segment) error) error {
	emit := func(rows []activity.Segment) error {
		for len(rows) > 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
			n := min(len(rows), searchActivityPageRows)
			if err := consume(rows[:n]); err != nil {
				return err
			}
			rows = rows[n:]
		}
		return nil
	}
	if supplied != nil {
		return emit(supplied)
	}
	if pager, ok := c.data.(searchActivityPager); ok {
		for day := window.start.UTC().Truncate(24 * time.Hour).Add(-24 * time.Hour); day.Before(window.end); day = day.Add(24 * time.Hour) {
			date, after := day.Format("2006-01-02"), ""
			for {
				if err := ctx.Err(); err != nil {
					return err
				}
				rows, next, err := pager.ListActivitySegmentsForDate(ctx, date, after, searchActivityPageRows)
				if err != nil {
					return err
				}
				if len(rows) > searchActivityPageRows || next != "" && (next == after || len(rows) == 0) {
					return ErrInvalidRequest
				}
				if err := emit(rows); err != nil {
					return err
				}
				if next == "" {
					break
				}
				after = next
			}
		}
		return ctx.Err()
	}
	if reader, ok := c.planActivity(); ok {
		var dates []string
		for day := window.start.UTC().Truncate(24 * time.Hour).Add(-24 * time.Hour); day.Before(window.end); day = day.Add(24 * time.Hour) {
			dates = append(dates, day.Format("2006-01-02"))
		}
		rows, err := reader.ListActivitySegmentsForDates(ctx, dates, MaxActivityReportSegments)
		if err != nil {
			return err
		}
		if len(rows) > MaxActivityReportSegments {
			return ErrActivityLimit
		}
		return emit(rows)
	}
	return nil
}
