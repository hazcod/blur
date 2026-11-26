//go:build linux

package client

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/hazcod/blur/internal/platform/darwin"
	platformlinux "github.com/hazcod/blur/internal/platform/linux"
)

const runningAppsRefresh = 10 * time.Second

// AT-SPI calls cross process boundaries and can stall on a busy app, so the
// view reads a cached list and refreshes it in the background.
var linuxRunningApps struct {
	sync.Mutex
	apps    []darwin.Application
	fetched time.Time
	loading bool
}

func (d *desktop) runningCaptureApps() []darwin.Application {
	linuxRunningApps.Lock()
	defer linuxRunningApps.Unlock()
	if !linuxRunningApps.loading && time.Since(linuxRunningApps.fetched) > runningAppsRefresh {
		linuxRunningApps.loading = true
		go d.refreshRunningCaptureApps()
	}
	return slices.Clone(linuxRunningApps.apps)
}

func (d *desktop) refreshRunningCaptureApps() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	found, err := platformlinux.RunningApplications(ctx)
	apps := make([]darwin.Application, 0, len(found))
	for _, app := range found {
		apps = append(apps, darwin.Application{Name: app.Name, BundleID: app.ID})
	}
	linuxRunningApps.Lock()
	changed := err == nil && !slices.Equal(apps, linuxRunningApps.apps)
	if err == nil {
		linuxRunningApps.apps = apps
	}
	linuxRunningApps.fetched, linuxRunningApps.loading = time.Now(), false
	linuxRunningApps.Unlock()
	if changed && d != nil && d.app != nil {
		d.app.Post(d.app.RequestUpdate)
	}
}

// supportsApplicationChooser is false: Linux has no application bundle
// picker; apps are added from the running list.
func supportsApplicationChooser() bool { return false }
