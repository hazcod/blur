package client

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/appidentity"
	"github.com/hazcod/blur/internal/assistant"
)

const maxActivityScopeMetadata = 65536

type activityScopeRead struct {
	memoryReadPublication
	sessions int
}

// Each callback owns at most one bounded window of clipped metadata. Distinct
// original IDs count sessions once, even when an interval crosses a child edge.
func (c *Client) walkActivityScope(ctx context.Context, start, end time.Time, plan assistant.Plan, consume func([]activity.Segment) error) (activityScopeRead, error) {
	if c == nil || c.data == nil || ctx == nil || consume == nil {
		return activityScopeRead{}, ErrInvalidRequest
	}
	if err := ctx.Err(); err != nil {
		return activityScopeRead{}, err
	}
	if start.IsZero() || !end.After(start) {
		return activityScopeRead{}, ErrActivityLimit
	}
	// Count touched local dates rather than elapsed 24-hour units, including
	// historical offset changes and an exclusive midnight endpoint.
	last := end.Add(-time.Nanosecond).In(start.Location())
	firstDate := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
	lastDate := time.Date(last.Year(), last.Month(), last.Day(), 0, 0, 0, 0, time.UTC)
	if lastDate.Sub(firstDate) > time.Duration(MaxActivityReportDays-1)*24*time.Hour {
		return activityScopeRead{}, ErrActivityLimit
	}
	reader, ok := c.planActivity()
	if !ok {
		return activityScopeRead{}, ErrNotConfigured
	}
	publication, stable := c.beginMemoryPublication()
	if !stable {
		return activityScopeRead{}, ErrMemoryChanging
	}
	read := activityScopeRead{memoryReadPublication: publication}
	if read.fence == nil && end.Sub(start) > searchWindowSpan {
		return activityScopeRead{}, ErrMemoryChanging
	}
	seen := map[string]bool{}
	for to, windows := end, 0; to.After(start); windows++ {
		if windows >= 15 {
			return activityScopeRead{}, ErrActivityLimit
		}
		if err := ctx.Err(); err != nil {
			return activityScopeRead{}, err
		}
		from := to.Add(-searchWindowSpan)
		if from.Before(start) {
			from = start
		}
		var dates []string
		for day := from.UTC().Truncate(24 * time.Hour).Add(-24 * time.Hour); day.Before(to); day = day.Add(24 * time.Hour) {
			dates = append(dates, day.Format("2006-01-02"))
		}
		rows, err := reader.ListActivitySegmentsForDates(ctx, dates, MaxActivityReportSegments)
		if err != nil {
			return activityScopeRead{}, err
		}
		if len(rows) > MaxActivityReportSegments {
			return activityScopeRead{}, ErrActivityLimit
		}
		for _, row := range rows {
			if row.Validate() != nil {
				return activityScopeRead{}, ErrInvalidRequest
			}
		}
		rows = filterSegments(rows, plan)
		clipped := rows[:0]
		for _, row := range rows {
			if row.AppID == "" || strings.EqualFold(row.AppID, appidentity.BundleID) || !row.WallEnd.After(from) || !row.WallStart.Before(to) {
				continue
			}
			if row.WallStart.Before(from) {
				row.WallStart = from
			}
			if row.WallEnd.After(to) {
				row.WallEnd = to
			}
			row.Duration = int64(row.WallEnd.Sub(row.WallStart))
			if !seen[row.ID] {
				if len(seen) >= maxActivityScopeMetadata {
					return activityScopeRead{}, ErrActivityLimit
				}
				seen[row.ID] = true
			}
			clipped = append(clipped, row)
		}
		if err := consume(clipped); err != nil {
			return activityScopeRead{}, err
		}
		to = from
	}
	if err := ctx.Err(); err != nil {
		return activityScopeRead{}, err
	}
	read.sessions = len(seen)
	if !read.publish(c, func() {}) {
		return activityScopeRead{}, ErrMemoryChanging
	}
	return read, nil
}

type activityReportAccumulator struct {
	eligible, unclassified int64
	dimensions             [5]map[string]int64
	entries                int
}

func (a *activityReportAccumulator) add(rows []activity.Segment, zone *time.Location) error {
	report, err := activity.Report(rows, zone)
	if err != nil {
		return err
	}
	a.eligible += report.Eligible
	a.unclassified += report.Unclassified
	add := func(dimension int, key string, duration int64) error {
		if a.dimensions[dimension] == nil {
			a.dimensions[dimension] = map[string]int64{}
		}
		if _, found := a.dimensions[dimension][key]; !found {
			if a.entries >= maxActivityScopeMetadata {
				return ErrActivityLimit
			}
			a.entries++
		}
		a.dimensions[dimension][key] += duration
		return nil
	}
	for _, day := range report.Days {
		if err := add(0, day.Date, day.Duration); err != nil {
			return err
		}
	}
	for i, buckets := range [][]activity.Bucket{report.Apps, report.Domains, report.Projects, report.Topics} {
		for _, bucket := range buckets {
			if err := add(i+1, bucket.Key, bucket.Duration); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *activityReportAccumulator) result() activity.ReportResult {
	result := activity.ReportResult{Eligible: a.eligible, Unclassified: a.unclassified}
	keys := func(values map[string]int64) []string {
		var out []string
		for key := range values {
			out = append(out, key)
		}
		slices.Sort(out)
		return out
	}
	for _, key := range keys(a.dimensions[0]) {
		result.Days = append(result.Days, activity.Day{Date: key, Duration: a.dimensions[0][key]})
	}
	for i, out := range []*[]activity.Bucket{&result.Apps, &result.Domains, &result.Projects, &result.Topics} {
		for _, key := range keys(a.dimensions[i+1]) {
			*out = append(*out, activity.Bucket{Key: key, Duration: a.dimensions[i+1][key]})
		}
	}
	return result
}

func (c *Client) activityScopeReport(ctx context.Context, start, end time.Time, zone *time.Location, plan assistant.Plan) (activity.ReportResult, activityScopeRead, error) {
	var accumulator activityReportAccumulator
	read, err := c.walkActivityScope(ctx, start, end, plan, func(rows []activity.Segment) error { return accumulator.add(rows, zone) })
	if err != nil {
		return activity.ReportResult{}, activityScopeRead{}, err
	}
	return accumulator.result(), read, nil
}

func (c *Client) readActivityReport(ctx context.Context, startDate, endDate, zoneName string) (activity.ReportResult, activityScopeRead, error) {
	if c == nil || c.data == nil {
		return activity.ReportResult{}, activityScopeRead{}, ErrNotConfigured
	}
	start, err := time.Parse("2006-01-02", startDate)
	if err != nil || start.Format("2006-01-02") != startDate {
		return activity.ReportResult{}, activityScopeRead{}, ErrInvalidRequest
	}
	last, err := time.Parse("2006-01-02", endDate)
	if err != nil || last.Format("2006-01-02") != endDate || last.Before(start) || last.Sub(start) > time.Duration(MaxActivityReportDays-1)*24*time.Hour {
		return activity.ReportResult{}, activityScopeRead{}, ErrInvalidRequest
	}
	zone, err := loadZone(zoneName)
	if err != nil {
		return activity.ReportResult{}, activityScopeRead{}, ErrInvalidRequest
	}
	op, done, err := c.beginOperation(ctx, noteOperationTimeout)
	if err != nil {
		return activity.ReportResult{}, activityScopeRead{}, err
	}
	defer done()
	from := activity.StartOfDate(start.Year(), start.Month(), start.Day(), zone)
	to := activity.StartOfDate(last.Year(), last.Month(), last.Day()+1, zone)
	return c.activityScopeReport(op, from, to, zone, assistant.Plan{})
}
