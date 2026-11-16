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

// DayBalancedEvidence selects up to limit items from a multi-day period so no
// local day with saved items is left out while another day gets a second one,
// and returns them oldest first. Within a day, picks spread from the day's
// first item to its last before filling the gaps between them. When there are
// more days than slots, TimeDistributedEvidence chooses among the days so the
// period's two ends are always kept. The result is true when items were left
// out.
func DayBalancedEvidence(input []Evidence, limit int, zone *time.Location) ([]Evidence, bool) {
	if limit <= 0 {
		return nil, len(input) > 0
	}
	if zone == nil {
		zone = time.UTC
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
	sortChronologically(items)
	if len(items) <= limit {
		return items, false
	}
	var dayKeys []string
	days := map[string][]Evidence{}
	for _, item := range items {
		key := item.CapturedAt.In(zone).Format("2006-01-02")
		if _, seen := days[key]; !seen {
			dayKeys = append(dayKeys, key)
		}
		days[key] = append(days[key], item)
	}
	// Each day's items in the order they should be admitted.
	spread := make(map[string][]Evidence, len(days))
	for key, dayItems := range days {
		ordered := make([]Evidence, 0, len(dayItems))
		for _, index := range spreadOrder(len(dayItems)) {
			ordered = append(ordered, dayItems[index])
		}
		spread[key] = ordered
	}
	var chosen []Evidence
	for round := 0; len(chosen) < limit; round++ {
		var pool []Evidence
		for _, key := range dayKeys {
			if round < len(spread[key]) {
				pool = append(pool, spread[key][round])
			}
		}
		if len(pool) == 0 {
			break
		}
		if remaining := limit - len(chosen); len(pool) > remaining {
			picked, _ := TimeDistributedEvidence(pool, remaining)
			chosen = append(chosen, picked...)
			break
		}
		chosen = append(chosen, pool...)
	}
	sortChronologically(chosen)
	return chosen, true
}

func sortChronologically(items []Evidence) {
	sort.SliceStable(items, func(i, j int) bool {
		if !items[i].CapturedAt.Equal(items[j].CapturedAt) {
			return items[i].CapturedAt.Before(items[j].CapturedAt)
		}
		return items[i].SourceID < items[j].SourceID
	})
}

// spreadOrder lists 0..n-1 as first, last, then repeated midpoints, so any
// prefix covers the span as evenly as its length allows.
func spreadOrder(n int) []int {
	if n <= 0 {
		return nil
	}
	order := []int{0}
	if n == 1 {
		return order
	}
	order = append(order, n-1)
	type span struct{ lo, hi int }
	queue := []span{{0, n - 1}}
	for len(queue) > 0 {
		s := queue[0]
		queue = queue[1:]
		if s.hi-s.lo < 2 {
			continue
		}
		mid := (s.lo + s.hi) / 2
		order = append(order, mid)
		queue = append(queue, span{s.lo, mid}, span{mid, s.hi})
	}
	return order
}

// chronologicalOrder lists stored recaps (week, day, digest) first and every
// other item oldest first, so a long-period answer reads as a timeline.
func chronologicalOrder(evidence []Evidence) []Evidence {
	var leading, rest []Evidence
	for _, item := range evidence {
		if item.Layer >= LayerDay {
			leading = append(leading, item)
		} else {
			rest = append(rest, item)
		}
	}
	sortChronologically(leading)
	sortChronologically(rest)
	return append(leading, rest...)
}
