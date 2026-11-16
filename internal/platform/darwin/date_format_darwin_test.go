//go:build darwin

package darwin

import (
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// Foundation is the oracle: this test does not assume that a timezone implies
// a language, date order, or a 12/24-hour preference. It changes no user setting.
func TestLocalizedReminderDatesMatchCurrentFoundationPreferences(t *testing.T) {
	if _, err := purego.Dlopen("/System/Library/Frameworks/Foundation.framework/Foundation", purego.RTLD_NOW|purego.RTLD_GLOBAL); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 13, 23, 30, 0, 0, time.UTC)
	for _, zone := range []string{"Europe/Brussels", "America/New_York", "UTC"} {
		for _, mode := range []string{"dateTime", "clock", "short"} {
			var want string
			withAutoreleasePool(func() {
				formatter := objc.ID(objc.GetClass("NSDateFormatter")).Send(sel("new"))
				defer formatter.Send(sel("release"))
				locale := objc.ID(objc.GetClass("NSLocale")).Send(sel("autoupdatingCurrentLocale"))
				formatter.Send(sel("setLocale:"), locale)
				location := objc.ID(objc.GetClass("NSTimeZone")).Send(sel("timeZoneWithName:"), nsStringFrom(zone))
				formatter.Send(sel("setTimeZone:"), location)
				dateStyle, timeStyle := uint64(2), uint64(2)
				switch mode {
				case "clock":
					dateStyle, timeStyle = 0, 1
				case "short":
					dateStyle, timeStyle = 1, 1
				}
				formatter.Send(sel("setDateStyle:"), dateStyle)
				formatter.Send(sel("setTimeStyle:"), timeStyle)
				date := objc.ID(objc.GetClass("NSDate")).Send(sel("dateWithTimeIntervalSince1970:"), float64(at.Unix()))
				want, _ = goStringFrom(formatter.Send(sel("stringFromDate:"), date))
			})
			got := LocalizedDateTime(at, zone)
			switch mode {
			case "clock":
				got = LocalizedTime(at, zone)
			case "short":
				got = LocalizedShortDateTime(at, zone)
			}
			if want == "" || got != want {
				t.Errorf("current region/clock preference ignored for %s (%s): got %q want %q", zone, mode, got, want)
			}
		}
	}
}

func TestLocalizedDateFormattingKeepsNativePoolLifetimePinned(t *testing.T) {
	at := time.Date(2026, 11, 13, 12, 30, 0, 0, time.UTC)
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			for range 32 {
				runtime.Gosched()
				if got := LocalizedDateTime(at, "Europe/Brussels"); got == "" {
					t.Error("localized display failed under scheduler pressure")
				}
			}
		})
	}
	group.Wait()
}
