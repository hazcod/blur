package capture

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"

	"github.com/ebitengine/purego"
	nativeffi "github.com/goexlib/cgo"
	"github.com/hazcod/blur/internal/appidentity"
	"github.com/hazcod/blur/internal/platform"
	"github.com/hazcod/blur/internal/platform/darwin"
)

const (
	axSuccess                  = 0
	axAttributeUnsupported     = -25205
	axParameterizedUnsupported = -25213
	axNoValue                  = -25212
	axCannotComplete           = -25204
	axMessagingTimeout         = 0.2
	cfUTF8                     = 0x08000100
)

type nativeAXAPI struct {
	appServices        uintptr
	core               uintptr
	createApp          func(int32) uintptr
	copyValue          func(uintptr, uintptr, *uintptr) int32
	copyNames          func(uintptr, *uintptr) int32
	parameterizedValue func(uintptr, uintptr, uintptr, *uintptr) int32
	valueCreate        func(int32, *nativeTextRange) uintptr
	valueType          func() uintptr
	valueKind          func(uintptr) int32
	valueGetRange      func(uintptr, int32, *nativeTextRange) uint8
	valueGetPair       func(uintptr, int32, *nativeAXPair) uint8
	secureInput        func() bool
	numberType         func() uintptr
	numberGetInt64     func(uintptr, int32, *int64) uint8
	attributedType     func() uintptr
	attributedString   func(uintptr) uintptr
	setValue           func(uintptr, uintptr, uintptr) int32
	trueValue          uintptr
	valueCount         func(uintptr, uintptr, *int64) int32
	copyValues         func(uintptr, uintptr, int64, int64, *uintptr) int32
	setTimeout         func(uintptr, float32) int32
	getTypeID          func(uintptr) uintptr
	stringType         func() uintptr
	urlType            func() uintptr
	urlString          func(uintptr) uintptr
	booleanType        func() uintptr
	booleanValue       func(uintptr) uint8
	arrayType          func() uintptr
	arrayCount         func(uintptr) int64
	arrayValue         func(uintptr, int64) uintptr
	stringLen          func(uintptr) int64
	stringCharacter    func(uintptr, int64) uint16
	stringCString      func(uintptr, uintptr, int64, uint32) uint8
	stringNew          func(uintptr, uintptr, uint32) uintptr
	stringCopy         func(uintptr, uintptr) uintptr
	retain             func(uintptr) uintptr
	release            func(uintptr)
	equal              func(uintptr, uintptr) bool
}

var nativeAX struct {
	once sync.Once
	api  nativeAXAPI
	err  error
}

type nativeAXNode struct {
	api            nativeAXAPI
	element        uintptr
	closed         atomic.Bool
	guardSensitive atomic.Bool
}

// NewDarwinPipeline installs the native AX reader behind the common capture
// pipeline. Raw AX text is converted only by bounded value callbacks after
// structure, profiles, and exclusions have passed preflight.
func NewDarwinPipeline(source *darwin.Adapter, limits Limits, exclusions Exclusions, profiles []AppProfile) Pipeline {
	profileCopy := make([]AppProfile, len(profiles))
	for i, profile := range profiles {
		profileCopy[i] = AppProfile{AppID: strings.Clone(profile.AppID), IgnoreRoles: append([]string(nil), profile.IgnoreRoles...)}
	}
	exclusions = cloneExclusions(exclusions)
	return Pipeline{
		Source: source, Limits: limits, Exclusions: exclusions, Profiles: profileCopy,
		Read: func(ctx context.Context, probe platform.CaptureProbe, limits Limits) (rawFrame, error) {
			return readDarwinAX(ctx, source, probe, limits)
		},
	}
}

func readDarwinAX(ctx context.Context, source *darwin.Adapter, probe platform.CaptureProbe, limits Limits) (rawFrame, error) {
	var frame rawFrame
	stage := ReadStageApplication
	if source == nil {
		return frame, NativeReadError(ReadStageSetup, 0)
	}
	nativeAX.once.Do(loadNativeAXAPI)
	if nativeAX.err != nil {
		return frame, NativeReadError(ReadStageSetup, 0)
	}
	err := source.WithVerifiedTarget(ctx, probe.TargetID, func(ctx context.Context, pid int32) error {
		if manualAXRequests.admit(pid, time.Now()) {
			requestManualAccessibility(ctx, nativeAX.api, pid)
		}
		stage = ReadStageWindow
		window, err := focusedWindow(ctx, nativeAX.api, pid)
		if err != nil {
			return err
		}
		root := &nativeAXNode{api: nativeAX.api, element: window}
		browser := appidentity.IsBrowserApp(probe.AppID)
		var page *browserPageLease
		documentDomain, documentDomainKnown := "", false
		if browser {
			stage = ReadStageSensitivity
			if err := validateBrowserWindow(ctx, nativeAX.api, window); err != nil {
				root.release()
				return errors.New("capture: browser privacy state unavailable")
			}
			stage = ReadStageBrowserPage
			page, err = findBrowserPage(ctx, root, limits)
			if err != nil {
				root.release()
				return err
			}
			stage = ReadStageBrowserDomain
			documentDomain, documentDomainKnown, err = page.domain(ctx)
			if err != nil {
				page.release()
				root.release()
				return err
			}
		}
		var content axTreeNode = root
		if page != nil {
			content = page.takeRoot()
		}
		stage = ReadStageStructure
		frame, err = collectAXFrame(ctx, content, limits)
		if err != nil {
			if page != nil {
				page.release()
				root.release()
			}
			return err
		}
		if page != nil {
			cleanup := frame.cleanup
			frame.cleanup = func() { cleanup(); page.release(); root.release() }
			frame.nativeRootID = window
			frame.validate = func(ctx context.Context) error {
				return contextualReadError(ReadStageValidation, source.WithVerifiedTarget(ctx, probe.TargetID, func(ctx context.Context, pid int32) error {
					if err := validateFocusedWindow(ctx, nativeAX.api, pid, window); err != nil {
						return err
					}
					if err := validateBrowserWindow(ctx, nativeAX.api, window); err != nil {
						return err
					}
					return page.validate(ctx)
				}))
			}
		}
		if frame.validate == nil {
			frame.validate = func(ctx context.Context) error {
				return contextualReadError(ReadStageValidation, source.WithVerifiedTarget(ctx, probe.TargetID, func(ctx context.Context, pid int32) error {
					return validateFocusedWindow(ctx, nativeAX.api, pid, window)
				}))
			}
		}
		frame.domain, frame.domainKnown = documentDomain, documentDomainKnown
		if browser {
			frame.validateDomain = func(ctx context.Context, expected string) error {
				return contextualReadError(ReadStageBrowserDomain, source.WithVerifiedTarget(ctx, probe.TargetID, func(ctx context.Context, targetPID int32) error {
					if err := validateFocusedWindow(ctx, nativeAX.api, targetPID, window); err != nil {
						return err
					}
					current, known, err := page.domain(ctx)
					if err != nil {
						return err
					}
					if !known || current != expected {
						return errCaptureDomainChanged
					}
					return nil
				}))
			}
			if documentDomainKnown && documentDomain != "" {
				frame.browserRegion = func(ctx context.Context, candidate darwin.ScreenTextWindow) (darwin.ScreenTextRect, error) {
					var region darwin.ScreenTextRect
					err := source.WithVerifiedTarget(ctx, probe.TargetID, func(ctx context.Context, targetPID int32) error {
						var err error
						region, err = verifyBrowserVisualRegion(ctx, nativeAX.api, targetPID, probe.AppID, window, page, documentDomain, candidate, limits)
						return err
					})
					return region, err
				}
			}
		}
		stage = ReadStageValidation
		return validateFocusedWindow(ctx, nativeAX.api, pid, window)
	})
	if err != nil {
		return frame, contextualReadError(stage, err)
	}
	// The focused-window proxy is owned by the first frame node. Its identity is
	// captured separately for per-value revalidation without exposing a title.
	rootElement := frame.nativeRootID
	frame.focus = darwinFocusReader(source, probe, rootElement)
	wrapAXValues(frame.roots, func(ctx context.Context, max int, value func(context.Context, int) (string, error)) (string, error) {
		var text string
		verifyErr := source.WithVerifiedTarget(ctx, probe.TargetID, func(ctx context.Context, pid int32) error {
			if err := validateFocusedWindow(ctx, nativeAX.api, pid, rootElement); err != nil {
				return err
			}
			if appidentity.IsBrowserApp(probe.AppID) {
				if err := validateBrowserWindow(ctx, nativeAX.api, rootElement); err != nil {
					return err
				}
			}
			var err error
			text, err = value(ctx, max)
			if err != nil && !errors.Is(err, errAXValueTruncated) {
				return err
			}
			if focusErr := validateFocusedWindow(ctx, nativeAX.api, pid, rootElement); focusErr != nil {
				return focusErr
			}
			if appidentity.IsBrowserApp(probe.AppID) {
				if privacyErr := validateBrowserWindow(ctx, nativeAX.api, rootElement); privacyErr != nil {
					return privacyErr
				}
			}
			return err
		})
		return text, contextualReadError(ReadStageText, verifyErr)
	})
	return frame, nil
}

func validateBrowserWindow(ctx context.Context, api nativeAXAPI, window uintptr) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	node := &nativeAXNode{api: api, element: window}
	title, err := node.stringAttribute(ctx, "AXTitle", 512)
	if err != nil || isPrivateBrowserWindowTitle(title) {
		return errors.New("capture: browser privacy state unavailable")
	}
	return nil
}

func readBrowserDocumentDomain(ctx context.Context, api nativeAXAPI, window uintptr) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	node := &nativeAXNode{api: api, element: window}
	for _, attribute := range []string{"AXDocument", "AXURL"} {
		document, err := node.urlAttribute(ctx, attribute, 2048)
		if err != nil {
			if contextErr := contextError(err); contextErr != nil {
				return "", false, contextErr
			}
			continue
		}
		if domain, known := domainFromDocumentURL(document); known {
			return domain, true, nil
		}
	}
	// Missing, non-web, or malformed metadata remains unknown. Domain rules
	// then fail closed before any source-text callback is invoked.
	return "", false, nil
}

func loadNativeAXAPI() {
	appServices, err := purego.Dlopen("/System/Library/Frameworks/ApplicationServices.framework/ApplicationServices", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		nativeAX.err = errors.New("ApplicationServices unavailable")
		return
	}
	core, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		nativeAX.err = errors.New("CoreFoundation unavailable")
		return
	}
	a := nativeAXAPI{appServices: appServices, core: core}
	purego.RegisterLibFunc(&a.createApp, appServices, "AXUIElementCreateApplication")
	purego.RegisterLibFunc(&a.copyValue, appServices, "AXUIElementCopyAttributeValue")
	purego.RegisterLibFunc(&a.copyNames, appServices, "AXUIElementCopyAttributeNames")
	purego.RegisterLibFunc(&a.parameterizedValue, appServices, "AXUIElementCopyParameterizedAttributeValue")
	purego.RegisterLibFunc(&a.valueCreate, appServices, "AXValueCreate")
	purego.RegisterLibFunc(&a.valueType, appServices, "AXValueGetTypeID")
	purego.RegisterLibFunc(&a.valueKind, appServices, "AXValueGetType")
	purego.RegisterLibFunc(&a.valueGetRange, appServices, "AXValueGetValue")
	purego.RegisterLibFunc(&a.valueGetPair, appServices, "AXValueGetValue")
	// HIToolbox owns the public secure-event-input query. Failure leaves the
	// optional visual fallback disabled; ordinary AX remains available.
	if carbon, err := purego.Dlopen("/System/Library/Frameworks/Carbon.framework/Frameworks/HIToolbox.framework/HIToolbox", purego.RTLD_NOW|purego.RTLD_LOCAL); err == nil {
		if symbol, err := purego.Dlsym(carbon, "IsSecureEventInputEnabled"); err == nil {
			purego.RegisterFunc(&a.secureInput, symbol)
		}
	}
	purego.RegisterLibFunc(&a.numberType, core, "CFNumberGetTypeID")
	purego.RegisterLibFunc(&a.numberGetInt64, core, "CFNumberGetValue")
	purego.RegisterLibFunc(&a.attributedType, core, "CFAttributedStringGetTypeID")
	purego.RegisterLibFunc(&a.attributedString, core, "CFAttributedStringGetString")
	purego.RegisterLibFunc(&a.setValue, appServices, "AXUIElementSetAttributeValue")
	purego.RegisterLibFunc(&a.valueCount, appServices, "AXUIElementGetAttributeValueCount")
	purego.RegisterLibFunc(&a.copyValues, appServices, "AXUIElementCopyAttributeValues")
	purego.RegisterLibFunc(&a.setTimeout, appServices, "AXUIElementSetMessagingTimeout")
	purego.RegisterLibFunc(&a.getTypeID, core, "CFGetTypeID")
	purego.RegisterLibFunc(&a.stringType, core, "CFStringGetTypeID")
	purego.RegisterLibFunc(&a.urlType, core, "CFURLGetTypeID")
	purego.RegisterLibFunc(&a.urlString, core, "CFURLGetString")
	purego.RegisterLibFunc(&a.booleanType, core, "CFBooleanGetTypeID")
	purego.RegisterLibFunc(&a.booleanValue, core, "CFBooleanGetValue")
	purego.RegisterLibFunc(&a.arrayType, core, "CFArrayGetTypeID")
	purego.RegisterLibFunc(&a.arrayCount, core, "CFArrayGetCount")
	purego.RegisterLibFunc(&a.arrayValue, core, "CFArrayGetValueAtIndex")
	purego.RegisterLibFunc(&a.stringLen, core, "CFStringGetLength")
	purego.RegisterLibFunc(&a.stringCharacter, core, "CFStringGetCharacterAtIndex")
	purego.RegisterLibFunc(&a.stringCString, core, "CFStringGetCString")
	purego.RegisterLibFunc(&a.stringNew, core, "CFStringCreateWithCString")
	purego.RegisterLibFunc(&a.stringCopy, core, "CFStringCreateCopy")
	purego.RegisterLibFunc(&a.retain, core, "CFRetain")
	purego.RegisterLibFunc(&a.release, core, "CFRelease")
	purego.RegisterLibFunc(&a.equal, core, "CFEqual")
	trueSymbol, err := nativeffi.GetExternVariant[uintptr](core, "kCFBooleanTrue")
	if err != nil || trueSymbol == nil || *trueSymbol == 0 {
		nativeAX.err = errors.New("CoreFoundation boolean unavailable")
		return
	}
	a.trueValue = *trueSymbol
	nativeAX.api = a
}

type manualAXTracker struct {
	mu       sync.Mutex
	attempts map[int32]time.Time
}

var manualAXRequests manualAXTracker

func (t *manualAXTracker) admit(pid int32, now time.Time) bool {
	if pid <= 0 {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.attempts == nil {
		t.attempts = make(map[int32]time.Time)
	}
	if at, found := t.attempts[pid]; found && now.Sub(at) < time.Minute {
		return false
	}
	if len(t.attempts) >= 128 {
		var oldest int32
		var at time.Time
		for process, attempted := range t.attempts {
			if oldest == 0 || attempted.Before(at) {
				oldest, at = process, attempted
			}
		}
		delete(t.attempts, oldest)
	}
	t.attempts[pid] = now
	return true
}

// Electron/Chromium can expose an empty AX tree until assistive software
// requests this supported attribute. This uses existing AX permission and
// never opens an OS permission prompt; unsupported apps simply ignore it.
func requestManualAccessibility(ctx context.Context, api nativeAXAPI, pid int32) {
	if ctx.Err() != nil || api.setValue == nil || api.trueValue == 0 {
		return
	}
	app := api.createApp(pid)
	if app == 0 {
		return
	}
	defer api.release(app)
	if setAXTimeout(ctx, api, app) != nil {
		return
	}
	attribute, storage, err := createAXAttribute(api, "AXManualAccessibility")
	if err != nil {
		return
	}
	defer api.release(attribute)
	defer runtime.KeepAlive(storage)
	_ = api.setValue(app, attribute, api.trueValue)
}

func focusedWindow(ctx context.Context, api nativeAXAPI, pid int32) (uintptr, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	app := api.createApp(pid)
	if app == 0 {
		return 0, NativeReadError(ReadStageApplication, 0)
	}
	defer api.release(app)
	if err := setAXTimeout(ctx, api, app); err != nil {
		return 0, err
	}
	attribute, storage, err := createAXAttribute(api, "AXFocusedWindow")
	if err != nil {
		return 0, err
	}
	defer api.release(attribute)
	defer runtime.KeepAlive(storage)
	var window uintptr
	if status := api.copyValue(app, attribute, &window); status != axSuccess || window == 0 {
		if window != 0 {
			api.release(window)
		}
		original := NativeReadError(ReadStageWindow, status)
		if status == axAttributeUnsupported || status == axNoValue || status == axCannotComplete {
			window, fallbackErr := focusedWindowFromElement(ctx, api, app)
			if fallbackErr != nil {
				return 0, fallbackErr
			}
			if window != 0 {
				return window, nil
			}
		}
		return 0, original
	}
	if err := ctx.Err(); err != nil {
		api.release(window)
		return 0, err
	}
	return window, nil
}

// focusedWindowFromElement resolves the focused element's containing window
// when an application does not complete AXFocusedWindow. The result is still
// a real AXWindow; callers continue to validate and inspect its complete tree
// before reading any text.
func focusedWindowFromElement(ctx context.Context, api nativeAXAPI, app uintptr) (uintptr, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	attribute, storage, err := createAXAttribute(api, "AXFocusedUIElement")
	if err != nil {
		return 0, err
	}
	defer api.release(attribute)
	defer runtime.KeepAlive(storage)
	var current uintptr
	status := api.copyValue(app, attribute, &current)
	if status != axSuccess || current == 0 {
		if current != 0 {
			api.release(current)
		}
		return 0, nil
	}
	defer func() {
		if current != 0 {
			api.release(current)
		}
	}()
	seen := make(map[uintptr]struct{}, MaxCaptureDepth)
	for depth := 0; depth < MaxCaptureDepth; depth++ {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if _, exists := seen[current]; exists {
			return 0, nil
		}
		seen[current] = struct{}{}
		node := &nativeAXNode{api: api, element: current}
		role, err := node.role(ctx)
		if err != nil {
			if contextErr := contextError(err); contextErr != nil {
				return 0, contextErr
			}
			return 0, err
		}
		if role == "AXWindow" {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
			window := current
			current = 0 // Transfer the copied AX reference to the caller.
			return window, nil
		}
		if !validAXRole(role) {
			return 0, NativeReadError(ReadStageRole, 0)
		}
		parent, parentStatus, parentErr := node.copyAttribute(ctx, "AXParent")
		if parentErr != nil {
			if contextErr := contextError(parentErr); contextErr != nil {
				return 0, contextErr
			}
			return 0, parentErr
		}
		if parentStatus != axSuccess || parent == 0 {
			if parent != 0 {
				api.release(parent)
			}
			return 0, nil
		}
		if _, exists := seen[parent]; exists {
			api.release(parent)
			return 0, nil
		}
		api.release(current)
		current = parent
	}
	return 0, nil
}

func validateFocusedWindow(ctx context.Context, api nativeAXAPI, pid int32, expected uintptr) error {
	current, err := focusedWindow(ctx, api, pid)
	if err != nil {
		return err
	}
	defer api.release(current)
	if !api.equal(current, expected) {
		return platform.ErrInvalidLease
	}
	return nil
}

func (n *nativeAXNode) identity() uintptr { return n.element }

func (n *nativeAXNode) release() {
	if n != nil && n.element != 0 && n.closed.CompareAndSwap(false, true) {
		n.api.release(n.element)
	}
}

func (n *nativeAXNode) role(ctx context.Context) (string, error) {
	return n.stringAttribute(ctx, "AXRole", maxProfileRoleLen)
}

func (n *nativeAXNode) hidden(ctx context.Context) (bool, error) {
	value, status, err := n.copyAttribute(ctx, "AXHidden")
	if err != nil {
		return false, err
	}
	if status == axAttributeUnsupported || status == axNoValue {
		return false, nil
	}
	defer n.api.release(value)
	if n.api.getTypeID(value) != n.api.booleanType() {
		return false, errBrowserContentUnavailable
	}
	return n.api.booleanValue(value) != 0, nil
}

func (n *nativeAXNode) location(ctx context.Context) (string, bool, error) {
	return readBrowserDocumentDomain(ctx, n.api, n.element)
}

func (n *nativeAXNode) childMatches(ctx context.Context, index, total int, expected uintptr) (bool, error) {
	if index < 0 || index >= total || expected == 0 {
		return false, errBrowserPageChanged
	}
	if err := n.prepare(ctx); err != nil {
		return false, err
	}
	attribute, storage, err := createAXAttribute(n.api, "AXChildren")
	if err != nil {
		return false, err
	}
	defer n.api.release(attribute)
	defer runtime.KeepAlive(storage)
	var count int64
	if n.api.valueCount(n.element, attribute, &count) != axSuccess || count != int64(total) {
		return false, errBrowserPageChanged
	}
	var array uintptr
	status := n.api.copyValues(n.element, attribute, int64(index), 1, &array)
	if array != 0 {
		defer n.api.release(array)
	}
	if status != axSuccess || array == 0 || n.api.getTypeID(array) != n.api.arrayType() || n.api.arrayCount(array) != 1 {
		return false, errBrowserPageChanged
	}
	child := n.api.arrayValue(array, 0)
	return child != 0 && n.api.equal(child, expected), ctx.Err()
}

func (n *nativeAXNode) sensitive(ctx context.Context, role string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return true, err
	}
	if isSecureTextRole(role) {
		return true, nil
	}
	if !needsSecureSubroleCheck(role) {
		return false, nil
	}
	subrole, err := n.stringAttribute(ctx, "AXSubrole", maxProfileRoleLen)
	if err != nil {
		return false, err
	}
	if isSecureTextRole(subrole) {
		return true, nil
	}
	// Capture follows the user's on-screen work, including editable documents.
	// Secure input is a separate property; editability alone is not secrecy.
	n.guardSensitive.Store(true)
	return false, nil
}

func isSecureTextRole(role string) bool {
	switch strings.TrimPrefix(role, "AX") {
	case "SecureTextField", "SecureTextArea", "PasswordField":
		return true
	default:
		return false
	}
}

func (n *nativeAXNode) verifyTextSensitivity(ctx context.Context) error {
	role, err := n.role(ctx)
	if err != nil {
		return err
	}
	if !validAXRole(role) {
		return NativeReadError(ReadStageRole, 0)
	}
	sensitive, err := n.sensitive(ctx, role)
	if err != nil {
		return err
	}
	if sensitive {
		return NativeReadError(ReadStageSensitivity, 0)
	}
	return nil
}

func (n *nativeAXNode) value(ctx context.Context, max int) (text string, err error) {
	defer func() {
		if text != "" && n.guardSensitive.Load() && (err == nil || errors.Is(err, errAXValueTruncated)) {
			if policyErr := n.verifyTextSensitivity(ctx); policyErr != nil {
				text, err = "", policyErr
			}
		}
	}()
	value, status, err := n.copyAttribute(ctx, "AXValue")
	if err != nil {
		return "", err
	}
	if status == axAttributeUnsupported || status == axNoValue {
		return n.rangeText(ctx, max)
	}
	defer n.api.release(value)
	textValue := n.textString(value)
	if textValue == 0 {
		return "", nil
	}
	if n.api.stringLen(textValue) == 0 {
		return n.rangeText(ctx, max)
	}
	return n.copyVisibleText(ctx, textValue, max)
}

func (n *nativeAXNode) children(ctx context.Context, max int) ([]axTreeNode, bool, error) {
	if max <= 0 {
		return nil, false, nil
	}
	nodes, total, err := n.childrenPage(ctx, 0, max, -1)
	return nodes, len(nodes) < total, err
}

// childrenPage bounds each native fetch while retaining the actual parent
// count. A continuation can validate that count before accessing a later page.
// expectedTotal=-1 opens a new traversal; later calls bind the original count.
func (n *nativeAXNode) childrenPage(ctx context.Context, start, max, expectedTotal int) ([]axTreeNode, int, error) {
	if ctx == nil || start < 0 || max < 1 || max > MaxCaptureNodes || expectedTotal < -1 {
		return nil, 0, platform.ErrReadLimit
	}
	if err := n.prepare(ctx); err != nil {
		return nil, 0, err
	}
	attribute, storage, err := createAXAttribute(n.api, "AXChildren")
	if err != nil {
		return nil, 0, err
	}
	defer n.api.release(attribute)
	defer runtime.KeepAlive(storage)
	var count int64
	status := n.api.valueCount(n.element, attribute, &count)
	if status == axAttributeUnsupported || status == axNoValue {
		count = 0
	} else if status != axSuccess {
		return nil, 0, NativeReadError(ReadStageChildren, status)
	}
	if count < 0 || int64(int(count)) != count {
		return nil, 0, NativeReadError(ReadStageChildren, 0)
	}
	total := int(count)
	if expectedTotal >= 0 && total != expectedTotal || start > total {
		return nil, 0, errBrowserPageChanged
	}
	if start == total {
		return nil, total, nil
	}
	requested := min(max, total-start)
	var array uintptr
	status = n.api.copyValues(n.element, attribute, int64(start), int64(requested), &array)
	if array != 0 {
		defer n.api.release(array)
	}
	if status != axSuccess || array == 0 {
		return nil, 0, NativeReadError(ReadStageChildren, status)
	}
	if n.api.getTypeID(array) != n.api.arrayType() {
		return nil, 0, NativeReadError(ReadStageChildren, 0)
	}
	actual := n.api.arrayCount(array)
	if actual <= 0 || actual > int64(requested) {
		return nil, 0, errBrowserPageChanged
	}
	children := make([]axTreeNode, 0, int(actual))
	for index := 0; index < int(actual); index++ {
		if err := ctx.Err(); err != nil {
			releaseAXNodes(children)
			return nil, 0, err
		}
		child := n.api.arrayValue(array, int64(index))
		if child == 0 || n.api.getTypeID(child) != axElementTypeID() {
			releaseAXNodes(children)
			return nil, 0, NativeReadError(ReadStageChildren, 0)
		}
		n.api.retain(child)
		children = append(children, &nativeAXNode{api: n.api, element: child})
	}
	return children, total, nil
}

var axElementType struct {
	once sync.Once
	id   uintptr
}

func axElementTypeID() uintptr {
	axElementType.once.Do(func() {
		var get func() uintptr
		purego.RegisterLibFunc(&get, nativeAX.api.appServices, "AXUIElementGetTypeID")
		axElementType.id = get()
	})
	return axElementType.id
}

func (n *nativeAXNode) stringAttribute(ctx context.Context, name string, max int) (string, error) {
	value, status, err := n.copyAttribute(ctx, name)
	if err != nil {
		return "", err
	}
	if status == axAttributeUnsupported || status == axNoValue {
		return "", nil
	}
	defer n.api.release(value)
	if n.api.getTypeID(value) != n.api.stringType() {
		return "", NativeReadError(readStageForAttribute(name), 0)
	}
	text, truncated, err := copyCFStringContext(ctx, n.api, value, max)
	if err != nil {
		return "", err
	}
	if truncated {
		return "", NativeReadError(readStageForAttribute(name), 0)
	}
	return text, nil
}

// Location attributes can contain either CFStringRef or CFURLRef. Only the
// bounded URL is converted here; the caller keeps its normalized host alone.
func (n *nativeAXNode) urlAttribute(ctx context.Context, name string, max int) (string, error) {
	value, status, err := n.copyAttribute(ctx, name)
	if err != nil {
		return "", err
	}
	if status == axAttributeUnsupported || status == axNoValue {
		return "", nil
	}
	defer n.api.release(value)
	if n.api.urlType != nil && n.api.getTypeID(value) == n.api.urlType() {
		value = n.api.urlString(value)
	}
	if value == 0 || n.api.getTypeID(value) != n.api.stringType() {
		return "", errors.New("capture: invalid accessibility location")
	}
	text, truncated, err := copyCFStringContext(ctx, n.api, value, max)
	if err != nil || truncated {
		return "", errors.New("capture: accessibility location out of bounds")
	}
	return text, nil
}

func (n *nativeAXNode) copyAttribute(ctx context.Context, name string) (uintptr, int32, error) {
	guard := name == "AXValue" && n.guardSensitive.Load()
	if guard {
		if err := n.verifyTextSensitivity(ctx); err != nil {
			return 0, -1, err
		}
	}
	if err := n.prepare(ctx); err != nil {
		return 0, -1, err
	}
	attribute, storage, err := createAXAttribute(n.api, name)
	if err != nil {
		return 0, -1, err
	}
	defer n.api.release(attribute)
	defer runtime.KeepAlive(storage)
	var value uintptr
	status := n.api.copyValue(n.element, attribute, &value)
	if status != axSuccess {
		if value != 0 {
			n.api.release(value)
		}
		if status == axAttributeUnsupported || status == axNoValue {
			return 0, status, nil
		}
		return 0, status, NativeReadError(readStageForAttribute(name), status)
	}
	if value == 0 {
		return 0, status, NativeReadError(readStageForAttribute(name), status)
	}
	if guard {
		if err := n.verifyTextSensitivity(ctx); err != nil {
			n.api.release(value)
			return 0, -1, err
		}
	}
	return value, status, nil
}

func (n *nativeAXNode) prepare(ctx context.Context) error {
	return setAXTimeout(ctx, n.api, n.element)
}

func setAXTimeout(ctx context.Context, api nativeAXAPI, element uintptr) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	timeout := float32(axMessagingTimeout)
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline).Seconds()
		if remaining <= 0 {
			return context.DeadlineExceeded
		}
		if remaining < float64(timeout) {
			timeout = float32(remaining)
		}
	}
	if timeout <= 0 {
		return context.DeadlineExceeded
	}
	if status := api.setTimeout(element, timeout); status != axSuccess {
		return NativeReadError(ReadStageSetup, status)
	}
	return ctx.Err()
}

func readStageForAttribute(name string) ReadStage {
	switch name {
	case "AXRole":
		return ReadStageRole
	case "AXSubrole":
		return ReadStageSensitivity
	case "AXChildren":
		return ReadStageChildren
	case "AXNumberOfCharacters", "AXVisibleCharacterRange", "AXStringForRange":
		return ReadStageRange
	case "AXValue":
		return ReadStageText
	default:
		return ReadStageWindow
	}
}

func createAXAttribute(api nativeAXAPI, name string) (uintptr, []byte, error) {
	bytes, pointer := nativeCString(name)
	attribute := api.stringNew(0, pointer, cfUTF8)
	runtime.KeepAlive(bytes)
	if attribute == 0 {
		return 0, nil, NativeReadError(ReadStageSetup, 0)
	}
	return attribute, bytes, nil
}

// copyCFString keeps the fixture/metadata convenience API. Native value reads
// use the context-aware form so large conversions honor cancellation.
func copyCFString(api nativeAXAPI, value uintptr, maxBytes int) (string, bool, error) {
	return copyCFStringContext(context.Background(), api, value, maxBytes)
}

func copyCFStringContext(ctx context.Context, api nativeAXAPI, value uintptr, maxBytes int) (string, bool, error) {
	text, _, more, err := copyCFStringRange(ctx, api, value, 0, maxBytes)
	return text, more, err
}

// copyCFStringRange reads an immutable CFString snapshot in bounded UTF-8
// pieces. The continuation is a UTF-16 code-unit offset, always between scalar
// values. Scalar CF APIs avoid unsupported by-value CFRange aggregate calls.
func copyCFStringRange(ctx context.Context, api nativeAXAPI, value uintptr, start int64, maxBytes int) (string, int64, bool, error) {
	if ctx == nil || value == 0 || api.stringLen == nil || maxBytes < 0 || maxBytes > MaxObservationTextBytes {
		return "", start, false, platform.ErrReadLimit
	}
	if err := ctx.Err(); err != nil {
		return "", start, false, err
	}
	length := api.stringLen(value)
	if length < 0 || start < 0 || start > length {
		return "", start, false, platform.ErrReadLimit
	}
	if start == length {
		return "", start, false, nil
	}
	if maxBytes == 0 {
		return "", start, true, nil
	}
	// Most AX labels are small. Keep a single native conversion for strings
	// that fit, and verify all UTF-16 units survived (including embedded NULs).
	if start == 0 && length <= int64(maxBytes) && api.stringCString != nil {
		buffer := make([]byte, maxBytes+1)
		converted := api.stringCString(value, uintptr(unsafe.Pointer(&buffer[0])), int64(len(buffer)), cfUTF8) // #nosec G103 -- nonempty bounded output buffer stays alive through the synchronous CF call.
		runtime.KeepAlive(buffer)
		if converted != 0 {
			end := 0
			for end < len(buffer) && buffer[end] != 0 {
				end++
			}
			if end == len(buffer) {
				clear(buffer)
				return "", start, false, errors.New("capture: unterminated accessibility string")
			}
			text := string(buffer[:end])
			clear(buffer)
			if !utf8.ValidString(text) {
				return "", start, false, errors.New("capture: invalid accessibility string")
			}
			units := int64(0)
			for _, r := range text {
				if r > 0xffff {
					units += 2
				} else {
					units++
				}
			}
			if units == length {
				if err := ctx.Err(); err != nil {
					return "", start, false, err
				}
				return text, length, false, nil
			}
		} else {
			clear(buffer)
		}
	}
	if api.stringCharacter == nil {
		return "", start, false, errors.New("capture: accessibility string conversion unavailable")
	}
	if start > 0 {
		current := api.stringCharacter(value, start)
		if current >= 0xdc00 && current <= 0xdfff {
			previous := api.stringCharacter(value, start-1)
			if previous >= 0xd800 && previous <= 0xdbff {
				return "", start, false, platform.ErrReadLimit
			}
		}
	}
	var out strings.Builder
	out.Grow(maxBytes)
	offset := start
	for offset < length {
		if (offset-start)&63 == 0 {
			if err := ctx.Err(); err != nil {
				return "", start, false, err
			}
		}
		unit := api.stringCharacter(value, offset)
		r, width := rune(unit), int64(1)
		if unit >= 0xd800 && unit <= 0xdbff {
			if offset+1 < length {
				low := api.stringCharacter(value, offset+1)
				if low >= 0xdc00 && low <= 0xdfff {
					r = utf16.DecodeRune(rune(unit), rune(low))
					width = 2
				} else {
					r = utf8.RuneError
				}
			} else {
				r = utf8.RuneError
			}
		} else if unit >= 0xdc00 && unit <= 0xdfff {
			r = utf8.RuneError
		}
		if r == 0 {
			r = ' '
		}
		if utf8.RuneLen(r) > maxBytes-out.Len() {
			break
		}
		out.WriteRune(r)
		offset += width
	}
	if err := ctx.Err(); err != nil {
		return "", start, false, err
	}
	return out.String(), offset, offset < length, nil
}

func nativeCString(s string) ([]byte, uintptr) {
	buffer := append([]byte(s), 0)
	return buffer, uintptr(unsafe.Pointer(&buffer[0])) // #nosec G103 -- NUL-terminated nonempty storage is returned and kept alive by each synchronous native caller.
}
