package capture

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/hazcod/blur/internal/appidentity"
	"github.com/hazcod/blur/internal/platform"
	"github.com/hazcod/blur/internal/platform/darwin"
)

const (
	axSuccess              = 0
	axAttributeUnsupported = -25205
	axNoValue              = -25212
	axMessagingTimeout     = 0.2
	cfUTF8                 = 0x08000100
)

type nativeAXAPI struct {
	appServices   uintptr
	core          uintptr
	createApp     func(int32) uintptr
	copyValue     func(uintptr, uintptr, *uintptr) int32
	valueCount    func(uintptr, uintptr, *int64) int32
	copyValues    func(uintptr, uintptr, int64, int64, *uintptr) int32
	setTimeout    func(uintptr, float32) int32
	getTypeID     func(uintptr) uintptr
	stringType    func() uintptr
	arrayCount    func(uintptr) int64
	arrayValue    func(uintptr, int64) uintptr
	stringLen     func(uintptr) int64
	stringCString func(uintptr, uintptr, int64, uint32) uint8
	stringNew     func(uintptr, uintptr, uint32) uintptr
	retain        func(uintptr) uintptr
	release       func(uintptr)
	equal         func(uintptr, uintptr) bool
}

var nativeAX struct {
	once sync.Once
	api  nativeAXAPI
	err  error
}

type nativeAXNode struct {
	api     nativeAXAPI
	element uintptr
	closed  atomic.Bool
}

// NewDarwinPipeline installs the native AX reader behind the common capture
// pipeline. Raw AX text is converted only by bounded value callbacks after
// structure, profiles, and exclusions have passed preflight.
func NewDarwinPipeline(source *darwin.Adapter, limits Limits, exclusions Exclusions, profiles []AppProfile) Pipeline {
	profileCopy := make([]AppProfile, len(profiles))
	for i, profile := range profiles {
		profileCopy[i] = AppProfile{AppID: strings.Clone(profile.AppID), IgnoreRoles: append([]string(nil), profile.IgnoreRoles...)}
	}
	exclusions.Apps = append([]string(nil), exclusions.Apps...)
	exclusions.Domains = append([]string(nil), exclusions.Domains...)
	return Pipeline{
		Source: source, Limits: limits, Exclusions: exclusions, Profiles: profileCopy,
		Read: func(ctx context.Context, probe platform.CaptureProbe, limits Limits) (rawFrame, error) {
			return readDarwinAX(ctx, source, probe, limits)
		},
	}
}

func readDarwinAX(ctx context.Context, source *darwin.Adapter, probe platform.CaptureProbe, limits Limits) (rawFrame, error) {
	var frame rawFrame
	if source == nil {
		return frame, errors.New("capture: Darwin AX source unavailable")
	}
	nativeAX.once.Do(loadNativeAXAPI)
	if nativeAX.err != nil {
		return frame, errors.New("capture: Darwin AX API unavailable")
	}
	err := source.WithVerifiedTarget(ctx, probe.TargetID, func(ctx context.Context, pid int32) error {
		window, err := focusedWindow(ctx, nativeAX.api, pid)
		if err != nil {
			return err
		}
		root := &nativeAXNode{api: nativeAX.api, element: window}
		browser := appidentity.IsBrowserApp(probe.AppID)
		documentDomain, documentDomainKnown := "", false
		if browser {
			if err := validateBrowserWindow(ctx, nativeAX.api, window); err != nil {
				root.release()
				return errors.New("capture: browser privacy state unavailable")
			}
			documentDomain, documentDomainKnown, err = readBrowserDocumentDomain(ctx, nativeAX.api, window)
			if err != nil {
				root.release()
				return err
			}
		}
		frame, err = collectAXFrame(ctx, root, limits)
		if err != nil {
			return err
		}
		frame.domain, frame.domainKnown = documentDomain, documentDomainKnown
		if browser {
			frame.validateDomain = func(ctx context.Context, expected string) error {
				return source.WithVerifiedTarget(ctx, probe.TargetID, func(ctx context.Context, targetPID int32) error {
					if err := validateFocusedWindow(ctx, nativeAX.api, targetPID, window); err != nil {
						return err
					}
					current, known, err := readBrowserDocumentDomain(ctx, nativeAX.api, window)
					if err != nil {
						return err
					}
					if !known || current != expected {
						return errCaptureDomainChanged
					}
					return nil
				})
			}
		}
		return validateFocusedWindow(ctx, nativeAX.api, pid, window)
	})
	if err != nil {
		return frame, err
	}
	// The focused-window proxy is owned by the first frame node. Its identity is
	// captured separately for per-value revalidation without exposing a title.
	rootElement := frame.nativeRootID
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
		return text, verifyErr
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
	document, err := node.stringAttribute(ctx, "AXDocument", 2048)
	if err != nil {
		if contextErr := contextError(err); contextErr != nil {
			return "", false, contextErr
		}
		// Unsupported or transiently malformed location metadata is unknown;
		// domain-policy captures then fail closed before any text is read.
		return "", false, nil
	}
	domain, ok := domainFromDocumentURL(document)
	return domain, ok, nil
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
	purego.RegisterLibFunc(&a.valueCount, appServices, "AXUIElementGetAttributeValueCount")
	purego.RegisterLibFunc(&a.copyValues, appServices, "AXUIElementCopyAttributeValues")
	purego.RegisterLibFunc(&a.setTimeout, appServices, "AXUIElementSetMessagingTimeout")
	purego.RegisterLibFunc(&a.getTypeID, core, "CFGetTypeID")
	purego.RegisterLibFunc(&a.stringType, core, "CFStringGetTypeID")
	purego.RegisterLibFunc(&a.arrayCount, core, "CFArrayGetCount")
	purego.RegisterLibFunc(&a.arrayValue, core, "CFArrayGetValueAtIndex")
	purego.RegisterLibFunc(&a.stringLen, core, "CFStringGetLength")
	purego.RegisterLibFunc(&a.stringCString, core, "CFStringGetCString")
	purego.RegisterLibFunc(&a.stringNew, core, "CFStringCreateWithCString")
	purego.RegisterLibFunc(&a.retain, core, "CFRetain")
	purego.RegisterLibFunc(&a.release, core, "CFRelease")
	purego.RegisterLibFunc(&a.equal, core, "CFEqual")
	nativeAX.api = a
}

func focusedWindow(ctx context.Context, api nativeAXAPI, pid int32) (uintptr, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	app := api.createApp(pid)
	if app == 0 {
		return 0, errors.New("capture: focused app unavailable")
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
		return 0, errors.New("capture: focused window unavailable")
	}
	if err := ctx.Err(); err != nil {
		api.release(window)
		return 0, err
	}
	return window, nil
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

func (n *nativeAXNode) sensitive(ctx context.Context, role string) (bool, error) {
	if isEditableTextRole(role) {
		return true, nil
	}
	subrole, err := n.stringAttribute(ctx, "AXSubrole", maxProfileRoleLen)
	if err != nil {
		return false, err
	}
	return subrole == "AXSecureTextField" || subrole == "AXPasswordField", nil
}

func (n *nativeAXNode) value(ctx context.Context, max int) (string, error) {
	value, status, err := n.copyAttribute(ctx, "AXValue")
	if err != nil {
		return "", err
	}
	if status == axAttributeUnsupported || status == axNoValue {
		return "", nil
	}
	defer n.api.release(value)
	if n.api.getTypeID(value) != n.api.stringType() {
		return "", nil
	}
	text, truncated, err := copyCFString(n.api, value, max)
	if err != nil {
		return "", err
	}
	if truncated {
		return text, errAXValueTruncated
	}
	return text, nil
}

func (n *nativeAXNode) children(ctx context.Context, max int) ([]axTreeNode, bool, error) {
	if max <= 0 {
		return nil, false, nil
	}
	if err := n.prepare(ctx); err != nil {
		return nil, false, err
	}
	attribute, storage, err := createAXAttribute(n.api, "AXChildren")
	if err != nil {
		return nil, false, err
	}
	defer n.api.release(attribute)
	defer runtime.KeepAlive(storage)
	var count int64
	status := n.api.valueCount(n.element, attribute, &count)
	if status == axAttributeUnsupported || status == axNoValue {
		return nil, false, nil
	}
	if status != axSuccess {
		return nil, false, errors.New("capture: accessibility children count failed")
	}
	if count <= 0 {
		return nil, false, nil
	}
	requested := int64(max)
	if count < requested {
		requested = count
	}
	var array uintptr
	status = n.api.copyValues(n.element, attribute, 0, requested, &array)
	if status != axSuccess || array == 0 {
		if array != 0 {
			n.api.release(array)
		}
		return nil, false, errors.New("capture: accessibility children read failed")
	}
	defer n.api.release(array)
	actual := n.api.arrayCount(array)
	if actual < 0 || actual > requested {
		return nil, false, errors.New("capture: accessibility children exceeded bound")
	}
	children := make([]axTreeNode, 0, int(actual))
	for index := int64(0); index < actual; index++ {
		child := n.api.arrayValue(array, index)
		if child == 0 || n.api.getTypeID(child) != axElementTypeID() {
			for _, created := range children {
				created.release()
			}
			return nil, false, errors.New("capture: invalid accessibility child")
		}
		n.api.retain(child)
		children = append(children, &nativeAXNode{api: n.api, element: child})
	}
	return children, count > requested || actual < count, nil
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
		return "", errors.New("capture: invalid accessibility string")
	}
	text, truncated, err := copyCFString(n.api, value, max)
	if err != nil || truncated {
		return "", errors.New("capture: accessibility string out of bounds")
	}
	return text, nil
}

func (n *nativeAXNode) copyAttribute(ctx context.Context, name string) (uintptr, int32, error) {
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
		return 0, status, errors.New("capture: accessibility attribute read failed")
	}
	if value == 0 {
		return 0, status, errors.New("capture: empty accessibility attribute")
	}
	return value, status, nil
}

func (n *nativeAXNode) prepare(ctx context.Context) error {
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
	if timeout <= 0 || n.api.setTimeout(n.element, timeout) != axSuccess {
		return errors.New("capture: accessibility timeout configuration failed")
	}
	return ctx.Err()
}

func setAXTimeout(ctx context.Context, api nativeAXAPI, element uintptr) error {
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
	if timeout <= 0 || api.setTimeout(element, timeout) != axSuccess {
		return errors.New("capture: accessibility timeout configuration failed")
	}
	return ctx.Err()
}

func createAXAttribute(api nativeAXAPI, name string) (uintptr, []byte, error) {
	bytes, pointer := nativeCString(name)
	attribute := api.stringNew(0, pointer, cfUTF8)
	runtime.KeepAlive(bytes)
	if attribute == 0 {
		return 0, nil, errors.New("capture: accessibility attribute unavailable")
	}
	return attribute, bytes, nil
}

func copyCFString(api nativeAXAPI, value uintptr, maxBytes int) (string, bool, error) {
	if maxBytes < 0 || maxBytes > MaxObservationTextBytes {
		return "", false, platform.ErrReadLimit
	}
	length := api.stringLen(value)
	if length < 0 {
		return "", false, errors.New("capture: invalid accessibility string length")
	}
	if length == 0 {
		return "", false, nil
	}
	if maxBytes == 0 {
		return "", true, nil
	}
	// CFStringGetCString writes a NUL-terminated UTF-8 string into the exact
	// bounded destination; unlike CFStringGetBytes it needs no by-value
	// CFRange aggregate, whose ABI is not supported reliably by purego.
	buffer := make([]byte, maxBytes+1)
	converted := api.stringCString(value, uintptr(unsafe.Pointer(&buffer[0])), int64(len(buffer)), cfUTF8)
	runtime.KeepAlive(buffer)
	if converted == 0 {
		clear(buffer)
		return "", true, nil
	}
	end := 0
	for end < len(buffer) && buffer[end] != 0 {
		end++
	}
	if end == len(buffer) {
		clear(buffer)
		return "", false, errors.New("capture: unterminated accessibility string")
	}
	text := strings.ReplaceAll(string(buffer[:end]), "\x00", " ")
	clear(buffer)
	return text, false, nil
}

func nativeCString(s string) ([]byte, uintptr) {
	buffer := append([]byte(s), 0)
	return buffer, uintptr(unsafe.Pointer(&buffer[0]))
}
