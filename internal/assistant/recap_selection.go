package assistant

import (
	"sort"
	"time"
)

// Farthest-time selection retains both ends of the period before filling its
// largest uncovered gaps. Dense recent hours cannot crowd out the morning.
func TimeDistributedEvidence(input []Evidence, limit int) ([]Evidence, bool) {
	if limit <= 0 {
		return nil, len(input) > 0
	}
	byID := map[string]Evidence{}
	for _, item := range input {
		old, found := byID[item.SourceID]
		if !found || item.Layer > old.Layer || item.Layer == old.Layer && item.CapturedAt.After(old.CapturedAt) {
			byID[item.SourceID] = item
		}
	}
	items := make([]Evidence, 0, len(byID))
	for _, item := range byID {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if !items[i].CapturedAt.Equal(items[j].CapturedAt) {
			return items[i].CapturedAt.Before(items[j].CapturedAt)
		}
		return items[i].SourceID < items[j].SourceID
	})
	if len(items) <= limit {
		return items, false
	}
	if limit == 1 {
		return items[len(items)-1:], true
	}
	chosen := map[int]bool{0: true, len(items) - 1: true}
	for len(chosen) < limit {
		best, furthest := -1, time.Duration(-1)
		for index, item := range items {
			if chosen[index] {
				continue
			}
			nearest := time.Duration(1<<63 - 1)
			for other := range chosen {
				distance := item.CapturedAt.Sub(items[other].CapturedAt)
				if distance < 0 {
					distance = -distance
				}
				nearest = min(nearest, distance)
			}
			if nearest > furthest {
				best, furthest = index, nearest
			}
		}
		chosen[best] = true
	}
	out := make([]Evidence, 0, limit)
	for index, item := range items {
		if chosen[index] {
			out = append(out, item)
		}
	}
	return out, true
}
