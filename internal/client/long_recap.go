package client

import (
	"context"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/chronicle"
	"github.com/hazcod/blur/internal/storage"
)

const maxLongRecapWindow = 400 * 24 * time.Hour

// longRecapEvidence has a global bound of eight <=31-day windows, one raw
// source per window, and the ordinary final evidence limit. It samples gaps
// explicitly until the persisted calendar hierarchy can cover them.
func (c *Client) longRecapEvidence(ctx context.Context, plan assistant.Plan, start, end time.Time, diagnosis *assistant.Diagnosis) ([]assistant.Evidence, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if !end.After(start) || end.Sub(start) > maxLongRecapWindow {
		return nil, false, ErrActivityLimit
	}
	if now := c.askClock(); now.Before(end) {
		end = now.Add(time.Nanosecond)
	}
	if !end.After(start) {
		return nil, false, nil
	}
	var raw, summaries []assistant.Evidence
	calendar, _, dirty, err := c.recapRollupsForLevels(ctx, plan, start, end, []string{chronicle.RollupLevelYear, chronicle.RollupLevelMonth})
	if err != nil {
		return nil, false, err
	}
	summaries = append(summaries, calendar...)
	if diagnosis != nil {
		diagnosis.RollupsDirty += dirty
	}
	for bin := 0; bin < assistant.MaxEvidenceSources; bin++ {
		from := start.Add(end.Sub(start) * time.Duration(bin) / assistant.MaxEvidenceSources)
		to := start.Add(end.Sub(start) * time.Duration(bin+1) / assistant.MaxEvidenceSources)
		if to.Sub(from) > storage.MaxObservationWindow {
			switch bin {
			case 0:
				to = from.Add(storage.MaxObservationWindow)
			case assistant.MaxEvidenceSources - 1:
				from = to.Add(-storage.MaxObservationWindow)
			default:
				from = from.Add((to.Sub(from) - storage.MaxObservationWindow) / 2)
				to = from.Add(storage.MaxObservationWindow)
			}
		}
		var child assistant.Diagnosis
		items, _, err := c.recapEvidenceWindow(ctx, plan, from, to, nil, &child, 1)
		if err != nil {
			return nil, false, err
		}
		if diagnosis != nil {
			diagnosis.RollupsDirty += child.RollupsDirty
		}
		for _, item := range items {
			if item.Layer == assistant.LayerSource {
				raw = append(raw, item)
			} else {
				summaries = append(summaries, item)
			}
		}
	}
	if diagnosis != nil {
		diagnosis.Chronological = true
	}
	leading, _ := selectRecapSummaries(summaries)
	selected, _ := assistant.TimeDistributedEvidence(raw, assistant.MaxEvidenceItems-len(leading))
	return append(leading, selected...), true, nil
}
