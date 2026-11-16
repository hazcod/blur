package darwin

import (
	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
	"github.com/golang-gui/goui/platform/darwin/frameworks/foundation"
	"runtime"
	"sync"
	"unsafe"
)

var statusDrawingOnce sync.Once
var statusDrawingReady bool

// Loading AppKit makes bitmap/image classes available in headless unit tests;
// it does not construct NSApplication or any status item/window.
func loadStatusIconDrawing() bool {
	statusDrawingOnce.Do(func() {
		if objc.GetClass("NSImage") == 0 {
			if _, err := purego.Dlopen("/System/Library/Frameworks/AppKit.framework/AppKit", purego.RTLD_NOW|purego.RTLD_LOCAL); err != nil {
				return
			}
		}
		statusDrawingReady = objc.GetClass("NSImage") != 0 && objc.GetClass("NSGraphicsContext") != 0
	})
	return statusDrawingReady
}

func menuBarStatusIconAlpha(captureRunning, meetingRecording bool) float64 {
	if meetingRecording {
		return 1
	}
	return menuBarIconAlpha(captureRunning)
}

// Geometry is the exact packaged assets/blur.svg center circle (512,512,r66).
// Only this circle is painted; the ring, background and icon alpha are retained.
func statusCenterDotRect(size float64) nsRect {
	diameter := size * 132 / 1024
	return nsRect{X: (size - diameter) / 2, Y: (size - diameter) / 2, Width: diameter, Height: diameter}
}

// renderStatusIconBitmap draws only into a fresh bitmap graphics context. It
// creates no NSApplication, window, status item, offscreen window or audio work.
// The caller owns the returned representation and must release it.
func renderStatusIconBitmap(source objc.ID, pixels int, recording bool) objc.ID {
	if source == 0 || pixels < 1 || pixels > 256 || !loadStatusIconDrawing() {
		return 0
	}
	repClass := objc.GetClass("NSBitmapImageRep")
	contextClass := objc.GetClass("NSGraphicsContext")
	if repClass == 0 || contextClass == 0 {
		return 0
	}
	space := newStatusIconString("NSDeviceRGBColorSpace")
	rep := objc.Send[objc.ID](objc.Send[objc.ID](objc.ID(repClass), objc.RegisterName("alloc")), objc.RegisterName("initWithBitmapDataPlanes:pixelsWide:pixelsHigh:bitsPerSample:samplesPerPixel:hasAlpha:isPlanar:colorSpaceName:bytesPerRow:bitsPerPixel:"), uintptr(0), int64(pixels), int64(pixels), int64(8), int64(4), true, false, space, int64(pixels*4), int64(32))
	releaseNativeObject(space)
	if rep == 0 {
		return 0
	}
	context := objc.Send[objc.ID](objc.ID(contextClass), objc.RegisterName("graphicsContextWithBitmapImageRep:"), rep)
	if context == 0 {
		releaseNativeObject(rep)
		return 0
	}
	objc.Send[objc.ID](objc.ID(contextClass), objc.RegisterName("saveGraphicsState"))
	defer objc.Send[objc.ID](objc.ID(contextClass), objc.RegisterName("restoreGraphicsState"))
	objc.Send[objc.ID](objc.ID(contextClass), objc.RegisterName("setCurrentContext:"), context)
	objc.Send[objc.ID](context, objc.RegisterName("setImageInterpolation:"), uint64(3))
	rect := nsRect{Width: float64(pixels), Height: float64(pixels)}
	// Source-copy rendering preserves the existing application icon instead of
	// applying template tint to its ring or mutating a shared AppIcon image.
	objc.Send[objc.ID](source, objc.RegisterName("drawInRect:fromRect:operation:fraction:"), rect, nsRect{}, uint64(1), float64(1))
	if recording {
		color := objc.Send[objc.ID](objc.ID(objc.GetClass("NSColor")), objc.RegisterName("colorWithSRGBRed:green:blue:alpha:"), float64(1), float64(59.0/255), float64(48.0/255), float64(1))
		path := objc.Send[objc.ID](objc.ID(objc.GetClass("NSBezierPath")), objc.RegisterName("bezierPathWithOvalInRect:"), statusCenterDotRect(float64(pixels)))
		if color == 0 || path == 0 {
			releaseNativeObject(rep)
			return 0
		}
		objc.Send[objc.ID](color, objc.RegisterName("setFill"))
		objc.Send[objc.ID](path, objc.RegisterName("fill"))
	}
	return rep
}

// A private NSImage owns 1x/2x bitmap copies. No shared AppIcon representation
// is recolored or resized, and the image remains non-template to retain red.
func newRecordingStatusIcon(source objc.ID) objc.ID {
	if source == 0 || !loadStatusIconDrawing() {
		return 0
	}
	size := foundation.NSSize{Width: statusMenuIconPointSize, Height: statusMenuIconPointSize}
	image := objc.Send[objc.ID](objc.Send[objc.ID](objc.ID(objc.GetClass("NSImage")), objc.RegisterName("alloc")), objc.RegisterName("initWithSize:"), size)
	if image == 0 {
		return 0
	}
	for _, pixels := range []int{statusMenuIconPointSize, statusMenuIconPointSize * 2} {
		rep := renderStatusIconBitmap(source, pixels, true)
		if rep == 0 {
			releaseNativeObject(image)
			return 0
		}
		objc.Send[objc.ID](rep, objc.RegisterName("setSize:"), size)
		objc.Send[objc.ID](image, objc.RegisterName("addRepresentation:"), rep)
		releaseNativeObject(rep)
	}
	objc.Send[objc.ID](image, objc.RegisterName("setTemplate:"), false)
	return image
}
func refreshStatusIcon() error {
	if statusBarButton == 0 {
		return nil
	}
	image := statusBarBaseImage
	if statusMeetingRecording && statusBarRecordingImage != 0 {
		image = statusBarRecordingImage
	}
	if image != 0 {
		objc.Send[objc.ID](statusBarButton, objc.RegisterName("setImage:"), image)
	}
	objc.Send[objc.ID](statusBarButton, objc.RegisterName("setAlphaValue:"), menuBarStatusIconAlpha(statusCaptureRunning, statusMeetingRecording))
	title := "Blur"
	if statusMeetingRecording {
		title = "Blur — Voice notes recording"
	}
	tooltip := newStatusIconString(title)
	objc.Send[objc.ID](statusBarButton, objc.RegisterName("setToolTip:"), tooltip)
	releaseNativeObject(tooltip)
	if statusMeetingRecording && statusBarRecordingImage == 0 {
		return errMenuBarItemUnavailable
	}
	return nil
}

func newStatusIconString(text string) objc.ID {
	// #nosec G103 -- NSString copies these immutable bytes synchronously; KeepAlive retains the Go string through that copy.
	value := objc.Send[objc.ID](objc.Send[objc.ID](objc.ID(objc.GetClass("NSString")), objc.RegisterName("alloc")), objc.RegisterName("initWithBytes:length:encoding:"), unsafe.StringData(text), len(text), uint64(4))
	runtime.KeepAlive(text)
	return value
}
