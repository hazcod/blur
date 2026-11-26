//go:build linux

package linux

import (
	"context"
	"errors"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/godbus/dbus/v5"
)

const (
	a11yDesktopRoot       = dbus.ObjectPath("/org/a11y/atspi/accessible/root")
	a11yAccessibleIface   = "org.a11y.atspi.Accessible"
	maxListedApplications = 64
)

// Application is a running app that capture can read, identified the same way
// capture events identify it: the lowercased executable name.
type Application struct {
	Name string
	ID   string
}

// RunningApplications lists the current user's apps registered with AT-SPI.
// It reads only each app's accessible name and process executable name, and
// skips Blur itself and processes owned by other users.
func RunningApplications(ctx context.Context) ([]Application, error) {
	conn, cancel, err := connectATSPIDBus(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	defer func() { _ = conn.Close() }()
	var children []atspiObjectRef
	var raw [][]any
	if err := conn.Object(a11yRegistry, a11yDesktopRoot).CallWithContext(ctx, a11yAccessibleIface+".GetChildren", 0).Store(&raw); err != nil {
		return nil, errors.New("AT-SPI application list unavailable")
	}
	for _, item := range raw {
		if len(item) != 2 {
			continue
		}
		bus, okBus := item[0].(string)
		path, okPath := item[1].(dbus.ObjectPath)
		if okBus && okPath && validATSPISender(bus) {
			children = append(children, atspiObjectRef{busName: bus, path: path})
		}
		if len(children) == maxListedApplications {
			break
		}
	}
	var apps []Application
	for _, child := range children {
		if ctx.Err() != nil {
			break
		}
		id, elevated, err := atspiProcessMetadata(ctx, conn, child.busName)
		if err != nil || elevated || isPrivateLinuxApp(id) || desktopInfrastructure(id) || slices.ContainsFunc(apps, func(app Application) bool { return app.ID == id }) {
			continue
		}
		apps = append(apps, Application{Name: applicationName(ctx, conn, child, id), ID: id})
	}
	return apps, nil
}

func applicationName(ctx context.Context, conn *dbus.Conn, ref atspiObjectRef, fallback string) string {
	var variant dbus.Variant
	err := conn.Object(ref.busName, ref.path).CallWithContext(ctx, "org.freedesktop.DBus.Properties.Get", 0, a11yAccessibleIface, "Name").Store(&variant)
	if err != nil {
		return fallback
	}
	name, ok := variant.Value().(string)
	name = strings.TrimSpace(name)
	if !ok || name == "" || len(name) > 128 || !utf8.ValidString(name) {
		return fallback
	}
	return name
}

// desktopInfrastructure reports processes that register with AT-SPI but are
// not apps a person would choose: desktop chrome, portals, and
// xdg-dbus-proxy. Flatpak apps currently share that executable identity, so
// offering it would silently select every Flatpak app. It affects only the
// picker.
func desktopInfrastructure(id string) bool {
	return transientDesktopFocus(id) || id == "xdg-dbus-proxy" || strings.HasPrefix(id, "xdg-desktop-portal")
}
