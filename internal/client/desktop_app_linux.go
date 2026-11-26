//go:build linux

package client

import (
	"errors"
	"sync"

	"github.com/golang-gui/goui/ui"
)

// errDesktopAppUnavailable reports a native service requested before the
// desktop shell started or after it stopped.
var errDesktopAppUnavailable = errors.New("client: desktop app unavailable")

// The X11 shell provides clipboard, URL and file-dialog services through the
// running goui app rather than package-level native handles, as on macOS.
var linuxDesktop struct {
	sync.Mutex
	app ui.App
}

func rememberDesktopApp(app ui.App) {
	linuxDesktop.Lock()
	linuxDesktop.app = app
	linuxDesktop.Unlock()
}

func currentDesktopApp() (ui.App, error) {
	linuxDesktop.Lock()
	defer linuxDesktop.Unlock()
	if linuxDesktop.app == nil {
		return nil, errDesktopAppUnavailable
	}
	return linuxDesktop.app, nil
}
