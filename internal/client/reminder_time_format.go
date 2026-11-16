package client

import (
	"time"

	"github.com/hazcod/blur/internal/platform/darwin"
	"github.com/hazcod/blur/internal/tasks"
)

func reminderDateTimeCopy(r tasks.Reminder) string {
	if text := darwin.LocalizedShortDateTime(r.DueAt, r.Timezone); text != "" {
		return text
	}
	return "Time unavailable"
}

// Evidence needs the precise instant as well as a readable localized label.
// Display preferences must never collapse two distinct native queue receipts.
func reminderEvidenceTimeCopy(r tasks.Reminder) string {
	label := reminderScheduleCopy(r)
	if r.Repeat == tasks.Once {
		if text := darwin.LocalizedDateTime(r.DueAt, r.Timezone); text != "" {
			label = text + " (" + r.Timezone + ")"
		}
	} else {
		label += " (" + r.Timezone + ")"
	}
	return label + " [" + r.DueAt.UTC().Format(time.RFC3339Nano) + "]"
}

func reminderClockCopy(r tasks.Reminder) string {
	zone, err := time.LoadLocation(r.Timezone)
	if err != nil {
		return "Time unavailable"
	}
	// Only the wall clock is displayed. A fixed valid date avoids changing a
	// recurring rule's hour through the offset of the current date or host zone.
	at := time.Date(2026, time.January, 15, r.Hour, r.Minute, 0, 0, zone)
	return darwin.LocalizedTime(at, r.Timezone)
}
