package windows

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/appidentity"
	"github.com/hazcod/blur/internal/browserprivacy"
	"github.com/hazcod/blur/internal/platform"
)

const captureEvidence = "ui_automation"

var _ platform.CaptureSource = (*Adapter)(nil)
var _ platform.CaptureSuspender = (*Adapter)(nil)

type captureBackend interface {
	Capability(context.Context) (platform.CapabilityReport, error)
	Foreground(context.Context) (FocusedTarget, error)
	Subscribe(context.Context) (eventSubscription, error)
}

// FocusedTarget is a borrowed, short-lived native identity. Never persist,
// serialize, or log it; WithVerifiedTarget supplies it only during one read.
type FocusedTarget struct {
	Window       uintptr
	ProcessID    uint32
	AppID        string
	CreationTime uint64
	Elevated     bool
	Secure       bool
	Private      bool
}

type eventSubscription struct {
	events <-chan platform.CaptureEvent
	stop   func()
	done   <-chan struct{}
}

type targetLease struct {
	id     string
	target FocusedTarget
}

// Adapter is the policy layer for Windows foreground capture. Native UIA and
// WinEvent details are supplied by captureBackend; tests inject a deterministic
// backend so lease and pause behavior can be exercised off Windows.
type Adapter struct {
	backend captureBackend
	mu      sync.Mutex
	lease   targetLease
	sub     *eventSubscription
}

func newAdapter(backend captureBackend) *Adapter { return &Adapter{backend: backend} }

func (a *Adapter) Capability(ctx context.Context) (platform.CapabilityReport, error) {
	report := unavailableReport()
	if ctx == nil {
		return report, platform.ErrInvalidCapabilityReport
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if a == nil || a.backend == nil {
		return report, nil
	}
	got, err := a.backend.Capability(ctx)
	if err != nil || got.Validate() != nil || got.ID != "capture" || got.Evidence != captureEvidence {
		return report, nil
	}
	return got, nil
}

func (a *Adapter) Probe(ctx context.Context) (platform.CaptureProbe, error) {
	if ctx == nil {
		return platform.CaptureProbe{}, platform.ErrInvalidLease
	}
	if err := ctx.Err(); err != nil {
		return platform.CaptureProbe{}, err
	}
	report, err := a.Capability(ctx)
	if err != nil {
		return platform.CaptureProbe{}, err
	}
	if a == nil || a.backend == nil || report.State != platform.Available {
		return platform.CaptureProbe{}, &platform.UnavailableError{Report: report}
	}
	target, err := a.backend.Foreground(ctx)
	if err != nil || !validFocusedTarget(target) {
		if err := ctx.Err(); err != nil {
			return platform.CaptureProbe{}, err
		}
		return platform.CaptureProbe{}, platform.ErrInvalidLease
	}
	id, err := newTargetID()
	if err != nil {
		return platform.CaptureProbe{}, err
	}
	a.mu.Lock()
	a.lease = targetLease{id: id, target: target}
	a.mu.Unlock()
	return platform.CaptureProbe{
		TargetID: id,
		AppID:    target.AppID,
		Private:  target.Private,
		Secure:   target.Secure,
		Elevated: target.Elevated,
	}, nil
}

func (a *Adapter) WithVerifiedTarget(ctx context.Context, targetID string, read func(context.Context, FocusedTarget) error) error {
	if a == nil || a.backend == nil || ctx == nil || read == nil {
		return platform.ErrInvalidLease
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	lease := a.lease
	a.mu.Unlock()
	if targetID == "" || targetID != lease.id || lease.id == "" || lease.target.Elevated || lease.target.Secure || lease.target.Private {
		return platform.ErrInvalidLease
	}
	current, err := a.backend.Foreground(ctx)
	if err != nil || current != lease.target {
		return platform.ErrInvalidLease
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	readErr := read(ctx, lease.target)
	if err := ctx.Err(); err != nil {
		return err
	}
	current, err = a.backend.Foreground(ctx)
	if err != nil || current != lease.target {
		return platform.ErrInvalidLease
	}
	a.mu.Lock()
	stillCurrent := a.lease == lease
	a.mu.Unlock()
	if !stillCurrent {
		return platform.ErrInvalidLease
	}
	return readErr
}

func (a *Adapter) Next(ctx context.Context) (platform.CaptureEvent, error) {
	if a == nil || a.backend == nil || ctx == nil {
		return platform.CaptureEvent{}, platform.ErrInvalidLease
	}
	if err := ctx.Err(); err != nil {
		return platform.CaptureEvent{}, err
	}
	report, err := a.Capability(ctx)
	if err != nil {
		return platform.CaptureEvent{}, err
	}
	if report.State != platform.Available {
		return platform.CaptureEvent{}, &platform.UnavailableError{Report: report}
	}
	for {
		sub, err := a.ensureSubscription(ctx)
		if err != nil {
			return platform.CaptureEvent{}, err
		}
		select {
		case <-ctx.Done():
			return platform.CaptureEvent{}, ctx.Err()
		case <-sub.done:
			a.clearSubscription(sub)
			return platform.CaptureEvent{}, errors.New("windows: capture events stopped")
		case event, ok := <-sub.events:
			if !ok {
				a.clearSubscription(sub)
				return platform.CaptureEvent{}, errors.New("windows: capture events stopped")
			}
			if !validEventKind(event.Kind) {
				continue
			}
			for {
				select {
				case next, ok := <-sub.events:
					if !ok {
						return platform.CaptureEvent{}, errors.New("windows: capture events stopped")
					}
					if validEventKind(next.Kind) {
						event = next
					}
				default:
					return platform.CaptureEvent{Kind: event.Kind}, nil
				}
			}
		}
	}
}

func (a *Adapter) ensureSubscription(ctx context.Context) (*eventSubscription, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sub != nil {
		return a.sub, nil
	}
	sub, err := a.backend.Subscribe(ctx)
	if err != nil || sub.events == nil || sub.done == nil || sub.stop == nil {
		return nil, errors.New("windows: capture events unavailable")
	}
	a.sub = &sub
	return a.sub, nil
}

func (a *Adapter) clearSubscription(sub *eventSubscription) {
	a.mu.Lock()
	if a.sub == sub {
		a.sub = nil
	}
	a.mu.Unlock()
}

// Suspend removes native foreground/focus hooks and waits for the hook thread
// to exit. A subsequent Next call may subscribe again after capture resumes.
func (a *Adapter) Suspend(ctx context.Context) error {
	if ctx == nil {
		return platform.ErrInvalidLease
	}
	if a == nil {
		return nil
	}
	a.mu.Lock()
	sub := a.sub
	a.sub = nil
	a.lease = targetLease{}
	a.mu.Unlock()
	if sub == nil {
		return nil
	}
	sub.stop()
	select {
	case <-sub.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func validFocusedTarget(target FocusedTarget) bool {
	if target.Window == 0 || target.ProcessID == 0 || target.CreationTime == 0 || len(target.AppID) == 0 || len(target.AppID) > 128 {
		return false
	}
	for _, r := range target.AppID {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '.' && r != '-' && r != '_' {
			return false
		}
	}
	return target.AppID == strings.ToLower(strings.TrimSpace(target.AppID))
}

func isPrivateBrowserWindow(appID, title string, titleErr error) bool {
	if !appidentity.IsBrowserApp(appID) {
		return false
	}
	if titleErr != nil || len(title) > 512 || !utf8.ValidString(title) {
		return true
	}
	return browserprivacy.IsPrivateWindowTitle(title)
}

func decodeForegroundWindowTitle(buffer []uint16, read int) (string, error) {
	if read < 1 || read >= 512 || read > len(buffer) {
		return "", errors.New("windows: foreground title unavailable")
	}
	for i := 0; i < read; i++ {
		unit := buffer[i]
		switch {
		case unit >= 0xD800 && unit <= 0xDBFF:
			if i+1 >= read || buffer[i+1] < 0xDC00 || buffer[i+1] > 0xDFFF {
				return "", errors.New("windows: foreground title unavailable")
			}
			i++
		case unit >= 0xDC00 && unit <= 0xDFFF:
			return "", errors.New("windows: foreground title unavailable")
		}
	}
	return string(utf16.Decode(buffer[:read])), nil
}

func executableAppID(path string) string {
	name := path
	if separator := strings.LastIndexAny(name, `/\\`); separator >= 0 {
		name = name[separator+1:]
	}
	name = strings.ToLower(strings.TrimSpace(name))
	var normalized strings.Builder
	normalized.Grow(min(len(name), 128))
	for _, r := range name {
		if normalized.Len() >= 128 {
			break
		}
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_' {
			normalized.WriteRune(r)
		} else {
			normalized.WriteByte('_')
		}
	}
	return strings.Trim(normalized.String(), "._-")
}

func validEventKind(kind string) bool {
	return kind == "frontmost_app_changed" || kind == "focused_context_changed"
}

func newTargetID() (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", errors.New("windows: target lease unavailable")
	}
	return "windows:" + hex.EncodeToString(nonce[:]), nil
}

func validTargetID(id string) bool {
	if len(id) != len("windows:")+32 || !strings.HasPrefix(id, "windows:") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(id, "windows:"))
	return err == nil
}

func unavailableReport() platform.CapabilityReport {
	return platform.CapabilityReport{ID: "capture", State: platform.TemporarilyUnresponsive, Reason: "service_unavailable", Evidence: captureEvidence}
}

func windowsCaptureCapability(ctx context.Context) (platform.CapabilityReport, error) {
	available := platform.CapabilityReport{ID: "capture", State: platform.Available, Reason: "available", Evidence: captureEvidence}
	if ctx == nil {
		return unavailableReport(), platform.ErrInvalidCapabilityReport
	}
	if err := ctx.Err(); err != nil {
		return available, err
	}
	return available, nil
}
