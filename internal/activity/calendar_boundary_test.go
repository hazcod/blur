package activity

import (
	"testing"
	"time"
)

func TestStartOfDateUsesFirstRealInstant(t *testing.T) {
	for _, tc := range []struct{ zone, date, want string }{
		{"America/Santiago", "2024-09-08", "2024-09-08T04:00:00Z"},
		{"America/Havana", "2024-11-03", "2024-11-03T04:00:00Z"},
		{"Pacific/Apia", "2011-12-30", "2011-12-30T10:00:00Z"},
		{"Asia/Kathmandu", "2024-01-01", "2023-12-31T18:15:00Z"},
	} {
		zone, err := time.LoadLocation(tc.zone)
		if err != nil {
			t.Fatal(err)
		}
		date, err := time.Parse("2006-01-02", tc.date)
		if err != nil {
			t.Fatal(err)
		}
		want, err := time.Parse(time.RFC3339, tc.want)
		if err != nil {
			t.Fatal(err)
		}
		if got := StartOfDate(date.Year(), date.Month(), date.Day(), zone); !got.Equal(want) {
			t.Errorf("%s %s = %s, want %s", tc.zone, tc.date, got, want)
		}
	}
}

func TestReportAdvancesAcrossSkippedAndRepeatedMidnights(t *testing.T) {
	for _, tc := range []struct {
		zone, start string
		days        map[string]time.Duration
	}{
		{"America/Santiago", "2024-09-08T03:30:00Z", map[string]time.Duration{"2024-09-07": 30 * time.Minute, "2024-09-08": 30 * time.Minute}},
		{"America/Havana", "2024-11-03T04:30:00Z", map[string]time.Duration{"2024-11-03": time.Hour}},
		{"Pacific/Apia", "2011-12-30T09:30:00Z", map[string]time.Duration{"2011-12-29": 30 * time.Minute, "2011-12-31": 30 * time.Minute}},
	} {
		t.Run(tc.zone, func(t *testing.T) {
			zone, err := time.LoadLocation(tc.zone)
			if err != nil {
				t.Fatal(err)
			}
			at, err := time.Parse(time.RFC3339, tc.start)
			if err != nil {
				t.Fatal(err)
			}
			type result struct {
				report ReportResult
				err    error
			}
			done := make(chan result, 1)
			go func() {
				r, err := Report([]Segment{{ID: "midnight", AppID: "reader", WallStart: at, WallEnd: at.Add(time.Hour), Duration: int64(time.Hour)}}, zone)
				done <- result{r, err}
			}()
			select {
			case got := <-done:
				if got.err != nil || got.report.Eligible != int64(time.Hour) || len(got.report.Days) != len(tc.days) {
					t.Fatalf("report %+v %v", got.report, got.err)
				}
				for _, day := range got.report.Days {
					if day.Duration != int64(tc.days[day.Date]) {
						t.Errorf("day %+v want %s", day, tc.days[day.Date])
					}
				}
			case <-time.After(2 * time.Second):
				t.Fatal("calendar splitter stopped advancing")
			}
		})
	}
}
