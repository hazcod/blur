package client

import (
	"fmt"
	"strings"
	"time"

	"github.com/golang-gui/goui/layout"
	"github.com/golang-gui/goui/theme/modern"
	"github.com/golang-gui/goui/ui"
)

// searchRunWords is how many plain words one label holds. The preview wraps at
// label boundaries so a matched word can be emphasised in place; keeping plain
// runs short keeps the right edge ragged by a few characters, not a line.
const searchRunWords = 3

// searchWordRunes is the longest unbroken token a preview run shows.
const searchWordRunes = 40

type excerptRun struct {
	text  string
	match bool
}

// excerptRuns splits a preview into wrap units: one unit per matched word and
// blocks of at most searchRunWords plain words between them.
func excerptRuns(excerpt string, terms []string) []excerptRun {
	words, hit := emphasisWords(excerpt, terms)
	if len(words) == 0 {
		return nil
	}
	// A run is a label that cannot wrap inside itself, so one unbroken token
	// (a URL, a hash) is clipped rather than allowed to stretch the row.
	for i, word := range words {
		words[i], _ = boundedPreview(word, searchWordRunes)
	}
	var runs []excerptRun
	var plain []string
	flush := func() {
		if len(plain) > 0 {
			runs = append(runs, excerptRun{text: strings.Join(plain, " ")})
			plain = nil
		}
	}
	for i, word := range words {
		if hit[i] {
			flush()
			runs = append(runs, excerptRun{text: word, match: true})
			continue
		}
		plain = append(plain, word)
		if len(plain) == searchRunWords {
			flush()
		}
	}
	flush()
	return runs
}

// searchStatusLine is the one status line under a search: how many captures
// matched, how many summaries sit beside them, and whether the closest matches
// (some keywords only) were included.
func searchStatusLine(hits, summaries int, relaxed, more bool) string {
	line := fmt.Sprintf("%d results", hits)
	if hits == 1 {
		line = "1 result"
	}
	if summaries == 1 {
		line += " · 1 summary"
	} else if summaries > 1 {
		line += fmt.Sprintf(" · %d summaries", summaries)
	}
	if relaxed {
		line += " · closest matches included"
	}
	if more {
		line += "; more matches are available. Narrow the search to see them."
	}
	return line
}

// summaryHeading names a summary row by its level and local period.
func summaryHeading(summary MemorySummaryHit) string {
	start, end := summary.Start.In(time.Local), summary.End.In(time.Local)
	switch summary.Level {
	case "day":
		return "Day summary · " + start.Format("2 Jan")
	case "week":
		last := end.Add(-time.Nanosecond)
		if start.Month() == last.Month() {
			return fmt.Sprintf("Week summary · %d–%s", start.Day(), last.Format("2 Jan"))
		}
		return "Week summary · " + start.Format("2 Jan") + "–" + last.Format("2 Jan")
	default:
		return "Hour summary · " + start.Format("2 Jan, 15:04")
	}
}

// searchHitCaption is "App · Title · date time". The window title is shown
// when the capture has one, else the site.
func searchHitCaption(hit SearchHit) string {
	parts := make([]string, 0, 3)
	if hit.AppID != "" {
		parts = append(parts, displayAppName(hit.AppID))
	}
	switch {
	case strings.TrimSpace(hit.WindowTitle) != "":
		parts = append(parts, strings.TrimSpace(hit.WindowTitle))
	case hit.Domain != "":
		parts = append(parts, hit.Domain)
	}
	switch {
	case hit.TimeUnknown:
		parts = append(parts, "Capture time unavailable")
	case !hit.CapturedAt.IsZero():
		parts = append(parts, hit.CapturedAt.In(time.Local).Format("2 Jan 2006, 15:04"))
	}
	return strings.Join(parts, " · ")
}

// usesSearchPreview reports a search row that renders as header plus preview.
// Notes, voice notes and tasks keep their own presentation.
func usesSearchPreview(hit SearchHit) bool {
	return hit.Task == nil && hit.Excerpt != "" && hit.AppID != "sh.blur.notes" && hit.AppID != "sh.blur.meetings"
}

// excerptView shows a preview with its matched words emphasised: muted ink for
// the surrounding text, the primary ink in bold for the match. No accent colour
// is spent on it.
func excerptView(excerpt string, terms []string) ui.View {
	runs := excerptRuns(excerpt, terms)
	anyMatch := false
	for _, run := range runs {
		anyMatch = anyMatch || run.match
	}
	if !anyMatch {
		return wrapLabel(excerpt).Style(modern.MutedText)
	}
	views := make([]ui.View, 0, len(runs))
	for i, run := range runs {
		text := run.text
		if i+1 < len(runs) {
			text += " "
		}
		label := ui.Label(text)
		if run.match {
			label = label.Style(styleMatch)
		} else {
			label = label.Style(modern.MutedText)
		}
		views = append(views, label)
	}
	return ui.HWrap(views...).CrossAlign(layout.CrossStart)
}

// searchDivider is the thin marker before the closest (partial) matches.
func searchDivider() ui.View {
	return ui.VBox(hairline(), wrapLabel("Closest matches").Style(styleMeta)).Spacing(6).CrossAlign(layout.CrossStretch)
}

// summaryRow is one recap index row: its period, a preview and nothing that
// pretends to be evidence. The sources it covers are reached through the
// capture rows and the recap itself.
func summaryRow(summary MemorySummaryHit, terms []string) ui.View {
	rows := []ui.View{wrapLabel(summaryHeading(summary)).Style(styleMeta)}
	if summary.Excerpt != "" {
		rows = append(rows, excerptView(summary.Excerpt, terms))
	}
	if summary.Dirty {
		rows = append(rows, wrapLabel("This recap is waiting to be refreshed.").Style(styleMeta))
	}
	return ui.VBox(rows...).Spacing(6).CrossAlign(layout.CrossStretch)
}
