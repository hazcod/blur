package darwin

import (
	"errors"
	"log/slog"
	"math"

	"github.com/ebitengine/purego/objc"
	"github.com/golang-gui/goui/platform/darwin/frameworks/appkit"
	"github.com/golang-gui/goui/platform/darwin/frameworks/foundation"
)

var errInvalidWindowSize = errors.New("darwin: invalid native window size")

const quickPanelCornerRadius = 16
const quickPanelMaskedCorners = uint64(3) // kCALayerMinXMinYCorner | kCALayerMaxXMinYCorner: lower edge only.

// Quick panel palette: graphite surface with a one-pixel hairline.
const (
	quickPanelSurfaceHex  = "#1B1D22"
	quickPanelHairlineHex = "#2E3139"
	// quickPanelHairlineBleed extends the hairline layer above the panel so its
	// own top border is clipped away; the panel top stays flush with the display.
	quickPanelHairlineBleed = 4.0
	// kCALayerWidthSizable | kCALayerHeightSizable keeps the hairline layer
	// tracking the content layer while the window frame animates.
	quickPanelHairlineAutoresize = uint32(2 | 16)
)

type nsRGB struct{ R, G, B float64 }

var (
	quickPanelSurfaceRGB  = nsRGB{27.0 / 255, 29.0 / 255, 34.0 / 255}
	quickPanelHairlineRGB = nsRGB{46.0 / 255, 49.0 / 255, 57.0 / 255}
)

type nsEdgeInsets struct {
	Top, Left, Bottom, Right float64
}

var quickPanelBackdrop struct {
	window    objc.ID
	content   objc.ID
	hairline  objc.ID
	installed bool
}

// ResizeWindowContent resizes and anchors Blur's transient Interact window to
// the physical top edge of the main display. Call on the AppKit main thread;
// dimensions are logical points, not backing pixels.
func ResizeWindowContent(handle uintptr, width, height float64) error {
	if handle == 0 || !validWindowContentSize(width, height) {
		return errInvalidWindowSize
	}
	window := appkit.NSWindow{NSObject: foundation.NSObject{ID: objc.ID(handle)}}
	window.SetContentSize(foundation.NSSize{Width: width, Height: height})
	prepareQuickPanelWindow(window)
	screenFrame, ok := mainScreenFrame()
	if !ok {
		window.Center()
		return nil
	}
	// Align the opaque surface with the display edge so it expands from and
	// visually incorporates the hardware notch, like a software island.
	windowFrame := window.Frame()
	x, y := quickPanelTopLeft(screenFrame.X, screenFrame.Y, screenFrame.Width, screenFrame.Height, float64(windowFrame.Size.Width), float64(windowFrame.Size.Height), 0)
	window.SetFrameTopLeftPoint(foundation.NSPoint{X: x, Y: y})
	// Reassert after moving/showing as AppKit can rebuild the frame/content
	// presentation when the borderless window is resized or reactivated.
	installQuickPanelBackdrop(window)
	return nil
}

// prepareQuickPanelWindow applies the window-level rules shared by every
// placement path.
func prepareQuickPanelWindow(window appkit.NSWindow) {
	// GoUI's ChromeNone removes the titlebar, but its Cocoa backend still
	// requests closable/miniaturizable window masks. On current macOS those
	// masks retain AppKit's rounded window silhouette. Interact is a flush
	// graphite island surface, so make this transient window truly borderless.
	objc.Send[objc.ID](window.ID, objc.RegisterName("setStyleMask:"), uint64(0))
	window.SetLevel(25)               // NSStatusWindowLevel: above the menu bar, below alerts.
	window.SetCollectionBehavior(337) // all spaces, stationary, fullscreen auxiliary.
}

// mainScreenFrame returns the main display frame in AppKit's bottom-left
// global coordinates.
func mainScreenFrame() (nsRect, bool) {
	screenID := objc.Send[objc.ID](objc.ID(objc.GetClass("NSScreen")), objc.RegisterName("mainScreen"))
	if screenID == 0 {
		return nsRect{}, false
	}
	screen := appkit.NSScreen{NSObject: foundation.NSObject{ID: screenID}}
	frame := screen.Frame()
	return nsRect{X: float64(frame.Origin.X), Y: float64(frame.Origin.Y), Width: float64(frame.Size.Width), Height: float64(frame.Size.Height)}, true
}

// PointerInsideWindow is a point-in-window check used only while Interact is
// visible. It reads the system pointer location, never the contents of another
// app or its event stream.
func PointerInsideWindow(handle uintptr) bool {
	if handle == 0 {
		return false
	}
	window := appkit.NSWindow{NSObject: foundation.NSObject{ID: objc.ID(handle)}}
	frame := window.Frame()
	eventClass := objc.ID(objc.GetClass("NSEvent"))
	if eventClass == 0 {
		return false
	}
	point := objc.Send[foundation.NSPoint](eventClass, objc.RegisterName("mouseLocation"))
	return point.X >= frame.Origin.X && point.X <= frame.Origin.X+frame.Size.Width && point.Y >= frame.Origin.Y && point.Y <= frame.Origin.Y+frame.Size.Height
}

// QuickPanelMaterialAvailable reports whether the native opaque backdrop is
// installed. The surface intentionally avoids NSVisualEffectView: GoUI paints
// an opaque surface over its content view, so a material beneath it is never
// visible, and vibrancy would turn the graphite surface grey.
func QuickPanelMaterialAvailable() bool { return quickPanelBackdrop.installed }

// QuickPanelSafeTopInset returns the main display's unobscured top inset in
// points. Interact keeps its opaque backdrop flush with the display edge while
// placing its composer below the camera housing.
func QuickPanelSafeTopInset() float64 {
	screenID := objc.Send[objc.ID](objc.ID(objc.GetClass("NSScreen")), objc.RegisterName("mainScreen"))
	if screenID == 0 {
		return 0
	}
	insets := objc.Send[nsEdgeInsets](screenID, objc.RegisterName("safeAreaInsets"))
	if insets.Top < 0 || math.IsNaN(insets.Top) || math.IsInf(insets.Top, 0) {
		return 0
	}
	return min(insets.Top, 56)
}

func installQuickPanelBackdrop(window appkit.NSWindow) {
	if window.ID == 0 {
		return
	}
	content := window.ContentView()
	if content.ID == 0 {
		return
	}
	alreadyInstalled := quickPanelBackdrop.window == window.ID && quickPanelBackdrop.content == content.ID && quickPanelBackdrop.installed
	quickPanelBackdrop.window = window.ID
	quickPanelBackdrop.content = content.ID

	// Keep the window clear so its rounded corners reveal the desktop, and make
	// its content layer exact sRGB graphite so the surface cannot pick up a
	// theme tint. The standard window shadow follows the rounded silhouette.
	clear := objc.Send[objc.ID](objc.ID(objc.GetClass("NSColor")), objc.RegisterName("clearColor"))
	objc.Send[objc.ID](window.ID, objc.RegisterName("setBackgroundColor:"), clear)
	objc.Send[objc.ID](window.ID, objc.RegisterName("setOpaque:"), false)
	objc.Send[objc.ID](window.ID, objc.RegisterName("setHasShadow:"), true)
	objc.Send[objc.ID](content.ID, objc.RegisterName("setWantsLayer:"), true)
	layer := objc.Send[objc.ID](content.ID, objc.RegisterName("layer"))
	if layer != 0 {
		objc.Send[objc.ID](layer, objc.RegisterName("setBackgroundColor:"), cgColor(quickPanelSurfaceRGB))
		objc.Send[objc.ID](layer, objc.RegisterName("setOpaque:"), false)
		objc.Send[objc.ID](layer, objc.RegisterName("setCornerRadius:"), float64(quickPanelCornerRadius))
		objc.Send[objc.ID](layer, objc.RegisterName("setMaskedCorners:"), quickPanelMaskedCorners)
		objc.Send[objc.ID](layer, objc.RegisterName("setMasksToBounds:"), true)
		if !alreadyInstalled || quickPanelBackdrop.hairline == 0 {
			quickPanelBackdrop.hairline = installQuickPanelHairline(layer)
		}
	}
	quickPanelBackdrop.installed = true
	if !alreadyInstalled {
		slog.Info("native Interact backdrop installed", "color", quickPanelSurfaceHex, "hairline", quickPanelHairlineHex, "corner_radius_points", quickPanelCornerRadius, "effect", "solid_graphite")
	}
}

// installQuickPanelHairline adds a one-pixel border on the left, right and
// lower edges. The border layer is taller than the surface so its top edge is
// clipped by the content layer, leaving the top flush with the display.
func installQuickPanelHairline(parent objc.ID) objc.ID {
	bounds := objc.Send[nsRect](parent, objc.RegisterName("bounds"))
	hairline := objc.Send[objc.ID](objc.ID(objc.GetClass("CALayer")), objc.RegisterName("layer"))
	if hairline == 0 {
		return 0
	}
	objc.Send[objc.ID](hairline, objc.RegisterName("setFrame:"), nsRect{X: 0, Y: 0, Width: bounds.Width, Height: bounds.Height + quickPanelHairlineBleed})
	objc.Send[objc.ID](hairline, objc.RegisterName("setAutoresizingMask:"), quickPanelHairlineAutoresize)
	objc.Send[objc.ID](hairline, objc.RegisterName("setBorderWidth:"), float64(1))
	objc.Send[objc.ID](hairline, objc.RegisterName("setBorderColor:"), cgColor(quickPanelHairlineRGB))
	objc.Send[objc.ID](hairline, objc.RegisterName("setCornerRadius:"), float64(quickPanelCornerRadius))
	objc.Send[objc.ID](hairline, objc.RegisterName("setMaskedCorners:"), quickPanelMaskedCorners)
	objc.Send[objc.ID](parent, objc.RegisterName("addSublayer:"), hairline)
	return hairline
}

func cgColor(c nsRGB) objc.ID {
	color := objc.Send[objc.ID](objc.ID(objc.GetClass("NSColor")), objc.RegisterName("colorWithSRGBRed:green:blue:alpha:"), c.R, c.G, c.B, float64(1))
	return objc.Send[objc.ID](color, objc.RegisterName("CGColor"))
}

func validWindowContentSize(width, height float64) bool {
	// Quick ask's compact one-line composer is about 56 points tall. Rejecting
	// that size makes the caller silently keep GoUI's centered default frame.
	return width >= 240 && width <= 1600 && height >= 40 && height <= 1200 && !math.IsNaN(width) && !math.IsNaN(height) && !math.IsInf(width, 0) && !math.IsInf(height, 0)
}

func quickPanelTopLeft(screenX, screenY, screenWidth, screenHeight, windowWidth, _, topGap float64) (float64, float64) {
	return screenX + (screenWidth-windowWidth)/2, screenY + screenHeight - topGap
}

// quickPanelFrameOrigin returns the bottom-left origin of a panel of the given
// size whose top edge sits topGap points below the top of the screen frame and
// which is horizontally centred. AppKit's coordinates grow upward, so the top
// edge stays fixed while a taller panel extends downward.
func quickPanelFrameOrigin(screen nsRect, width, height, topGap float64) (float64, float64) {
	x, topY := quickPanelTopLeft(screen.X, screen.Y, screen.Width, screen.Height, width, height, topGap)
	return x, topY - height
}
