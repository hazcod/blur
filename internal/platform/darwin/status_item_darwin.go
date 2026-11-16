package darwin

import (
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/ebitengine/purego/objc"
	"github.com/golang-gui/goui/platform/darwin/frameworks/foundation"
)

const (
	statusMenuIconSelector  = "imageNamed:"
	statusMenuIconName      = "AppIcon"
	statusMenuIconPointSize = 20
)

type statusMenuTarget uint8

const (
	statusTargetNone statusMenuTarget = iota
	statusTargetInteract
	statusTargetMeeting
	statusTargetMeetingStop
	statusTargetSettings
	statusTargetCapture
	statusTargetPause
	statusTargetQuit
)

// PauseUntilTomorrow is the sentinel duration the "Until tomorrow" menu item
// passes to the pause callback; the caller resolves it against the local clock.
const PauseUntilTomorrow time.Duration = -1

// statusPauseOptions is the Go-side table the "Pause for" menu items index via
// NSMenuItem tag.
var statusPauseOptions = []struct {
	title    string
	duration time.Duration
}{
	{"15 minutes", 15 * time.Minute},
	{"1 hour", time.Hour},
	{"Until tomorrow", PauseUntilTomorrow},
}

type statusMenuItem struct {
	title     string
	selector  string
	target    statusMenuTarget
	enabled   bool
	separator bool
	hidden    bool
	tag       int
	submenu   []statusMenuItem
}

var (
	errMenuBarItemUnavailable = errors.New("darwin: menu bar item unavailable")
	statusBarItem             objc.ID
	statusBarMenu             objc.ID
	statusMenuTrackingTimer   objc.ID
	statusMenuCommandHeld     bool
	statusCaptureItem         objc.ID
	statusMeetingItem         objc.ID
	statusMeetingStopItem     objc.ID
	statusOpenMeeting         func()
	statusMeetingControls     MeetingMenuControls
	statusMeetingRecording    bool
	statusCaptureToggleItem   objc.ID
	statusPauseItem           objc.ID
	statusStateTitle          string
	statusCaptureDetail       string
	statusPausedUntil         time.Time
	statusPauseFor            func(time.Duration)
	statusQuit                func()
	statusBarButton           objc.ID
	statusBarBaseImage        objc.ID
	statusBarRecordingImage   objc.ID
	statusBar                 objc.ID
	statusCaptureRunning      bool
	statusCaptureActiveApp    bool
	statusQuickCapture        func()
	statusOpenSettings        func()
	statusToggleCapture       func()
	statusTargetOnce          sync.Once
	statusTargetObject        objc.ID
	statusTargetError         error
)

// captureStateTitle is the disabled status line at the top of the menu.
func captureStateTitle(captureRunning bool, pausedUntil, now time.Time) string {
	if captureRunning {
		return "Capture is on"
	}
	if pausedUntil.IsZero() {
		return "Capture is paused"
	}
	until := pausedUntil.In(now.Location())
	clock := until.Format("15:04")
	ny, nm, nd := now.Date()
	uy, um, ud := until.Date()
	switch {
	case ny == uy && nm == um && nd == ud:
		return "Capture paused until " + clock
	case time.Date(ny, nm, nd+1, 12, 0, 0, 0, now.Location()).Format("2006-01-02") == time.Date(uy, um, ud, 12, 0, 0, 0, now.Location()).Format("2006-01-02"):
		return "Capture paused until tomorrow " + clock
	default:
		return "Capture paused until " + until.Format("Mon 15:04")
	}
}

func pauseSubmenu() []statusMenuItem {
	items := make([]statusMenuItem, 0, len(statusPauseOptions))
	for i, option := range statusPauseOptions {
		items = append(items, statusMenuItem{title: option.title, selector: "pauseFor:", target: statusTargetPause, enabled: true, tag: i})
	}
	return items
}

func menuBarItems(captureRunning bool, pausedUntil, now time.Time, pauseAvailable bool) []statusMenuItem {
	action := "Resume capture"
	if captureRunning {
		action = "Pause capture"
	}
	return []statusMenuItem{
		{title: captureStateTitle(captureRunning, pausedUntil, now)},
		{separator: true},
		{title: "Interact", selector: "openInteract:", target: statusTargetInteract, enabled: true},
		{title: "Settings", selector: "openSettings:", target: statusTargetSettings, enabled: true},
		{separator: true},
		{title: "Pause for", enabled: true, hidden: !captureRunning || !pauseAvailable, submenu: pauseSubmenu()},
		{title: action, selector: "toggleCapture:", target: statusTargetCapture, enabled: true},
		{separator: true},
		{title: "Quit Blur", selector: "quitBlur:", target: statusTargetQuit, enabled: true},
	}
}

func meetingMenuTitle(recording bool) string {
	if recording {
		return "Pause recording"
	}
	if statusMeetingControls.Active && statusMeetingControls.Paused {
		return "Resume recording"
	}
	return "Voice notes"
}

func menuBarItemsWithMeetings(captureRunning bool, pausedUntil, now time.Time, pauseAvailable bool) []statusMenuItem {
	items := menuBarItems(captureRunning, pausedUntil, now, pauseAvailable)
	items[0].title = menuBarStatusTitle(statusMeetingRecording, captureRunning, statusCaptureActiveApp, pausedUntil, now)
	meeting := statusMenuItem{title: meetingMenuTitle(statusMeetingRecording), selector: "openMeeting:", target: statusTargetMeeting, enabled: statusOpenMeeting != nil && !statusMeetingControls.Busy}
	stop := statusMenuItem{title: "Stop recording", selector: "stopMeeting:", target: statusTargetMeetingStop, enabled: statusMeetingControls.Stop != nil && !statusMeetingControls.Busy, hidden: !statusMeetingControls.Active}
	items = append(items, statusMenuItem{}, statusMenuItem{})
	copy(items[5:], items[3:])
	items[3] = meeting
	items[4] = stop
	return items
}

// ConfigureMeetingMenu sets the workspace action independently of capture/menu
// reconstruction. Call on the AppKit main thread; the action owns recording policy.
func ConfigureMeetingMenu(open func(), recording bool, controls ...MeetingMenuControls) error {
	statusOpenMeeting, statusMeetingRecording = open, recording
	statusMeetingControls = MeetingMenuControls{Active: recording}
	if len(controls) > 0 {
		statusMeetingControls = controls[0]
	}
	iconErr := refreshStatusIcon()
	refreshStatusCaptureTitle()
	if statusMeetingItem == 0 {
		return iconErr
	}
	title := foundation.ToNSString(meetingMenuTitle(recording)).ID
	objc.Send[objc.ID](statusMeetingItem, objc.RegisterName("setTitle:"), title)
	releaseNativeObject(title)
	objc.Send[objc.ID](statusMeetingItem, objc.RegisterName("setEnabled:"), boolToObjCBool(open != nil && !statusMeetingControls.Busy))
	if statusMeetingStopItem != 0 {
		objc.Send[objc.ID](statusMeetingStopItem, objc.RegisterName("setHidden:"), boolToObjCBool(!statusMeetingControls.Active))
		objc.Send[objc.ID](statusMeetingStopItem, objc.RegisterName("setEnabled:"), boolToObjCBool(statusMeetingControls.Stop != nil && !statusMeetingControls.Busy))
	}
	return iconErr
}

func menuBarIconAlpha(captureRunning bool) float64 {
	if captureRunning {
		return 1
	}
	return 0.48
}

func menuBarCapturePresentation(running, activeApp bool, pausedUntil, now time.Time, detail ...string) (float64, string) {
	title := captureStateTitle(running, pausedUntil, now)
	// Omit the argument entirely for the compact menu. An explicitly requested
	// empty detail still describes why an active capture worker has no content.
	if running && len(detail) > 0 {
		if detail[0] != "" {
			title += "; " + detail[0]
		} else if !activeApp {
			title += "; waiting for readable content"
		}
	}
	return menuBarIconAlpha(running), title
}

func menuBarStatusTitle(recording, running, activeApp bool, pausedUntil, now time.Time, detail ...string) string {
	_, title := menuBarCapturePresentation(running, activeApp, pausedUntil, now, detail...)
	if !recording {
		return title
	}
	if running {
		title = strings.Replace(title, "Capture is on", "Capturing", 1)
	} else {
		title = strings.Replace(title, "Capture is paused", "Capture paused", 1)
	}
	return "Recording - " + title
}

// ConfigureMenuBarItem creates or removes Blur's native AppKit status item.
// Call on the main OS thread. The callback must return promptly; it is invoked
// by AppKit and should enqueue UI work on the GoUI event loop.
func ConfigureMenuBarItem(enabled bool, openQuickCapture, openSettings, toggleCapture func(), pauseFor func(time.Duration), quit func()) error {
	removeMenuBarItem()
	if !enabled {
		return nil
	}
	if openQuickCapture == nil || openSettings == nil || toggleCapture == nil {
		return errMenuBarItemUnavailable
	}
	appClass := objc.GetClass("NSApplication")
	barClass := objc.GetClass("NSStatusBar")
	menuClass := objc.GetClass("NSMenu")
	itemClass := objc.GetClass("NSMenuItem")
	if appClass == 0 || barClass == 0 || menuClass == 0 || itemClass == 0 {
		return errMenuBarItemUnavailable
	}
	app := objc.Send[objc.ID](objc.ID(appClass), objc.RegisterName("sharedApplication"))
	bar := objc.Send[objc.ID](objc.ID(barClass), objc.RegisterName("systemStatusBar"))
	if app == 0 || bar == 0 {
		return errMenuBarItemUnavailable
	}
	actionTarget, err := getStatusMenuTarget(openQuickCapture, openSettings, toggleCapture, pauseFor, quit)
	if err != nil {
		return errMenuBarItemUnavailable
	}
	item := objc.Send[objc.ID](bar, objc.RegisterName("statusItemWithLength:"), float64(-1))
	if item == 0 {
		return errMenuBarItemUnavailable
	}
	// statusItemWithLength: returns an autoreleased item; keep it until removal.
	objc.Send[objc.ID](item, objc.RegisterName("retain"))
	discardItem := func() {
		objc.Send[objc.ID](bar, objc.RegisterName("removeStatusItem:"), item)
		releaseNativeObject(item)
	}
	menu := newNativeMenu(menuClass, "")
	if menu == 0 {
		discardItem()
		return errMenuBarItemUnavailable
	}
	built, ok := addStatusMenuItems(menu, objc.ID(itemClass), objc.ID(menuClass), menuBarItemsWithMeetings(statusCaptureRunning, statusPausedUntil, time.Now(), statusPauseFor != nil), actionTarget)
	if !ok {
		releaseNativeObject(menu)
		discardItem()
		return errMenuBarItemUnavailable
	}
	button := objc.Send[objc.ID](item, objc.RegisterName("button"))
	if button == 0 {
		releaseNativeObject(menu)
		discardItem()
		return errMenuBarItemUnavailable
	}
	iconName := foundation.ToNSString(statusMenuIconName).ID
	image := objc.Send[objc.ID](objc.ID(objc.GetClass("NSImage")), objc.RegisterName(statusMenuIconSelector), iconName)
	releaseNativeObject(iconName)
	if image == 0 {
		image = objc.Send[objc.ID](app, objc.RegisterName("applicationIconImage"))
	}
	if image != 0 {
		// Both lookups return shared images; resize a private copy so the
		// Dock/application icon is not mutated.
		image = objc.Send[objc.ID](image, objc.RegisterName("copy"))
	}
	if image == 0 {
		nativeTitle := foundation.ToNSString("Blur").ID
		objc.Send[objc.ID](button, objc.RegisterName("setTitle:"), nativeTitle)
		releaseNativeObject(nativeTitle)
	} else {
		objc.Send[objc.ID](image, objc.RegisterName("setSize:"), foundation.NSSize{Width: statusMenuIconPointSize, Height: statusMenuIconPointSize})
		objc.Send[objc.ID](image, objc.RegisterName("setTemplate:"), false)
		statusBarBaseImage = image // keep a private original for pause/stop restoration
		statusBarRecordingImage = newRecordingStatusIcon(image)
		nativeTooltip := foundation.ToNSString("Blur").ID
		objc.Send[objc.ID](button, objc.RegisterName("setToolTip:"), nativeTooltip)
		releaseNativeObject(nativeTooltip)
	}
	objc.Send[objc.ID](button, objc.RegisterName("setAlphaValue:"), menuBarStatusIconAlpha(statusCaptureRunning, statusMeetingRecording))
	objc.Send[objc.ID](menu, objc.RegisterName("setDelegate:"), actionTarget)
	objc.Send[objc.ID](item, objc.RegisterName("setMenu:"), menu)
	statusBarMenu = menu // owned by the status item
	releaseNativeObject(menu)
	statusBar, statusBarItem, statusCaptureItem, statusCaptureToggleItem, statusPauseItem, statusBarButton = bar, item, built.state, built.toggle, built.pause, button
	statusMeetingItem = built.meeting
	statusMeetingStopItem = built.meetingStop
	statusStateTitle = ""
	refreshStatusState()
	return refreshStatusIcon()
}

type builtStatusItems struct{ state, toggle, pause, meeting, meetingStop objc.ID }

// addStatusMenuItems appends descriptors (and nested submenus) to menu.
func addStatusMenuItems(menu, itemClass, menuClass objc.ID, descriptors []statusMenuItem, actionTarget objc.ID) (builtStatusItems, bool) {
	var built builtStatusItems
	for _, descriptor := range descriptors {
		var menuItem objc.ID
		if descriptor.separator {
			menuItem = objc.Send[objc.ID](itemClass, objc.RegisterName("separatorItem"))
		} else {
			menuItem = newNativeMenuItem(objc.Class(itemClass), descriptor.title, selectorOrZero(descriptor.selector), "")
		}
		if menuItem == 0 {
			return built, false
		}
		if !descriptor.separator {
			switch descriptor.target {
			case statusTargetInteract, statusTargetMeeting, statusTargetMeetingStop, statusTargetSettings, statusTargetCapture, statusTargetPause, statusTargetQuit:
				objc.Send[objc.ID](menuItem, objc.RegisterName("setTarget:"), actionTarget)
			}
			objc.Send[objc.ID](menuItem, objc.RegisterName("setEnabled:"), boolToObjCBool(descriptor.enabled))
			if descriptor.selector == "pauseFor:" {
				objc.Send[objc.ID](menuItem, objc.RegisterName("setTag:"), descriptor.tag)
			}
			if descriptor.hidden {
				objc.Send[objc.ID](menuItem, objc.RegisterName("setHidden:"), boolToObjCBool(true))
			}
			if len(descriptor.submenu) > 0 {
				sub := newNativeMenu(objc.Class(menuClass), descriptor.title)
				if sub == 0 {
					releaseNativeObject(menuItem)
					return built, false
				}
				if _, ok := addStatusMenuItems(sub, itemClass, menuClass, descriptor.submenu, actionTarget); !ok {
					releaseNativeObject(sub)
					releaseNativeObject(menuItem)
					return built, false
				}
				objc.Send[objc.ID](menuItem, objc.RegisterName("setSubmenu:"), sub)
				releaseNativeObject(sub)
				built.pause = menuItem
			}
			switch descriptor.selector {
			case "":
				if len(descriptor.submenu) == 0 {
					built.state = menuItem
				}
			case "toggleCapture:":
				built.toggle = menuItem
			case "openMeeting:":
				built.meeting = menuItem
			case "stopMeeting:":
				built.meetingStop = menuItem
			}
		}
		objc.Send[objc.ID](menu, objc.RegisterName("addItem:"), menuItem)
		if !descriptor.separator {
			if descriptor.selector == "" || descriptor.selector == "toggleCapture:" || descriptor.selector == "openMeeting:" || descriptor.selector == "stopMeeting:" || len(descriptor.submenu) > 0 {
				objc.Send[objc.ID](menuItem, objc.RegisterName("retain")) // kept by the package-level handle
			}
			releaseNativeObject(menuItem)
		}
	}
	return built, true
}

func getStatusMenuTarget(openQuickCapture, openSettings, toggleCapture func(), pauseFor func(time.Duration), quit func()) (objc.ID, error) {
	statusTargetOnce.Do(func() {
		superclass := objc.GetClass("NSObject")
		if superclass == 0 {
			statusTargetError = errMenuBarItemUnavailable
			return
		}
		class, err := objc.RegisterClass("BlurStatusMenuTarget", superclass, nil, nil, []objc.MethodDef{{
			Cmd: objc.RegisterName("openInteract:"),
			Fn: func(_ objc.ID, _ objc.SEL, _ objc.ID) {
				dispatchStatusInteract()
			},
		}, {
			Cmd: objc.RegisterName("openMeeting:"),
			Fn:  func(_ objc.ID, _ objc.SEL, _ objc.ID) { dispatchStatusMeeting() },
		}, {
			Cmd: objc.RegisterName("stopMeeting:"),
			Fn:  func(_ objc.ID, _ objc.SEL, _ objc.ID) { dispatchStatusMeetingStop() },
		}, {
			Cmd: objc.RegisterName("openSettings:"),
			Fn:  func(_ objc.ID, _ objc.SEL, _ objc.ID) { dispatchStatusSettings() },
		}, {
			Cmd: objc.RegisterName("toggleCapture:"),
			Fn:  func(_ objc.ID, _ objc.SEL, _ objc.ID) { dispatchStatusCaptureToggle() },
		}, {
			Cmd: objc.RegisterName("pauseFor:"),
			Fn: func(_ objc.ID, _ objc.SEL, sender objc.ID) {
				if sender != 0 {
					dispatchStatusPause(objc.Send[int](sender, objc.RegisterName("tag")))
				}
			},
		}, {
			Cmd: objc.RegisterName("menuWillOpen:"),
			Fn: func(_ objc.ID, _ objc.SEL, menu objc.ID) {
				if menu == statusBarMenu && menu != 0 {
					startStatusMenuTracking()
				}
			},
		}, {
			Cmd: objc.RegisterName("menuDidClose:"),
			Fn: func(_ objc.ID, _ objc.SEL, menu objc.ID) {
				if menu == statusBarMenu && menu != 0 {
					stopStatusMenuTracking()
					refreshStatusCaptureTitle()
				}
			},
		}, {
			Cmd: objc.RegisterName("refreshCaptureDetail:"),
			Fn: func(_ objc.ID, _ objc.SEL, timer objc.ID) {
				if timer == statusMenuTrackingTimer && timer != 0 {
					held := CommandKeyHeld()
					if held != statusMenuCommandHeld {
						statusMenuCommandHeld = held
						refreshStatusCaptureTitle()
					}
				}
			},
		}, {
			Cmd: objc.RegisterName("quitBlur:"),
			Fn:  func(_ objc.ID, _ objc.SEL, sender objc.ID) { dispatchStatusQuit(sender) },
		}})
		if err != nil {
			statusTargetError = err
			return
		}
		statusTargetObject = objc.Send[objc.ID](objc.Send[objc.ID](objc.ID(class), objc.RegisterName("alloc")), objc.RegisterName("init"))
		if statusTargetObject == 0 {
			statusTargetError = errMenuBarItemUnavailable
		}
	})
	if statusTargetError != nil || statusTargetObject == 0 {
		return 0, errMenuBarItemUnavailable
	}
	statusQuickCapture = openQuickCapture
	statusOpenSettings = openSettings
	statusToggleCapture = toggleCapture
	statusPauseFor = pauseFor
	statusQuit = quit
	return statusTargetObject, nil
}

func dispatchStatusInteract() {
	if statusQuickCapture != nil {
		slog.Info("menu bar Interact selected")
		invokeStatusCallback("Interact", statusQuickCapture)
	}
}

// Keep the old internal name as a thin alias for existing callers.
func dispatchStatusQuickCapture() { dispatchStatusInteract() }

func dispatchStatusMeeting() {
	if statusOpenMeeting != nil {
		invokeStatusCallback("Voice notes", statusOpenMeeting)
	}
}

func dispatchStatusMeetingStop() {
	if statusMeetingControls.Active && !statusMeetingControls.Busy && statusMeetingControls.Stop != nil {
		invokeStatusCallback("Stop recording", statusMeetingControls.Stop)
	}
}

func dispatchStatusSettings() {
	if statusOpenSettings != nil {
		slog.Info("menu bar Settings selected")
		invokeStatusCallback("Settings", statusOpenSettings)
	}
}

func dispatchStatusCaptureToggle() {
	if statusToggleCapture != nil {
		slog.Info("menu bar capture toggle selected")
		invokeStatusCallback("capture toggle", statusToggleCapture)
	}
}

func dispatchStatusPause(tag int) {
	if statusPauseFor == nil || tag < 0 || tag >= len(statusPauseOptions) {
		return
	}
	duration := statusPauseOptions[tag].duration
	slog.Info("menu bar timed pause selected", "option", statusPauseOptions[tag].title)
	invokeStatusCallback("timed pause", func() { statusPauseFor(duration) })
}

// dispatchStatusQuit lets Blur cancel timed-pause state, then terminates.
func dispatchStatusQuit(sender objc.ID) {
	if statusQuit != nil {
		invokeStatusCallback("quit", statusQuit)
	}
	app := objc.Send[objc.ID](objc.ID(objc.GetClass("NSApplication")), objc.RegisterName("sharedApplication"))
	if app != 0 {
		objc.Send[objc.ID](app, objc.RegisterName("terminate:"), sender)
	}
}

func invokeStatusCallback(name string, callback func()) {
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Error("menu bar callback panicked", "action", name, "panic_type", fmt.Sprintf("%T", recovered), "stack", string(debug.Stack()))
		}
	}()
	callback()
}

// UpdateMenuBarCaptureState updates the status text without recreating AppKit objects.
// Call on the main OS thread.
func UpdateMenuBarCaptureState(captureRunning, activeAppCapturing bool, pausedUntil time.Time, detail ...string) {
	runningChanged := statusCaptureRunning != captureRunning
	activeChanged := statusCaptureActiveApp != activeAppCapturing
	statusCaptureRunning = captureRunning
	statusCaptureActiveApp = activeAppCapturing
	statusPausedUntil = pausedUntil
	statusCaptureDetail = ""
	if len(detail) > 0 {
		statusCaptureDetail = detail[0]
	}
	if statusCaptureItem == 0 {
		return
	}
	if runningChanged && statusCaptureToggleItem != 0 {
		action := "Resume capture"
		if captureRunning {
			action = "Pause capture"
		}
		nativeAction := foundation.ToNSString(action).ID
		objc.Send[objc.ID](statusCaptureToggleItem, objc.RegisterName("setTitle:"), nativeAction)
		releaseNativeObject(nativeAction)
	}

	refreshStatusState()
	if runningChanged || activeChanged {
		slog.Debug("menu bar capture indicator changed", "worker_running", captureRunning, "active_app_capturing", activeAppCapturing)
	}
}

func refreshStatusState() {
	_ = refreshStatusIcon()
	if statusCaptureItem == 0 {
		return
	}
	if statusPauseItem != 0 {
		objc.Send[objc.ID](statusPauseItem, objc.RegisterName("setHidden:"), boolToObjCBool(!statusCaptureRunning || statusPauseFor == nil))
	}
	refreshStatusCaptureTitle()
}

func refreshStatusCaptureTitle() {
	if statusCaptureItem == 0 {
		return
	}
	var detail []string
	if statusMenuCommandHeld {
		detail = []string{statusCaptureDetail}
	}
	title := menuBarStatusTitle(statusMeetingRecording, statusCaptureRunning, statusCaptureActiveApp, statusPausedUntil, time.Now(), detail...)
	if title == statusStateTitle {
		return
	}
	statusStateTitle = title
	nativeText := foundation.ToNSString(title).ID
	defer releaseNativeObject(nativeText)
	objc.Send[objc.ID](statusCaptureItem, objc.RegisterName("setTitle:"), nativeText)
}

// A menu runs a nested tracking loop, which does not deliver local NSEvent
// monitors. Read only the current modifier flags while that menu is open.
// No key events or background monitoring are retained.
func startStatusMenuTracking() {
	stopStatusMenuTracking()
	statusMenuCommandHeld = CommandKeyHeld()
	refreshStatusCaptureTitle()
	timer := objc.Send[objc.ID](objc.ID(objc.GetClass("NSTimer")), objc.RegisterName("timerWithTimeInterval:target:selector:userInfo:repeats:"),
		float64(1.0/30), statusTargetObject, objc.RegisterName("refreshCaptureDetail:"), objc.ID(0), true)
	if timer == 0 {
		return
	}
	objc.Send[objc.ID](timer, objc.RegisterName("retain"))
	statusMenuTrackingTimer = timer
	runLoop := objc.Send[objc.ID](objc.ID(objc.GetClass("NSRunLoop")), objc.RegisterName("mainRunLoop"))
	mode := foundation.ToNSString("NSEventTrackingRunLoopMode").ID
	objc.Send[objc.ID](runLoop, objc.RegisterName("addTimer:forMode:"), timer, mode)
	releaseNativeObject(mode)
}

func stopStatusMenuTracking() {
	if statusMenuTrackingTimer != 0 {
		objc.Send[objc.ID](statusMenuTrackingTimer, objc.RegisterName("invalidate"))
		releaseNativeObject(statusMenuTrackingTimer)
		statusMenuTrackingTimer = 0
	}
	statusMenuCommandHeld = false
}

func selectorOrZero(name string) objc.SEL {
	if name == "" {
		return 0
	}
	return objc.RegisterName(name)
}

func boolToObjCBool(value bool) uintptr {
	if value {
		return 1
	}
	return 0
}

func removeMenuBarItem() {
	stopStatusMenuTracking()
	if statusBarMenu != 0 {
		objc.Send[objc.ID](statusBarMenu, objc.RegisterName("setDelegate:"), objc.ID(0))
		statusBarMenu = 0
	}
	if statusBar != 0 && statusBarItem != 0 {
		objc.Send[objc.ID](statusBar, objc.RegisterName("removeStatusItem:"), statusBarItem)
		releaseNativeObject(statusBarItem)
	}
	releaseNativeObject(statusCaptureItem)
	releaseNativeObject(statusCaptureToggleItem)
	releaseNativeObject(statusPauseItem)
	releaseNativeObject(statusMeetingItem)
	releaseNativeObject(statusMeetingStopItem)
	releaseNativeObject(statusBarBaseImage)
	releaseNativeObject(statusBarRecordingImage)
	statusBarBaseImage, statusBarRecordingImage = 0, 0
	statusMeetingItem, statusMeetingStopItem = 0, 0
	statusBar, statusBarItem, statusCaptureItem, statusCaptureToggleItem, statusPauseItem, statusBarButton = 0, 0, 0, 0, 0, 0
	statusStateTitle = ""
	statusQuickCapture, statusOpenSettings, statusToggleCapture, statusPauseFor, statusQuit = nil, nil, nil, nil, nil
}
