package darwin

import (
	"errors"
	"strings"

	"github.com/ebitengine/purego/objc"
	"github.com/golang-gui/goui/platform/darwin/frameworks/foundation"
)

const menuCommandModifier uintptr = 1 << 20 // NSEventModifierFlagCommand

var errMainMenuUnavailable = errors.New("darwin: application menu unavailable")

type applicationMenuItem struct {
	title             string
	selector          string
	key               string
	modifiers         uintptr
	targetApplication bool
}

func applicationMenuItems(appName string) []applicationMenuItem {
	appName = strings.TrimSpace(appName)
	if appName == "" || len(appName) > 80 {
		return nil
	}
	return []applicationMenuItem{
		{title: "About " + appName, selector: "orderFrontStandardAboutPanel:", targetApplication: true},
		// AppKit resolves a nil-target action through the responder chain. NSWindow
		// handles performClose:, which reaches GoUI's close veto and hides the
		// window without stopping the menu-bar capture process.
		{title: "Close Window", selector: "performClose:", key: "q", modifiers: menuCommandModifier},
		// Quit remains an explicit action; Cmd+Q is intentionally Close Window.
		{title: "Quit " + appName, selector: "terminate:", targetApplication: true},
	}
}

// InstallApplicationMenu installs a small AppKit-owned app menu. Native menu
// key equivalents are dispatched by NSApplication before window shortcuts.
// Call on the main OS thread after AppKit initialization.
func InstallApplicationMenu(appName string) error {
	items := applicationMenuItems(appName)
	if len(items) == 0 {
		return errMainMenuUnavailable
	}
	appClass := objc.GetClass("NSApplication")
	menuClass := objc.GetClass("NSMenu")
	itemClass := objc.GetClass("NSMenuItem")
	if appClass == 0 || menuClass == 0 || itemClass == 0 {
		return errMainMenuUnavailable
	}
	app := objc.Send[objc.ID](objc.ID(appClass), objc.RegisterName("sharedApplication"))
	mainMenu := newNativeMenu(menuClass, "")
	appMenu := newNativeMenu(menuClass, appName)
	defer releaseNativeObject(mainMenu)
	defer releaseNativeObject(appMenu)
	if app == 0 || mainMenu == 0 || appMenu == 0 {
		return errMainMenuUnavailable
	}
	rootItem := newNativeMenuItem(itemClass, appName, 0, "")
	defer releaseNativeObject(rootItem)
	if rootItem == 0 {
		return errMainMenuUnavailable
	}
	objc.Send[objc.ID](rootItem, objc.RegisterName("setSubmenu:"), appMenu)
	objc.Send[objc.ID](mainMenu, objc.RegisterName("addItem:"), rootItem)
	for _, descriptor := range items {
		item := newNativeMenuItem(itemClass, descriptor.title, objc.RegisterName(descriptor.selector), descriptor.key)
		if item == 0 {
			return errMainMenuUnavailable
		}
		defer releaseNativeObject(item)
		if descriptor.targetApplication {
			objc.Send[objc.ID](item, objc.RegisterName("setTarget:"), app)
		}
		if descriptor.modifiers != 0 {
			objc.Send[objc.ID](item, objc.RegisterName("setKeyEquivalentModifierMask:"), descriptor.modifiers)
		}
		objc.Send[objc.ID](appMenu, objc.RegisterName("addItem:"), item)
	}
	objc.Send[objc.ID](app, objc.RegisterName("setMainMenu:"), mainMenu)
	return nil
}

func newNativeMenu(class objc.Class, title string) objc.ID {
	object := objc.Send[objc.ID](objc.ID(class), objc.RegisterName("alloc"))
	nativeTitle := foundation.ToNSString(title).ID
	defer releaseNativeObject(nativeTitle)
	return objc.Send[objc.ID](object, objc.RegisterName("initWithTitle:"), nativeTitle)
}

func newNativeMenuItem(class objc.Class, title string, action objc.SEL, key string) objc.ID {
	object := objc.Send[objc.ID](objc.ID(class), objc.RegisterName("alloc"))
	nativeTitle := foundation.ToNSString(title).ID
	nativeKey := foundation.ToNSString(key).ID
	defer releaseNativeObject(nativeTitle)
	defer releaseNativeObject(nativeKey)
	return objc.Send[objc.ID](object, objc.RegisterName("initWithTitle:action:keyEquivalent:"), nativeTitle, action, nativeKey)
}

func releaseNativeObject(object objc.ID) {
	if object != 0 {
		objc.Send[objc.ID](object, objc.RegisterName("release"))
	}
}
