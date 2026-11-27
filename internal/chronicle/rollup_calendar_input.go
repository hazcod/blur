package chronicle

import "unicode/utf8"

// balancedCalendarInput keeps each child represented before spending more
// bytes on any one child. The bound includes labels, separators and disclosure.
func balancedCalendarInput(level string, children []RollupChild) RollupInput {
	in := RollupInput{Level: level}
	if len(children) > MaxRollupChildren {
		chosen := make([]RollupChild, MaxRollupChildren)
		for i := range chosen {
			chosen[i] = children[i*(len(children)-1)/(MaxRollupChildren-1)]
		}
		children, in.Omitted = chosen, true
	}
	in.Children = append([]RollupChild(nil), children...)
	remaining := MaxRollupInputBytes - 128
	for i := range in.Children {
		c := &in.Children[i]
		// Labels are worker generated; bound them defensively for this API too.
		if len(c.Label) > 64 {
			c.Label = clipRollupInput(c.Label, 64)
			in.Omitted = true
		}
		remaining -= len(c.Label) + 4
	}
	for i := range in.Children {
		c := &in.Children[i]
		allowance := max(0, remaining/(len(in.Children)-i))
		if len(c.Text) > allowance {
			c.Text = clipRollupInput(c.Text, allowance)
			in.Omitted = true
		}
		remaining -= len(c.Text)
	}
	in.Omitted = in.Omitted || inputCoverageLimited(in.Children)
	return in
}

func clipRollupInput(s string, size int) string {
	if len(s) <= size {
		return s
	}
	for size > 0 && !utf8.RuneStart(s[size]) {
		size--
	}
	return s[:size]
}

func inputCoverageLimited(children []RollupChild) bool {
	seen := make(map[string]struct{}, MaxRollupGeneratedSources)
	for _, c := range children {
		if c.Sampled {
			return true
		}
		for _, id := range c.SourceIDs {
			seen[id] = struct{}{}
			if len(seen) > MaxRollupGeneratedSources {
				return true
			}
		}
	}
	return false
}
