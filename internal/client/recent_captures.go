package client

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/appidentity"
	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/storage"
)

const maxRecentDayCaptures = 8

// recentPlanEvidence joins exact persisted visit timestamps with legacy
// activity sightings. Raw evidence is available before a session closes. Each filter
// read is bounded, and the returned deduplicated evidence stays under one
// aggregate source-byte ceiling.
func (c *Client) recentPlanEvidence(ctx context.Context, plan assistant.Plan, start, end time.Time, segments []activity.Segment, limit int) ([]assistant.Evidence, error) {
	items, _, err := c.recentPlanEvidenceWithCoverage(ctx, plan, start, end, segments, limit)
	return items, err
}

func (c *Client) recentPlanEvidenceWithCoverage(ctx context.Context, plan assistant.Plan, start, end time.Time, segments []activity.Segment, limit int) ([]assistant.Evidence, bool, error) {
	if limit < 1 || limit > planEvidenceItems || len(plan.AppIDs)+len(plan.Domains) > 8 {
		return nil, false, ErrInvalidRequest
	}
	sampled := uniqueSources(segments) > limit
	ids, starts := recentSourceIDs(segments, limit)
	evidence, err := c.hydrateSources(ctx, ids, starts, limit)
	if err != nil {
		return nil, false, err
	}
	if reader, ok := c.data.(ObservationWindowReader); ok {
		if !end.After(start) || end.Sub(start) > storage.MaxObservationWindow {
			return nil, false, ErrActivityLimit
		}
		qualifiers, err := memoryFilters(plan.AppIDs, plan.Domains)
		if err != nil {
			return nil, false, err
		}
		filters := make([]storage.ObservationWindowRequest, 0, len(qualifiers))
		for _, q := range qualifiers {
			filters = append(filters, storage.ObservationWindowRequest{Start: start, End: end, AppID: q.app, Domain: q.domain, Limit: limit, MaxBytes: storage.MaxObservationPageBytes})
		}

		for _, filter := range filters {
			filter.ExcludeAppIDs = []string{appidentity.BundleID}
			page, err := readSourceWindow(ctx, reader, filter)
			if err != nil {
				return nil, false, err
			}
			if len(page.CapturedAt) != len(page.Sources) || len(page.Sources) > filter.Limit {
				return nil, false, ErrInvalidRequest
			}
			sampled = sampled || page.HasMore || page.Pending
			for index, observation := range page.Sources {
				item := newEvidence(observation.SourceID(), observation.AppID(), observation.Domain(), observation.Text())
				item.Partial = observation.Truncated()
				item.CapturedAt = page.CapturedAt[index]
				if provenance, ok := observation.Provenance(); ok {
					item.Document = assistant.DocumentProvenance{
						EditionID: provenance.EditionID, FamilyID: provenance.FamilyID, Publication: provenance.Publication,
						PartIndex: provenance.PartIndex, PartCount: provenance.PartCount,
						TotalBytes: provenance.TotalBytes, Start: provenance.Start, End: provenance.End,
						Coverage: uint8(provenance.Coverage),
					}
				}
				evidence = append(evidence, item)
			}
		}
	}
	byID := make(map[string]assistant.Evidence, len(evidence))
	for _, item := range evidence {
		if strings.EqualFold(item.AppID, appidentity.BundleID) || strings.TrimSpace(item.Text) == "" {
			continue
		}
		previous, found := byID[item.SourceID]
		if !found || item.CapturedAt.After(previous.CapturedAt) {
			byID[item.SourceID] = item
		}
	}
	sampled = sampled || len(byID) > limit
	ordered := make([]assistant.Evidence, 0, len(byID))
	for _, item := range byID {
		ordered = append(ordered, item)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if !ordered[i].CapturedAt.Equal(ordered[j].CapturedAt) {
			return ordered[i].CapturedAt.After(ordered[j].CapturedAt)
		}
		return ordered[i].SourceID > ordered[j].SourceID
	})
	out := make([]assistant.Evidence, 0, min(len(ordered), limit))
	used := 0
	for _, item := range ordered {
		bytes := len(item.Text) + len(item.AppID) + len(item.Domain)
		if len(out) == limit || bytes > MaxSearchBytes-used {
			sampled = true
			break
		}
		used += bytes
		out = append(out, item)
	}
	return out, sampled, nil
}

// recentDayCaptures reads primary evidence directly. A capture does not need
// to wait for an activity segment or model-generated recap to become visible.
// Nothing is written into a projection or sent to a provider by this read.
func recentDayCaptures(ctx context.Context, reader ObservationWindowReader, date, zoneName string) ([]RecentCapture, error) {
	zone, err := loadZone(zoneName)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	start, err := time.ParseInLocation("2006-01-02", date, zone)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	page, err := readSourceWindow(ctx, reader, storage.ObservationWindowRequest{
		Start: start, End: start.AddDate(0, 0, 1), ExcludeAppIDs: []string{appidentity.BundleID}, Limit: maxRecentDayCaptures, MaxBytes: storage.MaxObservationPageBytes,
	})
	if err != nil {
		return nil, err
	}
	out := make([]RecentCapture, 0, min(len(page.Sources), maxRecentDayCaptures))
	for index, observation := range page.Sources {
		// Older app versions captured their own UI. Keep those recursive search
		// and Ask snapshots out of the day preview without deleting user data.
		if strings.EqualFold(observation.AppID(), appidentity.BundleID) || strings.TrimSpace(observation.Text()) == "" {
			continue
		}
		if index >= len(page.CapturedAt) {
			return nil, ErrInvalidRequest
		}
		out = append(out, RecentCapture{
			SearchHit:  SearchHit{SourceID: observation.SourceID(), AppID: observation.AppID(), Domain: observation.Domain(), Text: observation.Text(), Truncated: observation.Truncated()},
			CapturedAt: page.CapturedAt[index],
		})
		if len(out) == maxRecentDayCaptures {
			break
		}
	}
	return out, nil
}
