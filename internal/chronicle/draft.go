package chronicle

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Bounds for the extractive "this hour so far" draft.
const (
	MaxDraftLines      = 7
	MaxDraftBytes      = 640
	maxDraftActivity   = 3
	maxDraftTitles     = 4
	maxDraftTitleRunes = 80
	maxDraftUpperShare = 0.6
)

// ActivityLine is one app or site with the time spent in it during the hour.
type ActivityLine struct {
	Label    string
	Duration time.Duration
}

// Draft is a deterministic, model-free, in-memory preview of an unfinished
// hour. It is never persisted and cites no model output.
type Draft struct {
	Start, End time.Time
	Lines      []string
	Sources    int
	// Omitted reports that eligible lines were dropped to honour the bounds.
	Omitted bool
}

// DraftHour builds a purely extractive preview: the top three app/site lines by
// duration, then up to four representative evidence titles chosen with the
// same salience ordering as PackHour. Output is at most MaxDraftLines lines and
// MaxDraftBytes bytes and is identical for identical input.
func DraftHour(hour Hour, activity []ActivityLine, now time.Time) Draft {
	draft := Draft{Start: hour.Start, End: now.UTC(), Sources: len(hour.Evidence)}
	if !hour.End.IsZero() && draft.End.After(hour.End) {
		draft.End = hour.End
	}
	if draft.End.Before(draft.Start) {
		draft.End = draft.Start
	}
	var candidates []string
	rows := make([]ActivityLine, 0, len(activity))
	for _, line := range activity {
		if label := cleanDraftText(line.Label); label != "" && minutesOf(line.Duration) > 0 {
			line.Label = label
			rows = append(rows, line)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Duration != rows[j].Duration {
			return rows[i].Duration > rows[j].Duration
		}
		return rows[i].Label < rows[j].Label
	})
	seen := make(map[string]struct{})
	titles := 0
	for _, row := range rows {
		key := strings.ToLower(row.Label)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		if len(candidates) == maxDraftActivity {
			draft.Omitted = true
			continue
		}
		candidates = append(candidates, fmt.Sprintf("%s, %d min", row.Label, minutesOf(row.Duration)))
	}
	seenTitles := make(map[string]struct{})
	for _, index := range salienceOrder(hour.Evidence) {
		title := draftTitle(hour.Evidence[index].Observation.Text())
		if title == "" {
			continue
		}
		key := strings.ToLower(title)
		if _, dup := seenTitles[key]; dup {
			continue
		}
		seenTitles[key] = struct{}{}
		if titles == maxDraftTitles {
			draft.Omitted = true
			continue
		}
		titles++
		candidates = append(candidates, title)
	}
	total := 0
	for _, line := range candidates {
		if len(draft.Lines) == MaxDraftLines || total+len(line) > MaxDraftBytes {
			draft.Omitted = true
			continue
		}
		draft.Lines = append(draft.Lines, line)
		total += len(line)
	}
	return draft
}

func minutesOf(d time.Duration) int { return int((d + 30*time.Second) / time.Minute) }

// cleanDraftText drops control characters and collapses whitespace.
func cleanDraftText(text string) string {
	text = strings.Map(func(r rune) rune {
		if r == utf8.RuneError || unicode.IsControl(r) && !unicode.IsSpace(r) {
			return -1
		}
		return r
	}, text)
	return strings.Join(strings.Fields(text), " ")
}

// draftTitle derives a short title from the first usable line of evidence
// text: URL- and path-like words are removed, shouting is rejected, and the
// result is cut to maxDraftTitleRunes on a word boundary.
func draftTitle(text string) string {
	for _, rawLine := range strings.Split(text, "\n") {
		words := strings.Fields(cleanDraftText(rawLine))
		kept := words[:0]
		for _, word := range words {
			if strings.Contains(word, "/") || strings.Contains(word, "\\") {
				continue
			}
			kept = append(kept, word)
		}
		title := strings.Join(kept, " ")
		if title == "" || shouting(title) {
			continue
		}
		return truncateWords(title, maxDraftTitleRunes)
	}
	return ""
}

func shouting(text string) bool {
	letters, upper := 0, 0
	for _, r := range text {
		if unicode.IsLetter(r) {
			letters++
			if unicode.IsUpper(r) {
				upper++
			}
		}
	}
	return letters > 0 && float64(upper) > maxDraftUpperShare*float64(letters)
}

func truncateWords(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)[:limit-1]
	cut := len(runes)
	for i := len(runes) - 1; i > 0; i-- {
		if runes[i] == ' ' {
			cut = i
			break
		}
	}
	return strings.TrimRight(string(runes[:cut]), " ,;:-.") + "…"
}
