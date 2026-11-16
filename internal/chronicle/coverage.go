package chronicle

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Coverage distinguishes gaps already present at capture from source excerpts
// selected for the model and entire sources omitted by packing bounds.
type Coverage struct {
	CapturedPartial    int  `json:"captured_partial_sources"`
	Excerpted          int  `json:"excerpted_sources"`
	Omitted            int  `json:"omitted_sources"`
	SourceLimitReached bool `json:"source_limit_reached"`
}

func (p Packed) Coverage() Coverage {
	c := Coverage{Omitted: p.Omitted, SourceLimitReached: p.SourceLimitReached}
	for _, item := range p.Evidence {
		if item.CapturedPartial() {
			c.CapturedPartial++
		}
		if item.Excerpted() {
			c.Excerpted++
		}
	}
	return c
}

func (c Coverage) hasGaps() bool {
	return c.CapturedPartial > 0 || c.Excerpted > 0 || c.Omitted > 0 || c.SourceLimitReached
}

func (c Coverage) note() string {
	parts := []string{"Coverage: saved captures only"}
	if c.CapturedPartial > 0 {
		parts = append(parts, fmt.Sprintf("%d partial capture(s)", c.CapturedPartial))
	}
	if c.Excerpted > 0 {
		parts = append(parts, fmt.Sprintf("%d source(s) excerpted (head and tail; middle unavailable to recap)", c.Excerpted))
	}
	if c.Omitted > 0 {
		parts = append(parts, fmt.Sprintf("%d source(s) omitted from recap", c.Omitted))
	}
	if c.SourceLimitReached {
		parts = append(parts, "source limit reached; additional captures may be absent")
	}
	return strings.Join(parts, "; ") + "."
}

func applyCoverage(input Packed, summary Summary) (Summary, error) {
	if !validText(summary.Text, MaxSummaryBytes) {
		return Summary{}, ErrInvalid
	}
	summary.Coverage = input.Coverage()
	note := summary.Coverage.note()
	if strings.HasSuffix(summary.Text, note) {
		return summary, nil
	}
	text := strings.TrimSpace(summary.Text)
	limit := MaxSummaryBytes - len(note) - 2
	if len(text) > limit {
		for limit > 0 && !utf8.RuneStart(text[limit]) {
			limit--
		}
		text = strings.TrimSpace(text[:limit])
	}
	summary.Text = text + "\n\n" + note
	return summary, nil
}
