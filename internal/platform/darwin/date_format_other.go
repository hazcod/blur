//go:build !darwin

package darwin

import "time"

func nativeDateFormat(time.Time, string, bool, bool) string { return "" }
