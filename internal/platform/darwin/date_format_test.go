package darwin

import (
	"testing"
	"time"
)

func TestLocalizedDatesRejectUnknownZonesAndZeroTimes(t *testing.T) {
	for _, at := range []time.Time{{}, time.Now()} {
		if got := LocalizedDateTime(at, "not/a/zone"); got != "" {
			t.Fatalf("invalid timezone acquired a display time: %q", got)
		}
	}
	if got := LocalizedTime(time.Time{}, "UTC"); got != "" {
		t.Fatalf("zero date acquired a display time: %q", got)
	}
	if got := LocalizedShortDateTime(time.Time{}, "UTC"); got != "" {
		t.Fatalf("zero date acquired a short display time: %q", got)
	}
}

func TestDateFormattingFallbackKeepsTheRequestedTimezone(t *testing.T) {
	at := time.Date(2026, 11, 13, 23, 30, 0, 0, time.UTC)
	if got := fallbackDateFormat(at, "Europe/Brussels", false, false); got != "Sat 14 Nov 2026, 00:30:00" {
		t.Fatalf("fallback date used the host timezone: %q", got)
	}
	if got := fallbackDateFormat(at, "Europe/Brussels", false, true); got != "14 Nov 2026, 00:30" {
		t.Fatalf("fallback short date was not compact or used the host timezone: %q", got)
	}
	if got := fallbackDateFormat(at, "America/New_York", true, false); got != "18:30" {
		t.Fatalf("fallback clock used the host timezone: %q", got)
	}
}
