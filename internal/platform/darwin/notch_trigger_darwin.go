package darwin

import (
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"

	"github.com/ebitengine/purego/objc"
	"github.com/golang-gui/goui/platform/darwin/frameworks/foundation"
)

const (
	notchIdleWidth     = 190.0
	notchHoverWidth    = 216.0
	notchTriggerHeight = 32.0
	notchIdleAlpha     = 0.015
)

var (
	notchClassOnce sync.Once
	notchClassErr  error
	notchPanel     objc.ID
	notchView      objc.ID
	notchTracking  objc.ID
	notchInteract  func()
	notchHover     func(bool)
	notchIsHovered bool
)

// InstallNotchTrigger installs a transparent, event-driven click/hover target
// over the screen's centered camera/notch area. It performs no polling and
// retains no screen content. Call from the AppKit main thread.
func InstallNotchTrigger(onHover func(bool), openInteract func()) error {
	if openInteract == nil {
		return errors.New("darwin: Interact action unavailable")
	}
	if notchPanel != 0 {
		notchInteract = openInteract
		notchHover = onHover
		return nil
	}
	if err := registerNotchViewClass(); err != nil {
		return err
	}
	screen := objc.Send[objc.ID](objc.ID(objc.GetClass("NSScreen")), objc.RegisterName("mainScreen"))
	if screen == 0 {
		return errors.New("darwin: main screen unavailable")
	}
	screenFrame := objc.Send[foundation.NSRect](screen, objc.RegisterName("frame"))
	frame := notchTriggerFrame(screenFrame, notchIdleWidth)
	panelClass := objc.GetClass("NSPanel")
	if panelClass == 0 {
		return errors.New("darwin: notch panel class unavailable")
	}
	panel := objc.Send[objc.ID](objc.Send[objc.ID](objc.ID(panelClass), objc.RegisterName("alloc")), objc.RegisterName("initWithContentRect:styleMask:backing:defer:"), frame, int64(0), int64(2), false)
	if panel == 0 {
		return errors.New("darwin: notch panel allocation failed")
	}
	viewClass := objc.GetClass("BlurNotchTriggerView")
	view := objc.Send[objc.ID](objc.Send[objc.ID](objc.ID(viewClass), objc.RegisterName("alloc")), objc.RegisterName("initWithFrame:"), foundation.NSRect{Size: frame.Size})
	if view == 0 {
		releaseNativeObject(panel)
		return errors.New("darwin: notch hit area allocation failed")
	}
	objc.Send[objc.ID](view, objc.RegisterName("setWantsLayer:"), true)
	objc.Send[objc.ID](view, objc.RegisterName("setAutoresizingMask:"), int64(18)) // width + height
	layer := objc.Send[objc.ID](view, objc.RegisterName("layer"))
	if layer != 0 {
		black := objc.Send[objc.ID](objc.ID(objc.GetClass("NSColor")), objc.RegisterName("blackColor"))
		cgColor := objc.Send[objc.ID](black, objc.RegisterName("CGColor"))
		objc.Send[objc.ID](layer, objc.RegisterName("setBackgroundColor:"), cgColor)
		objc.Send[objc.ID](layer, objc.RegisterName("setCornerRadius:"), notchTriggerHeight/2)
		// Match the physical camera/notch silhouette: flush to the top edge,
		// softly rounded only along the lower edge.
		objc.Send[objc.ID](layer, objc.RegisterName("setMaskedCorners:"), uint64(3))
		objc.Send[objc.ID](layer, objc.RegisterName("setMasksToBounds:"), true)
	}
	areaClass := objc.GetClass("NSTrackingArea")
	area := objc.Send[objc.ID](objc.Send[objc.ID](objc.ID(areaClass), objc.RegisterName("alloc")), objc.RegisterName("initWithRect:options:owner:userInfo:"), foundation.NSRect{Size: frame.Size}, int64(0x281), view, objc.ID(0))
	if area == 0 {
		releaseNativeObject(view)
		releaseNativeObject(panel)
		return errors.New("darwin: notch tracking area unavailable")
	}
	objc.Send[objc.ID](view, objc.RegisterName("addTrackingArea:"), area)
	objc.Send[objc.ID](panel, objc.RegisterName("setContentView:"), view)
	objc.Send[objc.ID](panel, objc.RegisterName("setOpaque:"), false)
	clear := objc.Send[objc.ID](objc.ID(objc.GetClass("NSColor")), objc.RegisterName("clearColor"))
	objc.Send[objc.ID](panel, objc.RegisterName("setBackgroundColor:"), clear)
	objc.Send[objc.ID](panel, objc.RegisterName("setHasShadow:"), false)
	objc.Send[objc.ID](panel, objc.RegisterName("setIgnoresMouseEvents:"), false)
	objc.Send[objc.ID](panel, objc.RegisterName("setHidesOnDeactivate:"), false)
	objc.Send[objc.ID](panel, objc.RegisterName("setRestorable:"), false)
	objc.Send[objc.ID](panel, objc.RegisterName("setLevel:"), int64(25))               // NSStatusWindowLevel
	objc.Send[objc.ID](panel, objc.RegisterName("setCollectionBehavior:"), int64(337)) // all spaces, stationary, ignore cycle, fullscreen auxiliary
	objc.Send[objc.ID](view, objc.RegisterName("setAlphaValue:"), notchIdleAlpha)
	objc.Send[objc.ID](panel, objc.RegisterName("orderFrontRegardless"))
	notchPanel, notchView, notchTracking, notchInteract, notchHover = panel, view, area, openInteract, onHover
	return nil
}

func registerNotchViewClass() error {
	notchClassOnce.Do(func() {
		class, err := objc.RegisterClass("BlurNotchTriggerView", objc.GetClass("NSView"), nil, nil, []objc.MethodDef{{
			Cmd: objc.RegisterName("mouseEntered:"),
			Fn:  func(_ objc.ID, _ objc.SEL, _ objc.ID) { handleNotchHover(true) },
		}, {
			Cmd: objc.RegisterName("mouseExited:"),
			Fn:  func(_ objc.ID, _ objc.SEL, _ objc.ID) { handleNotchHover(false) },
		}, {
			Cmd: objc.RegisterName("mouseDown:"),
			Fn: func(_ objc.ID, _ objc.SEL, _ objc.ID) {
				if notchInteract != nil {
					slog.Info("notch Interact clicked")
					defer func() {
						if recovered := recover(); recovered != nil {
							slog.Error("notch Interact callback panicked", "panic_type", fmt.Sprintf("%T", recovered), "stack", string(debug.Stack()))
						}
					}()
					notchInteract()
				}
			},
		}, {
			Cmd: objc.RegisterName("acceptsFirstMouse:"),
			Fn:  func(_ objc.ID, _ objc.SEL, _ objc.ID) uint8 { return 1 },
		}})
		if err != nil {
			notchClassErr = err
		} else if class == 0 {
			notchClassErr = errors.New("darwin: notch view class unavailable")
		}
	})
	return notchClassErr
}

func handleNotchHover(hovered bool) {
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Error("notch hover callback panicked", "panic_type", fmt.Sprintf("%T", recovered), "stack", string(debug.Stack()))
		}
	}()
	slog.Debug("notch Interact hover changed", "expanded", hovered)
	updateNotchTrigger(hovered)
	if notchHover != nil {
		notchHover(hovered)
	}
}

func updateNotchTrigger(hovered bool) {
	if notchPanel == 0 || notchView == 0 || notchIsHovered == hovered {
		return
	}
	notchIsHovered = hovered
	width := notchIdleWidth
	alpha := notchIdleAlpha
	if hovered {
		width = notchHoverWidth
		alpha = 0.96
	}
	screen := objc.Send[objc.ID](objc.ID(objc.GetClass("NSScreen")), objc.RegisterName("mainScreen"))
	if screen != 0 {
		frame := notchTriggerFrame(objc.Send[foundation.NSRect](screen, objc.RegisterName("frame")), width)
		objc.Send[objc.ID](notchPanel, objc.RegisterName("setFrame:display:"), frame, true)
	}
	objc.Send[objc.ID](notchView, objc.RegisterName("setAlphaValue:"), alpha)
}

func notchTriggerFrame(screen foundation.NSRect, width float64) foundation.NSRect {
	return foundation.NSRect{
		Origin: foundation.NSPoint{
			X: screen.Origin.X + (screen.Size.Width-width)/2,
			Y: screen.Origin.Y + screen.Size.Height - notchTriggerHeight,
		},
		Size: foundation.NSSize{Width: width, Height: notchTriggerHeight},
	}
}
