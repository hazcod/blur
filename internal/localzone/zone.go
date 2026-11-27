// Package localzone resolves stable zone names for local calendar projections.
package localzone

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Name uses the same machine zone for note saves, Today, and reminders.
func Name() string { return Resolve(os.Getenv("TZ"), "/etc/localtime", time.Now()) }

// Resolve accepts an explicit valid TZ or the system zoneinfo symlink, then
// returns the existing fixed-offset fallback when no IANA name is available.
func Resolve(tzEnv, localtimePath string, now time.Time) string {
	if name := strings.TrimPrefix(tzEnv, ":"); name != "" && name != "Local" {
		if _, err := time.LoadLocation(name); err == nil {
			return name
		}
	}
	if target, err := os.Readlink(localtimePath); err == nil {
		target = strings.ReplaceAll(target, `\`, "/")
		const marker = "zoneinfo/"
		if i := strings.LastIndex(target, marker); i >= 0 {
			if name := target[i+len(marker):]; name != "" && name != "Local" {
				if _, err := time.LoadLocation(name); err == nil {
					return name
				}
			}
		}
	}
	abbreviation, offset := now.In(time.Local).Zone()
	sign := '+'
	if offset < 0 {
		sign, offset = '-', -offset
	}
	return fmt.Sprintf("%s%c%02d%02d", abbreviation, sign, offset/3600, offset%3600/60)
}
