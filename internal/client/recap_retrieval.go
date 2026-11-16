package client

import (
	"context"
	"strings"
	"time"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/storage"
)

const maxRecapChroniclePages = 32 // the supported 31-day range holds at most 744 UTC hours

// Recaps sample the requested period rather than the latest few captures.
// Full originals stay available to the source inspector; model packing has its
// own much smaller budget. At most eight raw 1 MiB records are retained.
func (c *Client) recapEvidence(ctx context.Context, plan assistant.Plan, start, end time.Time, segments []activity.Segment) ([]assistant.Evidence, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if !end.After(start) || end.Sub(start) > storage.MaxObservationWindow {
		return nil, false, ErrActivityLimit
	}
	if now := c.askClock(); now.Before(end) {
		end = now.Add(time.Nanosecond)
	}
	if !end.After(start) {
		return nil, false, nil
	}
	segments = recapSegmentsInWindow(segments, start, end)
	var candidates []assistant.Evidence
	if c.chronicles != nil && len(plan.AppIDs)+len(plan.Domains) == 0 {
		after := ""
		seen := map[string]bool{}
		for page := 0; ; page++ {
			if err := ctx.Err(); err != nil {
				return nil, false, err
			}
			rows, next, err := c.chronicles.ListChronicles(ctx, start.UTC(), end.UTC(), after, planRecapChronicle)
			if err != nil {
				return nil, false, err
			}
			if len(rows) > planRecapChronicle {
				return nil, false, ErrInvalidRequest
			}
			for _, row := range rows {
				// A summary crossing a requested boundary also contains work
				// outside that scope. Primary captures fill that boundary.
				if strings.TrimSpace(row.Text) == "" || row.Start.Before(start) || row.End.After(end) || !row.End.After(row.Start) {
					continue
				}
				// A recap is its own derived source. Reusing its first supporting
				// capture ID makes separate hour summaries collapse during
				// time-distributed selection and falsely labels the recap as that
				// raw capture. Bind the citation to this exact recap instead.
				id := syntheticID("chronicle", row.HourID, row.Start.UTC().Format(time.RFC3339Nano), row.End.UTC().Format(time.RFC3339Nano), row.Text)
				candidates = append(candidates, assistant.Evidence{SourceID: id, Text: row.Text, Layer: assistant.LayerHour, CapturedAt: row.Start, EndAt: row.End})
			}
			if next == "" {
				break
			}
			if len(rows) == 0 || next == after || seen[next] || page+1 >= maxRecapChroniclePages {
				return nil, false, ErrActivityLimit
			}
			seen[next], after = true, next
		}
	}

	var raw []assistant.Evidence
	sampled := false
	seenRaw := map[string]bool{}
	for bin := 0; bin < assistant.MaxEvidenceSources; bin++ {
		from := start.Add(end.Sub(start) * time.Duration(bin) / assistant.MaxEvidenceSources)
		to := start.Add(end.Sub(start) * time.Duration(bin+1) / assistant.MaxEvidenceSources)
		if !to.After(from) {
			continue
		}
		items, more, err := c.recentPlanEvidenceWithCoverage(ctx, plan, from, to, recapSegmentsInWindow(segments, from, to), 1)
		if err != nil {
			return nil, false, err
		}
		sampled = sampled || more
		for _, item := range items {
			if !seenRaw[item.SourceID] {
				raw = append(raw, item)
				seenRaw[item.SourceID] = true
			}
		}
	}
	// Sparse periods should not waste the remaining source slots. Preserve the
	// distributed picks and only fill unused slots from recent evidence.
	if len(raw) < assistant.MaxEvidenceSources {
		items, more, err := c.recentPlanEvidenceWithCoverage(ctx, plan, start, end, segments, assistant.MaxEvidenceSources)
		if err != nil {
			return nil, false, err
		}
		sampled = sampled || more
		for _, item := range items {
			if !seenRaw[item.SourceID] && len(raw) < assistant.MaxEvidenceSources {
				raw = append(raw, item)
				seenRaw[item.SourceID] = true
			}
		}
	}
	candidates = append(candidates, raw...)
	selected, limited := assistant.TimeDistributedEvidence(candidates, assistant.MaxEvidenceItems)
	return selected, sampled || limited, nil
}

func recapSegmentsInWindow(segments []activity.Segment, start, end time.Time) []activity.Segment {
	var out []activity.Segment
	for _, segment := range segments {
		if segment.WallStart.Before(end) && segment.WallEnd.After(start) {
			if segment.WallStart.Before(start) {
				segment.WallStart = start
			}
			if segment.WallEnd.After(end) {
				segment.WallEnd = end
			}
			out = append(out, segment)
		}
	}
	return out
}
