package capture

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/privacy"
)

// The window title says which document, conversation or page the captured text
// came from ("Dima, Dziga (DM) - Cresco - Slack"). It is stored beside the
// text, redacted exactly as the text is, and indexed as its own search field.
// A title is optional context: it never fails or delays a capture, it is never
// read for a secure, elevated or private window (those captures stop before
// any read), and it is not part of the content ID.
const (
	// MaxTitleBytes bounds the stored title after redaction (UTF-8 safe).
	MaxTitleBytes = 256
	// TitleReadBudget bounds the whole native title read. A slower read is
	// abandoned and the capture proceeds without a title.
	TitleReadBudget = 100 * time.Millisecond
)

// Title returns the sanitized window title, or "" when none was recorded.
func (o SanitizedObservation) Title() string { return strings.Clone(o.title) }

func (o SanitizedObservation) hasTitle() bool { return o.title != "" }

// WithTitle returns the observation with a window title. The content ID is
// unchanged. The title is flattened to one line, redacted to a fixed point and
// bounded here; a title that cannot be made safe is dropped.
func (o SanitizedObservation) WithTitle(title string) SanitizedObservation {
	out := o.Clone()
	out.title = sanitizeTitle(title)
	return out
}

func (o SanitizedObservation) withTitleRaw(title string) SanitizedObservation {
	out := o.Clone()
	out.title = title
	return out
}

func validTitle(title string) bool {
	if len(title) > MaxTitleBytes || !utf8.ValidString(title) || strings.ContainsRune(title, 0) {
		return false
	}
	if title != strings.TrimSpace(title) {
		return false
	}
	redacted, _ := privacy.Redact(title)
	return redacted == title
}

// sanitizeTitle returns a title that is a fixed point of the redactor and at
// most MaxTitleBytes, or "" when no such value is reachable within a bounded
// number of passes (see sanitizeFocusText for why it repeats).
func sanitizeTitle(value string) string {
	value = cleanFocusText(value)
	for pass := 0; pass < 8 && value != ""; pass++ {
		redacted, _ := privacy.Redact(value)
		redacted, _ = truncateUTF8(redacted, MaxTitleBytes)
		redacted = strings.TrimSpace(redacted)
		if redacted == value && validTitle(value) {
			return value
		}
		value = redacted
	}
	return ""
}

// readTitleForCapture runs the optional native title reader under its own
// budget. It never fails the capture: a missing, failed, panicking or late
// read yields "". browser marks a browser app, whose private-window titles are
// never kept even if a reader hands one back.
func readTitleForCapture(ctx context.Context, read func(context.Context) string, browser bool) string {
	if read == nil || ctx.Err() != nil {
		return ""
	}
	titleCtx, cancel := context.WithTimeout(ctx, TitleReadBudget)
	defer cancel()
	raw := safeTitleRead(titleCtx, read)
	if raw == "" || titleCtx.Err() != nil {
		return ""
	}
	if browser && isPrivateBrowserWindowTitle(raw) {
		return ""
	}
	return sanitizeTitle(raw)
}

func safeTitleRead(ctx context.Context, read func(context.Context) string) (title string) {
	defer func() {
		if recover() != nil {
			title = ""
		}
	}()
	return read(ctx)
}
