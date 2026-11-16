//go:build darwin

package darwin

import (
	"sync"
	"time"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

var dateFormatterFoundation struct {
	once sync.Once
	ok   bool
}

func nativeDateFormat(at time.Time, zoneName string, clockOnly, shortDateTime bool) string {
	dateFormatterFoundation.once.Do(func() {
		_, err := purego.Dlopen("/System/Library/Frameworks/Foundation.framework/Foundation", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		dateFormatterFoundation.ok = err == nil
	})
	if !dateFormatterFoundation.ok {
		return ""
	}
	var text string
	// The helper pins the thread through allocation, formatting and pool drain.
	// Each call owns its formatter, so background receipt formatting shares no
	// mutable NSDateFormatter state and picks up changed system preferences.
	withAutoreleasePool(func() {
		class := objc.GetClass("NSDateFormatter")
		if class == 0 {
			return
		}
		formatter := objc.ID(class).Send(sel("new"))
		if formatter == 0 {
			return
		}
		defer formatter.Send(sel("release"))
		locale := objc.ID(objc.GetClass("NSLocale")).Send(sel("autoupdatingCurrentLocale"))
		zone := objc.ID(objc.GetClass("NSTimeZone")).Send(sel("timeZoneWithName:"), nsStringFrom(zoneName))
		if locale == 0 || zone == 0 {
			return
		}
		formatter.Send(sel("setLocale:"), locale)
		formatter.Send(sel("setTimeZone:"), zone)
		dateStyle := uint64(2) // NSDateFormatterMediumStyle
		if clockOnly {
			dateStyle = 0 // NSDateFormatterNoStyle
		} else if shortDateTime {
			dateStyle = 1 // NSDateFormatterShortStyle
		}
		formatter.Send(sel("setDateStyle:"), dateStyle)
		timeStyle := uint64(2) // NSDateFormatterMediumStyle keeps second delays visible.
		if clockOnly {
			timeStyle = 1 // Recurring rules have minute precision.
		} else if shortDateTime {
			timeStyle = 1 // Compact labels use the user's 12/24-hour preference.
		}
		formatter.Send(sel("setTimeStyle:"), timeStyle)
		seconds := float64(at.Unix()) + float64(at.Nanosecond())/float64(time.Second)
		date := objc.ID(objc.GetClass("NSDate")).Send(sel("dateWithTimeIntervalSince1970:"), seconds)
		text, _ = goStringFrom(formatter.Send(sel("stringFromDate:"), date))
	})
	return text
}
