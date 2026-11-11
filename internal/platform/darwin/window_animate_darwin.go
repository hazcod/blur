package darwin

import (
	"math"

	"github.com/ebitengine/purego/objc"
	"github.com/golang-gui/goui/platform/darwin/frameworks/appkit"
	"github.com/golang-gui/goui/platform/darwin/frameworks/foundation"
)

// quickPanelFadeSeconds is the fade-in duration used when the panel opens.
const quickPanelFadeSeconds = 0.12

// ReduceMotionEnabled reports the system "Reduce motion" accessibility
// preference. Call on the AppKit main thread.
func ReduceMotionEnabled() bool {
	workspace := objc.Send[objc.ID](objc.ID(objc.GetClass("NSWorkspace")), objc.RegisterName("sharedWorkspace"))
	if workspace == 0 {
		return false
	}
	return objc.Send[bool](workspace, objc.RegisterName("accessibilityDisplayShouldReduceMotion"))
}

// AnimateWindowContentResize is ResizeWindowContent with AppKit's default
// frame animation. The panel's top edge stays flush with the main display's
// top edge while it grows or shrinks downward. With Reduce Motion enabled, a
// hidden window, or an unchanged frame it applies the new frame immediately.
// Call on the AppKit main thread; dimensions are logical points.
func AnimateWindowContentResize(handle uintptr, width, height float64) error {
	if handle == 0 || !validWindowContentSize(width, height) {
		return errInvalidWindowSize
	}
	screen, ok := mainScreenFrame()
	if !ok {
		return ResizeWindowContent(handle, width, height)
	}
	window := appkit.NSWindow{NSObject: foundation.NSObject{ID: objc.ID(handle)}}
	prepareQuickPanelWindow(window)
	installQuickPanelBackdrop(window)

	x, y := quickPanelFrameOrigin(screen, width, height, 0)
	target := nsRect{X: x, Y: y, Width: width, Height: height}
	current := objc.Send[nsRect](window.ID, objc.RegisterName("frame"))
	visible := objc.Send[bool](window.ID, objc.RegisterName("isVisible"))
	animate := shouldAnimateWindowFrame(ReduceMotionEnabled(), visible, current, target)
	objc.Send[objc.ID](window.ID, objc.RegisterName("setFrame:display:animate:"), target, true, animate)
	// The shadow is derived from the window's alpha shape; refresh it for the
	// new silhouette.
	objc.Send[objc.ID](window.ID, objc.RegisterName("invalidateShadow"))
	return nil
}

// FadeInWindow fades a window from transparent to opaque over about 120 ms.
// With Reduce Motion enabled the window is simply made opaque. Call on the
// AppKit main thread.
func FadeInWindow(handle uintptr) error {
	if handle == 0 {
		return errInvalidWindowSize
	}
	window := objc.ID(handle)
	setAlpha := objc.RegisterName("setAlphaValue:")
	if ReduceMotionEnabled() {
		objc.Send[objc.ID](window, setAlpha, float64(1))
		return nil
	}
	objc.Send[objc.ID](window, setAlpha, float64(0))
	context := objc.ID(objc.GetClass("NSAnimationContext"))
	objc.Send[objc.ID](context, objc.RegisterName("beginGrouping"))
	current := objc.Send[objc.ID](context, objc.RegisterName("currentContext"))
	objc.Send[objc.ID](current, objc.RegisterName("setDuration:"), float64(quickPanelFadeSeconds))
	proxy := objc.Send[objc.ID](window, objc.RegisterName("animator"))
	objc.Send[objc.ID](proxy, setAlpha, float64(1))
	objc.Send[objc.ID](context, objc.RegisterName("endGrouping"))
	return nil
}

// shouldAnimateWindowFrame decides whether a frame change is animated: never
// under Reduce Motion, for a hidden window, or when the frame is effectively
// unchanged.
func shouldAnimateWindowFrame(reduceMotion, visible bool, current, target nsRect) bool {
	if reduceMotion || !visible {
		return false
	}
	const epsilon = 0.5
	return math.Abs(current.X-target.X) > epsilon || math.Abs(current.Y-target.Y) > epsilon ||
		math.Abs(current.Width-target.Width) > epsilon || math.Abs(current.Height-target.Height) > epsilon
}

// WindowVisible reports whether the window is currently on screen. Call on the
// AppKit main thread.
func WindowVisible(handle uintptr) bool {
	return handle != 0 && objc.Send[bool](objc.ID(handle), objc.RegisterName("isVisible"))
}

// OrderWindowFrontQuietly puts a window on screen without activating the app
// or making the window key. Call on the AppKit main thread.
func OrderWindowFrontQuietly(handle uintptr) error {
	if handle == 0 {
		return errInvalidWindowSize
	}
	objc.Send[objc.ID](objc.ID(handle), objc.RegisterName("orderFrontRegardless"))
	return nil
}

// SetWindowAlpha sets a window's alpha without animation. The quick panel is
// made fully transparent before it is ordered front so that nothing is shown
// until its final frame is set. Call on the AppKit main thread.
func SetWindowAlpha(handle uintptr, alpha float64) error {
	if handle == 0 {
		return errInvalidWindowSize
	}
	objc.Send[objc.ID](objc.ID(handle), objc.RegisterName("setAlphaValue:"), alpha)
	return nil
}

// CommandKeyHeld reports whether the Command key is down right now. It reads
// the current modifier state only; no key events are observed. Call on the
// AppKit main thread.
func CommandKeyHeld() bool {
	const commandFlag = uint64(1 << 20)
	flags := objc.Send[uint64](objc.ID(objc.GetClass("NSEvent")), objc.RegisterName("modifierFlags"))
	return flags&commandFlag != 0
}
