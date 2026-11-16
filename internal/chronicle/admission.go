package chronicle

import "sort"

const (
	minAdmissionEvidenceBytes = 128
	// At most five text halvings (4096 -> 128) and six source halvings
	// (64 -> 1), plus the original count. No provider inference occurs here.
	maxAdmissionCounts = 12
)

// shrinkPacked retains head and tail from each supplied source before dropping
// the lower-salience half when fixed request metadata still cannot fit. Every
// change strictly reduces supplied text or source count and keeps provenance.
func shrinkPacked(input Packed) (Packed, bool) {
	if !validPacked(input) {
		return Packed{}, false
	}
	largest := 0
	for _, item := range input.Evidence {
		largest = max(largest, len(item.Text()))
	}
	selected := make([]Evidence, 0, len(input.Evidence))
	if largest > minAdmissionEvidenceBytes {
		limit := max(minAdmissionEvidenceBytes, largest/2)
		for _, item := range input.Evidence {
			selected = append(selected, projectEvidence(item, limit))
		}
	} else {
		if len(input.Evidence) <= 1 {
			return Packed{}, false
		}
		order := salienceOrder(input.Evidence)
		for _, index := range order[:(len(order)+1)/2] {
			selected = append(selected, input.Evidence[index].Clone())
		}
		input.Omitted += len(input.Evidence) - len(selected)
		sort.Slice(selected, func(i, j int) bool {
			if !selected[i].At.Equal(selected[j].At) {
				return selected[i].At.Before(selected[j].At)
			}
			return selected[i].SourceID() < selected[j].SourceID()
		})
	}
	input.Evidence = selected
	input.SourceIDs = make([]string, len(selected))
	input.InputBytes = 0
	for i, item := range selected {
		input.SourceIDs[i] = item.SourceID()
		input.InputBytes += evidenceCost(item)
	}
	sort.Strings(input.SourceIDs)
	return input, validPacked(input)
}
