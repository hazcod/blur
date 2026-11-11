package client

import (
	"testing"
	"time"
)

func TestReportBarFill(t *testing.T) {
	cases := []struct {
		value, largest int64
		want           float32
	}{{0, 10, 0}, {10, 0, 0}, {10, 10, 120}, {5, 10, 60}, {1, 1000, 2}, {20, 10, 120}}
	for _, c := range cases {
		if got := reportBarFill(c.value, c.largest, 120); got != c.want {
			t.Errorf("fill(%d,%d)=%v want %v", c.value, c.largest, got, c.want)
		}
	}
}

func TestReportPresets(t *testing.T) {
	now := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC) // Wednesday
	want := map[string][2]string{
		"Today": {"2026-11-11", "2026-11-11"}, "Yesterday": {"2026-11-10", "2026-11-10"},
		"This week": {"2026-11-09", "2026-11-11"}, "Last 7 days": {"2026-11-05", "2026-11-11"},
		"Last 30 days": {"2026-10-13", "2026-11-11"},
	}
	for name, w := range want {
		s, e, ok := reportPresetRange(name, now)
		if !ok || s != w[0] || e != w[1] {
			t.Errorf("%s = %s..%s", name, s, e)
		}
		if reportSelectedPreset(w[0], w[1], now) == "" {
			t.Errorf("%s not matched", name)
		}
	}
	if got := reportZoneLine("Europe/Brussels"); got != "Times in Europe/Brussels" {
		t.Error(got)
	}
	if first, rest := splitFirstSentence("A b. C d."); first != "A b." || rest != "C d." {
		t.Error(first, rest)
	}
}
