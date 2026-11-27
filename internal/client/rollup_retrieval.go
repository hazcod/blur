package client

import (
	"context"
	"strings"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/chronicle"
)

// maxRollupRecapItems leaves room for partial-day hour recaps and raw captures
// when a long window is answered from day and week rollups.
const maxRollupRecapItems = 8

type recapSpan struct{ start, end time.Time }

func (s recapSpan) contains(start, end time.Time) bool {
	return !start.Before(s.start) && !end.After(s.end)
}

// wholeLocalDays returns every local calendar day that lies completely inside
// [start, end), keyed by its YYYY-MM-DD date, and the ISO weeks that do too.
func wholeLocalDays(start, end time.Time, zone *time.Location) (days, weeks map[string]recapSpan) {
	days, weeks = map[string]recapSpan{}, map[string]recapSpan{}
	local := start.In(zone)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, zone)
	if day.Before(start) {
		day = time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, zone)
	}
	for range 402 {
		next := time.Date(day.Year(), day.Month(), day.Day()+1, 0, 0, 0, 0, zone)
		if next.After(end) {
			break
		}
		days[day.Format("2006-01-02")] = recapSpan{day.UTC(), next.UTC()}
		if day.Weekday() == time.Monday {
			weekEnd := time.Date(day.Year(), day.Month(), day.Day()+7, 0, 0, 0, 0, zone)
			if !weekEnd.After(end) {
				if key, ok := chronicle.WeekKeyForDate(day.Format("2006-01-02")); ok {
					weeks[key] = recapSpan{day.UTC(), weekEnd.UTC()}
				}
			}
		}
		day = next
	}
	return days, weeks
}

// recapRollups reads stored day and week recaps for the whole periods inside
// the window. The spans they cover are returned so hour recaps inside them are
// not repeated. A window with no whole local day, or any app/domain filter,
// yields nothing and the caller falls back to hour recaps and raw captures.
func (c *Client) recapRollups(ctx context.Context, plan assistant.Plan, start, end time.Time) (items []assistant.Evidence, covered []recapSpan, dirty int, err error) {
	return c.recapRollupsForLevels(ctx, plan, start, end, []string{chronicle.RollupLevelYear, chronicle.RollupLevelMonth, chronicle.RollupLevelWeek, chronicle.RollupLevelDay})
}

func (c *Client) recapRollupsForLevels(ctx context.Context, plan assistant.Plan, start, end time.Time, levels []string) (items []assistant.Evidence, covered []recapSpan, dirty int, err error) {
	if c.chronicles == nil || len(plan.AppIDs)+len(plan.Domains) > 0 {
		return nil, nil, 0, nil
	}
	zone, zoneErr := loadZone(localZoneName())
	if zoneErr != nil {
		zone = time.UTC
	}
	days, weeks := wholeLocalDays(start, end, zone)
	if len(days) == 0 {
		return nil, nil, 0, nil
	}
	months, years := map[string]recapSpan{}, map[string]recapSpan{}
	for _, span := range days {
		day := span.start.In(zone)
		if day.Day() != 1 {
			continue
		}
		if to := day.AddDate(0, 1, 0); !to.After(end) {
			months[day.Format("2006-01")] = recapSpan{day.UTC(), to.UTC()}
		}
		if day.Month() == time.January {
			if to := day.AddDate(1, 0, 0); !to.After(end) {
				years[day.Format("2006")] = recapSpan{day.UTC(), to.UTC()}
			}
		}
	}
	for _, level := range levels {
		periods := map[string]map[string]recapSpan{chronicle.RollupLevelDay: days, chronicle.RollupLevelWeek: weeks, chronicle.RollupLevelMonth: months, chronicle.RollupLevelYear: years}[level]
		if len(periods) == 0 {
			continue
		}
		rows, next, listErr := c.chronicles.ListChronicleRollups(ctx, level, start.UTC(), end.UTC(), "", 64)
		if listErr != nil {
			return nil, nil, 0, listErr
		}
		if len(rows) > 64 || next != "" {
			return nil, nil, 0, ErrActivityLimit
		}
		for _, row := range rows {
			span, ok := periods[row.PeriodKey]
			if !ok || row.Level != level || !row.Start.Equal(span.start) || !row.End.Equal(span.end) {
				// Built for another zone or a partial period: not this window's.
				continue
			}
			if row.Dirty {
				dirty++
				// Stale parents cannot cover current child recaps or establish
				// current saved state while regeneration is pending.
				continue
			}
			if strings.TrimSpace(row.Text) == "" || len(row.SourceIDs) == 0 {
				continue
			}
			layer := assistant.LayerDay
			if level == chronicle.RollupLevelWeek {
				layer = assistant.LayerWeek
			} else if level == chronicle.RollupLevelMonth {
				layer = assistant.LayerMonth
			} else if level == chronicle.RollupLevelYear {
				layer = assistant.LayerYear
			}
			// A rollup is its own derived source, bound to this exact text.
			id := syntheticID("chronicle-rollup", row.ID, row.Start.UTC().Format(time.RFC3339Nano), row.End.UTC().Format(time.RFC3339Nano), row.Text)
			items = append(items, assistant.Evidence{SourceID: id, Text: row.Text, Layer: layer, CapturedAt: row.Start.In(zone), EndAt: row.End.In(zone), SourceIDs: append([]string(nil), row.SourceIDs...), CoverageLimited: row.Sampled || row.CoverageVersion == 0})
			covered = append(covered, span)
		}
	}
	return items, covered, dirty, nil
}

func selectRecapSummaries(items []assistant.Evidence) (selected []assistant.Evidence, limited bool) {
	for _, layer := range []assistant.Layer{assistant.LayerYear, assistant.LayerMonth, assistant.LayerWeek, assistant.LayerDay, assistant.LayerHour} {
		var group []assistant.Evidence
		for _, item := range items {
			if item.Layer == layer {
				group = append(group, item)
			}
		}
		chosen, omitted := assistant.TimeDistributedEvidence(group, maxRollupRecapItems-len(selected))
		selected = append(selected, chosen...)
		limited = limited || omitted
	}
	return selected, limited
}

func coveredByRollup(covered []recapSpan, start, end time.Time) bool {
	for _, span := range covered {
		if span.contains(start, end) {
			return true
		}
	}
	return false
}
