package activity

import (
	"testing"
	"time"
)

func BenchmarkReportYear(b *testing.B) {
	segments := syntheticYearSegments()
	want := int64(len(segments)) * int64(time.Minute)
	b.ReportAllocs()
	b.SetBytes(int64(len(segments)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		report, err := Report(segments, time.UTC)
		if err != nil {
			b.Fatal(err)
		}
		if report.Eligible != want || len(report.Days) != 365 {
			b.Fatalf("year report eligible=%d days=%d", report.Eligible, len(report.Days))
		}
	}
}
