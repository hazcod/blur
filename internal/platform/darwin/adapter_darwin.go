package darwin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"runtime"
	"strings"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
	"github.com/hazcod/blur/internal/appidentity"
	"github.com/hazcod/blur/internal/platform"
)

var _ platform.CaptureSource = (*Adapter)(nil)
var _ platform.CaptureSuspender = (*Adapter)(nil)
var _ platform.CapturePermissionRequester = (*Adapter)(nil)

// Adapter provides passive Darwin accessibility capture. Capability is checked
// non-interactively; explicit capture startup may request native permission guidance.
type Adapter struct {
	trusted             func() (bool, error)
	requestPermission   func() error
	currentTarget       func() (string, int32, error)
	subscribeActivation func(chan<- struct{}) (func(), error)
	watchFocus          func(int32, chan<- struct{}) (func(), error)
	targetMu            sync.Mutex
	currentID           string
	currentApp          string
	currentPID          int32
	eventMu             sync.Mutex
	events              chan struct{}
	eventDone           chan struct{}
	stopEvents          func()
	eventStarted        bool
	closed              bool
}

func New() *Adapter {
	return &Adapter{
		trusted: nativeAccessibilityTrusted, requestPermission: nativeAccessibilityPrompt, currentTarget: nativeFrontmostTarget,
		subscribeActivation: nativeActivationSubscription, watchFocus: nativeFocusSubscription,
	}
}

// RequestPermission asks macOS to present its own permission guidance without
// changing the user's TCC decision.
func (a *Adapter) RequestPermission(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a == nil || a.requestPermission == nil {
		return errors.New("darwin: accessibility permission guidance unavailable")
	}
	return a.requestPermission()
}

func (a *Adapter) Capability(ctx context.Context) (platform.CapabilityReport, error) {
	report := platform.CapabilityReport{
		ID: "capture", State: platform.Available,
		Reason: "available", Evidence: "axui_element",
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if a == nil || a.trusted == nil {
		report.State, report.Reason = platform.TemporarilyUnresponsive, "temporarily_unresponsive"
		return report, nil
	}
	trusted, err := a.trusted()
	if err != nil {
		report.State, report.Reason = platform.TemporarilyUnresponsive, "temporarily_unresponsive"
		return report, nil
	}
	if !trusted {
		report.State, report.Reason = platform.PermissionRequired, "permission_required"
	}
	return report, nil
}

func (a *Adapter) Probe(ctx context.Context) (platform.CaptureProbe, error) {
	var empty platform.CaptureProbe
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	trusted, err := a.trustedStatus()
	if err != nil || !trusted {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return empty, ctxErr
		}
		report, _ := a.Capability(ctx)
		return empty, &platform.UnavailableError{Report: report}
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if a.currentTarget == nil {
		return empty, errors.New("darwin: target probe unavailable")
	}
	appID, pid, err := a.currentTarget()
	if err != nil || pid <= 0 {
		return empty, errors.New("darwin: target probe failed")
	}
	appID = strings.ToLower(strings.TrimSpace(appID))
	if !validBundleID(appID) {
		return empty, errors.New("darwin: target probe failed")
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return empty, errors.New("darwin: target identity unavailable")
	}
	targetID := "darwin:" + hex.EncodeToString(nonce[:])
	a.targetMu.Lock()
	a.currentID, a.currentApp, a.currentPID = targetID, appID, pid
	a.targetMu.Unlock()
	return platform.CaptureProbe{
		TargetID: targetID,
		AppID:    appID,
		Private:  appID == appidentity.BundleID,
	}, nil
}

// ActiveApplicationID returns only the most recently probed application ID.
// It does not make a new OS query and is intended for capture-state UI.
func (a *Adapter) ActiveApplicationID() (string, bool) {
	if a == nil {
		return "", false
	}
	a.targetMu.Lock()
	defer a.targetMu.Unlock()
	return a.currentApp, a.currentApp != ""
}

// VerifyTarget revalidates the opaque target token against the current
// frontmost process immediately before a bounded read. No PID leaves the
// adapter, and a later Probe invalidates every earlier token.
func (a *Adapter) VerifyTarget(ctx context.Context, targetID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a == nil || a.currentTarget == nil {
		return platform.ErrInvalidLease
	}
	a.targetMu.Lock()
	valid := targetID != "" && targetID == a.currentID
	wantApp, wantPID := a.currentApp, a.currentPID
	a.targetMu.Unlock()
	if !valid {
		return platform.ErrInvalidLease
	}
	appID, pid, err := a.currentTarget()
	if err != nil {
		return platform.ErrInvalidLease
	}
	appID = strings.ToLower(strings.TrimSpace(appID))
	if err := ctx.Err(); err != nil {
		return err
	}
	if appID != wantApp || pid != wantPID {
		return platform.ErrInvalidLease
	}
	return nil
}

// WithVerifiedTarget gives a capture reader the PID only for the duration of
// one read. It checks focus before and after the callback; callers must discard
// the read if revalidation fails and must not persist the PID.
func (a *Adapter) WithVerifiedTarget(ctx context.Context, targetID string, read func(context.Context, int32) error) error {
	if read == nil {
		return platform.ErrInvalidLease
	}
	if err := a.VerifyTarget(ctx, targetID); err != nil {
		return err
	}
	a.targetMu.Lock()
	if a.currentID != targetID || a.currentPID <= 0 {
		a.targetMu.Unlock()
		return platform.ErrInvalidLease
	}
	pid := a.currentPID
	a.targetMu.Unlock()
	readErr := read(ctx, pid)
	verifyErr := a.VerifyTarget(ctx, targetID)
	if verifyErr != nil {
		return verifyErr
	}
	return readErr
}

func (a *Adapter) Next(ctx context.Context) (platform.CaptureEvent, error) {
	var empty platform.CaptureEvent
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if err := a.ensureActivationEvents(); err != nil {
		return empty, err
	}
	a.eventMu.Lock()
	events, done := a.events, a.eventDone
	a.eventMu.Unlock()
	var focusEvents <-chan struct{}
	var stopFocus func()
	if probe, probeErr := a.Probe(ctx); probeErr == nil && !probe.Private {
		a.targetMu.Lock()
		pid := a.currentPID
		a.targetMu.Unlock()
		watch := a.watchFocus
		if watch == nil {
			watch = nativeFocusSubscription
		}
		focus := make(chan struct{}, 1)
		if stop, watchErr := watch(pid, focus); watchErr == nil {
			focusEvents, stopFocus = focus, stop
		}
	}
	if stopFocus != nil {
		defer stopFocus()
	}
	select {
	case <-ctx.Done():
		return empty, ctx.Err()
	case <-done:
		return empty, errors.New("darwin: capture source closed")
	case <-events:
		a.eventMu.Lock()
		closed := a.closed
		a.eventMu.Unlock()
		if closed {
			return empty, errors.New("darwin: capture source closed")
		}
		return platform.CaptureEvent{Kind: "frontmost_app_changed"}, nil
	case <-focusEvents:
		return platform.CaptureEvent{Kind: "focused_context_changed"}, nil
	}
}

func (a *Adapter) trustedStatus() (bool, error) {
	if a == nil || a.trusted == nil {
		return false, errors.New("permission probe unavailable")
	}
	return a.trusted()
}

func validBundleID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '.' && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

var axPermission struct {
	once  sync.Once
	check func() uint8
	err   error
}

func nativeAccessibilityTrusted() (bool, error) {
	axPermission.once.Do(func() {
		h, err := purego.Dlopen("/System/Library/Frameworks/ApplicationServices.framework/ApplicationServices", purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			axPermission.err = err
			return
		}
		// AXIsProcessTrusted returns Apple's Boolean (UInt8), not C _Bool.
		// Bind the ABI width explicitly so a granted TCC result cannot be
		// misread through Go's bool return convention.
		var check func() uint8
		purego.RegisterLibFunc(&check, h, "AXIsProcessTrusted")
		axPermission.check = check
	})
	if axPermission.err != nil || axPermission.check == nil {
		return false, axPermission.err
	}
	return accessibilityTrustedValue(axPermission.check()), nil
}

func accessibilityTrustedValue(value uint8) bool { return value != 0 }

type frontmostAPI struct {
	objcHandle   uintptr
	appKitHandle uintptr
	cfHandle     uintptr
	getClass     func(uintptr) uintptr
	selector     func(uintptr) uintptr
	message      func(uintptr, uintptr) uintptr
	messageInt   func(uintptr, uintptr) int32
	stringCopy   func(uintptr, uintptr, int64, uint32) uint8
}

var frontmost struct {
	once sync.Once
	api  frontmostAPI
	err  error
}

const cfUTF8Encoding = 0x08000100

func nativeFrontmostTarget() (string, int32, error) {
	frontmost.once.Do(loadFrontmostAPI)
	if frontmost.err != nil {
		return "", 0, errors.New("frontmost app API unavailable")
	}
	return queryFrontmostTargetOnMainThread(dispatchFrontmostQueryToMain, readFrontmostTarget)
}

func queryFrontmostTargetOnMainThread(dispatch func(func()) error, read func() (string, int32, error)) (string, int32, error) {
	if dispatch == nil || read == nil {
		return "", 0, errors.New("frontmost app main-thread dispatcher unavailable")
	}
	var appID string
	var pid int32
	var queryErr error
	done := make(chan struct{})
	if err := dispatch(func() {
		appID, pid, queryErr = read()
		close(done)
	}); err != nil {
		return "", 0, err
	}
	select {
	case <-done:
		return appID, pid, queryErr
	default:
		return "", 0, errors.New("frontmost app dispatcher did not complete synchronously")
	}
}

func readFrontmostTarget() (string, int32, error) {
	a := frontmost.api
	poolClass := a.getClassName("NSAutoreleasePool")
	pool := a.sendObject(poolClass, "alloc")
	pool = a.sendObject(pool, "init")
	if pool == 0 {
		return "", 0, errors.New("frontmost app unavailable")
	}
	defer a.sendObject(pool, "drain")
	class := a.getClassName("NSWorkspace")
	workspace := a.sendObject(class, "sharedWorkspace")
	application := a.sendObject(workspace, "frontmostApplication")
	if application == 0 {
		return "", 0, errors.New("frontmost app unavailable")
	}
	bundle := a.sendObject(application, "bundleIdentifier")
	if bundle == 0 {
		return "", 0, errors.New("frontmost app identifier unavailable")
	}
	var output [129]byte
	if a.stringCopy(bundle, uintptr(unsafe.Pointer(&output[0])), int64(len(output)), cfUTF8Encoding) == 0 {
		return "", 0, errors.New("frontmost app identifier unavailable")
	}
	n := 0
	for n < len(output) && output[n] != 0 {
		n++
	}
	if n == len(output) {
		return "", 0, errors.New("frontmost app identifier invalid")
	}
	pid := a.sendInteger(application, "processIdentifier")
	return string(output[:n]), pid, nil
}

var frontmostDispatcher struct {
	once      sync.Once
	callMu    sync.Mutex
	pendingMu sync.Mutex
	pending   func()
	target    objc.ID
	selector  objc.SEL
	err       error
}

func dispatchFrontmostQueryToMain(run func()) error {
	if run == nil {
		return errors.New("frontmost app query missing")
	}
	return withPinnedOSThread(runtime.LockOSThread, runtime.UnlockOSThread, func() error {
		return dispatchFrontmostQueryToMainPinned(run)
	})
}

func withFrontmostOSThread(lock, unlock func(), run func() error) error {
	return withPinnedOSThread(lock, unlock, run)
}

func withPinnedOSThread(lock, unlock func(), run func() error) error {
	lock()
	defer unlock()
	return run()
}

func dispatchFrontmostQueryToMainPinned(run func()) error {
	frontmostDispatcher.callMu.Lock()
	defer frontmostDispatcher.callMu.Unlock()
	frontmostDispatcher.once.Do(initFrontmostDispatcher)
	if frontmostDispatcher.err != nil || frontmostDispatcher.target == 0 {
		return errors.New("frontmost app main-thread dispatcher unavailable")
	}
	frontmostDispatcher.pendingMu.Lock()
	frontmostDispatcher.pending = run
	frontmostDispatcher.pendingMu.Unlock()
	defer func() {
		frontmostDispatcher.pendingMu.Lock()
		frontmostDispatcher.pending = nil
		frontmostDispatcher.pendingMu.Unlock()
	}()

	// NSObject's synchronous main-thread selector preserves AppKit thread
	// affinity while allowing the passive capture worker to request a fresh
	// frontmost target. Keep this goroutine pinned for the full autorelease-pool
	// lifetime: Cocoa pools are thread-local and draining from another runtime
	// thread can corrupt memory.
	poolClass := objc.GetClass("NSAutoreleasePool")
	pool := objc.ID(poolClass).Send(objc.RegisterName("alloc")).Send(objc.RegisterName("init"))
	if pool == 0 {
		return errors.New("frontmost app autorelease pool unavailable")
	}
	defer pool.Send(objc.RegisterName("drain"))
	objc.Send[objc.ID](frontmostDispatcher.target, objc.RegisterName("performSelectorOnMainThread:withObject:waitUntilDone:"), frontmostDispatcher.selector, objc.ID(0), true)
	return nil
}

func initFrontmostDispatcher() {
	class, err := objc.RegisterClass("BlurFrontmostQueryTarget", objc.GetClass("NSObject"), nil, nil, []objc.MethodDef{{
		Cmd: objc.RegisterName("blurRunFrontmostQuery:"),
		Fn: func(_ objc.ID, _ objc.SEL, _ objc.ID) {
			frontmostDispatcher.pendingMu.Lock()
			run := frontmostDispatcher.pending
			frontmostDispatcher.pendingMu.Unlock()
			if run != nil {
				run()
			}
		},
	}})
	if err != nil || class == 0 {
		frontmostDispatcher.err = err
		return
	}
	frontmostDispatcher.selector = objc.RegisterName("blurRunFrontmostQuery:")
	frontmostDispatcher.target = objc.Send[objc.ID](objc.Send[objc.ID](objc.ID(class), objc.RegisterName("alloc")), objc.RegisterName("init"))
	if frontmostDispatcher.target == 0 {
		frontmostDispatcher.err = errors.New("frontmost app dispatcher object unavailable")
	}
}

func loadFrontmostAPI() {
	objc, err := purego.Dlopen("/usr/lib/libobjc.A.dylib", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		frontmost.err = err
		return
	}
	appKit, err := purego.Dlopen("/System/Library/Frameworks/AppKit.framework/AppKit", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		frontmost.err = err
		return
	}
	cf, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		frontmost.err = err
		return
	}
	a := frontmostAPI{objcHandle: objc, appKitHandle: appKit, cfHandle: cf}
	purego.RegisterLibFunc(&a.getClass, objc, "objc_getClass")
	purego.RegisterLibFunc(&a.selector, objc, "sel_registerName")
	purego.RegisterLibFunc(&a.message, objc, "objc_msgSend")
	purego.RegisterLibFunc(&a.messageInt, objc, "objc_msgSend")
	purego.RegisterLibFunc(&a.stringCopy, cf, "CFStringGetCString")
	frontmost.api = a
}

func cString(s string) ([]byte, uintptr) {
	b := append([]byte(s), 0)
	return b, uintptr(unsafe.Pointer(&b[0]))
}

func (a frontmostAPI) getClassName(name string) uintptr {
	b, pointer := cString(name)
	value := a.getClass(pointer)
	runtime.KeepAlive(b)
	return value
}

func (a frontmostAPI) selectorName(name string) uintptr {
	b, pointer := cString(name)
	value := a.selector(pointer)
	runtime.KeepAlive(b)
	return value
}

func (a frontmostAPI) sendObject(receiver uintptr, name string) uintptr {
	selector := a.selectorName(name)
	return a.message(receiver, selector)
}

func (a frontmostAPI) sendInteger(receiver uintptr, name string) int32 {
	selector := a.selectorName(name)
	return a.messageInt(receiver, selector)
}
