// Package browsercapture validates active browser-page snapshots before they
// enter Blur's existing redaction and encrypted-storage pipeline.
package browsercapture

import (
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/hazcod/blur/internal/capture"
)

const (
	SafariBundleID = "com.apple.safari"
	MaxPageBytes   = 256 << 10
	MaxTitleBytes  = 8 << 10
	MaxURLBytes    = 4 << 10
	MaxSnapshotAge = 15 * time.Second
	MaxClockSkew   = 5 * time.Second
)

var ErrRejected = errors.New("browser capture: page rejected")

// Snapshot is an untrusted envelope from a browser bridge. PrivacyKnown and
// Active must be established by the native browser adapter, not page script.
// The raw URL is used only to derive the hostname and is never persisted.
type Snapshot struct {
	Browser      string    `json:"browser"`
	URL          string    `json:"url"`
	Title        string    `json:"title"`
	Text         string    `json:"text"`
	Active       bool      `json:"active"`
	PrivacyKnown bool      `json:"privacy_known"`
	Private      bool      `json:"private"`
	CapturedAt   time.Time `json:"captured_at"`
}

// Sanitize accepts only a fresh active Safari page with a known non-private
// browsing state and an enabled capture policy. It returns the same validated,
// redacted observation type used by native accessibility capture.
func Sanitize(page Snapshot, enabled bool, now time.Time) (capture.SanitizedObservation, error) {
	if !enabled || !page.Active || !page.PrivacyKnown || page.Private || page.Browser != SafariBundleID ||
		now.IsZero() || page.CapturedAt.IsZero() || len(page.URL) == 0 || len(page.URL) > MaxURLBytes ||
		len(page.Title) > MaxTitleBytes || len(page.Text) == 0 || len(page.Text) > MaxPageBytes || strings.TrimSpace(page.Text) == "" {
		return capture.SanitizedObservation{}, ErrRejected
	}
	if page.CapturedAt.After(now.Add(MaxClockSkew)) || now.Sub(page.CapturedAt) > MaxSnapshotAge {
		return capture.SanitizedObservation{}, ErrRejected
	}
	u, err := url.Parse(page.URL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.Opaque != "" {
		return capture.SanitizedObservation{}, ErrRejected
	}
	if strings.ContainsAny(page.Title, "\x00\r") || strings.ContainsRune(page.Text, 0) {
		return capture.SanitizedObservation{}, ErrRejected
	}
	domain := u.Hostname()
	if domain == "" {
		return capture.SanitizedObservation{}, ErrRejected
	}
	text := page.Text
	if title := strings.TrimSpace(page.Title); title != "" {
		text = title + "\n" + text
	}
	if len(text) > MaxPageBytes {
		return capture.SanitizedObservation{}, ErrRejected
	}
	observation, err := capture.SanitizeObservation(SafariBundleID, domain, text, MaxPageBytes)
	if err != nil {
		return capture.SanitizedObservation{}, ErrRejected
	}
	return observation, nil
}
