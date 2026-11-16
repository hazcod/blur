package darwin

import (
	"github.com/ebitengine/purego/objc"
)

// PrepareTransientWindow selects the requested panel before activation. The
// previous key window (often Settings) must not be restored first. This uses
// targeted activation without NSApplicationActivateAllWindows; Show may then
// resume the GUI window's frame machinery while this panel is already key.
// Call on the AppKit main thread with one of Blur's own window handles.
func PrepareTransientWindow(handle uintptr) error {
	if handle == 0 {
		return errInvalidWindowSize
	}
	withAutoreleasePool(func() {
		window := objc.ID(handle)
		objc.Send[objc.ID](window, objc.RegisterName("setExcludedFromWindowsMenu:"), true)
		objc.Send[objc.ID](window, objc.RegisterName("makeKeyWindow"))
		objc.Send[objc.ID](window, objc.RegisterName("orderFrontRegardless"))
		application := objc.Send[objc.ID](objc.ID(objc.GetClass("NSRunningApplication")), objc.RegisterName("currentApplication"))
		if application != 0 {
			_ = objc.Send[bool](application, objc.RegisterName("activateWithOptions:"), uint64(0))
		}
	})
	return nil
}
