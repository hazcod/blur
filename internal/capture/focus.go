package capture

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/privacy"
)

// The focused-element breadcrumb says where in the window the user was typing
// or clicking (role, label, enclosing labels). It carries labels only: the
// element's value is never read, and a secure field yields nothing at all.
const (
	// MaxFocusFieldBytes bounds each stored breadcrumb field after redaction.
	MaxFocusFieldBytes = 120
	// FocusReadBudget bounds the whole native breadcrumb read. A slower read is
	// abandoned and the capture proceeds without a breadcrumb.
	FocusReadBudget = 200 * time.Millisecond

	maxFocusAncestors = 3
	maxFocusNameRead  = 512
	focusPathSep      = " > "
)

// rawFocus is the unsanitized result of a native read. Ancestors are ordered
// outermost first.
type rawFocus struct {
	role      string
	name      string
	ancestors []string
}

// focusNode is the minimal accessibility element surface the breadcrumb reader
// may touch. It deliberately has no value accessor, and attribute is only ever
// called with AXSubrole, AXTitle or AXDescription.
type focusNode interface {
	role(context.Context) (string, error)
	attribute(ctx context.Context, name string, max int) (string, error)
	parent(context.Context) (focusNode, error)
	identity() uintptr
	release()
}

// readFocusBreadcrumb walks from the keyboard-focused element toward its
// window. It takes ownership of start and releases every node it visits.
// inWindow, when set, must accept the enclosing AXWindow or nothing is
// returned, so an element from a popover or another window is never attributed
// to the captured one. Any failure, a secure field or a cancelled context
// returns false and no partial data.
func readFocusBreadcrumb(ctx context.Context, start focusNode, inWindow func(focusNode) bool) (rawFocus, bool) {
	var empty rawFocus
	if start == nil {
		return empty, false
	}
	current := start
	defer func() {
		if current != nil {
			current.release()
		}
	}()
	if ctx.Err() != nil {
		return empty, false
	}
	role, err := current.role(ctx)
	if err != nil || !validAXRole(role) || isSecureTextRole(role) {
		return empty, false
	}
	if needsSecureSubroleCheck(role) {
		subrole, err := current.attribute(ctx, "AXSubrole", maxProfileRoleLen)
		if err != nil || isSecureTextRole(subrole) {
			return empty, false
		}
	}
	name, ok := focusName(ctx, current)
	if !ok {
		return empty, false
	}
	result := rawFocus{role: role, name: name}
	var nearest []string // innermost first
	windowMatched := inWindow == nil
	for hops := 0; hops < MaxCaptureDepth; hops++ {
		if ctx.Err() != nil {
			return empty, false
		}
		next, err := current.parent(ctx)
		if err != nil {
			return empty, false
		}
		current.release()
		current = nil
		if next == nil {
			break
		}
		current = next
		ancestorRole, err := current.role(ctx)
		if err != nil {
			return empty, false
		}
		if ancestorRole == "AXWindow" {
			// The window title is deliberately not read.
			if inWindow != nil {
				windowMatched = inWindow(current)
			}
			break
		}
		if ancestorRole == "AXApplication" {
			break
		}
		if len(nearest) < maxFocusAncestors {
			label, ok := focusName(ctx, current)
			if !ok {
				return empty, false
			}
			if label != "" {
				nearest = append(nearest, label)
			}
		}
	}
	if !windowMatched {
		return empty, false
	}
	for i := len(nearest) - 1; i >= 0; i-- {
		result.ancestors = append(result.ancestors, nearest[i])
	}
	return result, true
}

// focusName is the element title, else its description.
func focusName(ctx context.Context, node focusNode) (string, bool) {
	title, err := node.attribute(ctx, "AXTitle", maxFocusNameRead)
	if err != nil {
		return "", false
	}
	if strings.TrimSpace(title) != "" {
		return title, true
	}
	description, err := node.attribute(ctx, "AXDescription", maxFocusNameRead)
	if err != nil {
		return "", false
	}
	return description, true
}

// sanitizeFocus applies the same redactor the captured text gets, then bounds
// each field. Output is a fixed point of the redactor so Validate accepts it.
func sanitizeFocus(raw rawFocus) (role, name, path string) {
	if validAXRole(raw.role) {
		role = raw.role
	}
	name = sanitizeFocusText(raw.name)
	parts := make([]string, 0, len(raw.ancestors))
	for _, ancestor := range raw.ancestors {
		if cleaned := cleanFocusText(ancestor); cleaned != "" {
			parts = append(parts, cleaned)
		}
	}
	path = sanitizeFocusText(strings.Join(parts, focusPathSep))
	return role, name, path
}

// cleanFocusText rejects invalid UTF-8 and NUL, and flattens other control
// characters and runs of whitespace so a label stays on one line.
func cleanFocusText(value string) string {
	if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return ""
	}
	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, value)), " ")
}

// sanitizeFocusText returns a field that is a fixed point of the redactor and
// at most MaxFocusFieldBytes, or "" when no such value can be reached within a
// bounded number of passes. Redaction can expose new matches (a removed token
// can join its neighbours), so it repeats until stable; truncation can bisect
// a placeholder, so each truncation is followed by another stability check.
func sanitizeFocusText(value string) string {
	value = cleanFocusText(value)
	for pass := 0; pass < 8 && value != ""; pass++ {
		redacted, _ := privacy.Redact(value)
		redacted, _ = truncateUTF8(redacted, MaxFocusFieldBytes)
		redacted = strings.TrimSpace(redacted)
		if redacted == value && validFocusFields("", value, "") {
			return value
		}
		value = redacted
	}
	return ""
}

// WithFocus returns the observation with a focused-element breadcrumb. The
// content ID is unchanged: the breadcrumb describes where the text was seen,
// not what the text is. Fields are redacted and bounded here.
func (o SanitizedObservation) WithFocus(role, name, path string) SanitizedObservation {
	out := o.Clone()
	out.focusedRole, out.focusedName, out.focusedPath = "", "", ""
	if validAXRole(role) {
		out.focusedRole = role
	}
	out.focusedName = sanitizeFocusText(name)
	out.focusedPath = sanitizeFocusText(path)
	return out
}

func (o SanitizedObservation) withFocusRaw(role, name, path string) SanitizedObservation {
	out := o.Clone()
	out.focusedRole, out.focusedName, out.focusedPath = role, name, path
	return out
}

func (o SanitizedObservation) FocusedRole() string { return strings.Clone(o.focusedRole) }
func (o SanitizedObservation) FocusedName() string { return strings.Clone(o.focusedName) }
func (o SanitizedObservation) FocusedPath() string { return strings.Clone(o.focusedPath) }

func (o SanitizedObservation) hasFocus() bool {
	return o.focusedRole != "" || o.focusedName != "" || o.focusedPath != ""
}

func validFocusFields(role, name, path string) bool {
	if role != "" && !validAXRole(role) {
		return false
	}
	for _, field := range []string{name, path} {
		if len(field) > MaxFocusFieldBytes || !utf8.ValidString(field) || strings.ContainsRune(field, 0) {
			return false
		}
		if redacted, _ := privacy.Redact(field); redacted != field {
			return false
		}
	}
	return true
}

// readFocusForCapture runs the optional native reader under its own budget.
// It never fails the capture: a missing, failed or late result is empty. The
// reader is called synchronously because it borrows native elements that the
// frame releases after capture; every native call inside it is individually
// bounded by the same deadline (see setAXTimeout), so it cannot run long.
func readFocusForCapture(ctx context.Context, read func(context.Context) (rawFocus, bool)) (role, name, path string) {
	if read == nil || ctx.Err() != nil {
		return "", "", ""
	}
	focusCtx, cancel := context.WithTimeout(ctx, FocusReadBudget)
	defer cancel()
	raw, ok := safeFocusRead(focusCtx, read)
	if !ok || focusCtx.Err() != nil {
		return "", "", ""
	}
	return sanitizeFocus(raw)
}

func safeFocusRead(ctx context.Context, read func(context.Context) (rawFocus, bool)) (raw rawFocus, ok bool) {
	defer func() {
		if recover() != nil {
			raw, ok = rawFocus{}, false
		}
	}()
	return read(ctx)
}
