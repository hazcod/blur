//go:build linux

package capture

import (
	"context"
	"errors"
	"sync/atomic"
	"unicode/utf8"

	"github.com/godbus/dbus/v5"
	"github.com/hazcod/blur/internal/appidentity"
	"github.com/hazcod/blur/internal/platform"
	platformlinux "github.com/hazcod/blur/internal/platform/linux"
)

const (
	atspiAccessibleIface = "org.a11y.atspi.Accessible"
	atspiTextIface       = "org.a11y.atspi.Text"
	atspiStateFocused    = 12
	atspiMaxInterfaces   = 64
)

var nextATSPINodeID atomic.Uint64

type atspiAXNode struct {
	conn *dbus.Conn
	bus  string
	path dbus.ObjectPath
	id   uintptr
}

func newATSPIAXNode(conn *dbus.Conn, bus string, path dbus.ObjectPath) (*atspiAXNode, error) {
	if conn == nil || !platformlinux.ValidAccessibleObject(bus, path) {
		return nil, errors.New("capture: invalid AT-SPI object")
	}
	id := nextATSPINodeID.Add(1)
	if id == 0 {
		return nil, errors.New("capture: AT-SPI identity exhausted")
	}
	return &atspiAXNode{conn: conn, bus: bus, path: path, id: uintptr(id)}, nil
}

func (n *atspiAXNode) identity() uintptr { return n.id }
func (*atspiAXNode) release()            {}

func (n *atspiAXNode) role(ctx context.Context) (string, error) {
	var roleID uint32
	if err := n.object().CallWithContext(ctx, atspiAccessibleIface+".GetRole", 0).Store(&roleID); err != nil {
		return "", errors.New("capture: AT-SPI role unavailable")
	}
	role, _ := platformlinux.RoleForCapture(roleID)
	return role, nil
}

func (*atspiAXNode) sensitive(_ context.Context, role string) (bool, error) {
	return isEditableTextRole(role), nil
}

func (n *atspiAXNode) children(ctx context.Context, max int) ([]axTreeNode, bool, error) {
	if max <= 0 {
		return nil, false, nil
	}
	var count int32
	if err := n.object().CallWithContext(ctx, atspiAccessibleIface+".GetChildCount", 0).Store(&count); err != nil {
		return nil, false, errors.New("capture: AT-SPI child count unavailable")
	}
	limited, more, err := boundedATSPIChildCount(count, max)
	if err != nil {
		return nil, false, err
	}
	children := make([]axTreeNode, 0, limited)
	for i := 0; i < limited; i++ {
		if err := ctx.Err(); err != nil {
			return children, more, err
		}
		var bus string
		var path dbus.ObjectPath
		if err := n.object().CallWithContext(ctx, atspiAccessibleIface+".GetChildAtIndex", 0, int32(i)).Store(&bus, &path); err != nil {
			return children, more, errors.New("capture: AT-SPI child unavailable")
		}
		child, err := newATSPIAXNode(n.conn, bus, path)
		if err != nil {
			return children, more, err
		}
		children = append(children, child)
	}
	return children, more, nil
}

func (n *atspiAXNode) value(ctx context.Context, max int) (string, error) {
	if max <= 0 {
		return "", errAXValueTruncated
	}
	var interfaces []string
	if err := n.object().CallWithContext(ctx, atspiAccessibleIface+".GetInterfaces", 0).Store(&interfaces); err != nil || len(interfaces) > atspiMaxInterfaces {
		return "", errors.New("capture: AT-SPI interfaces unavailable")
	}
	hasText := false
	for _, name := range interfaces {
		if name == atspiTextIface {
			hasText = true
			break
		}
	}
	if !hasText {
		return "", nil
	}
	textObject := n.conn.Object(n.bus, n.path)
	var characterCount int32
	if err := textObject.CallWithContext(ctx, atspiTextIface+".GetCharacterCount", 0).Store(&characterCount); err != nil || characterCount < 0 {
		return "", errors.New("capture: AT-SPI text length unavailable")
	}
	if characterCount == 0 {
		return "", nil
	}
	limit := int32(max + 1)
	end, more, err := boundedATSPIChildCount(characterCount, int(limit))
	if err != nil {
		return "", err
	}
	var text string
	if err := textObject.CallWithContext(ctx, atspiTextIface+".GetText", 0, int32(0), int32(end)).Store(&text); err != nil {
		return "", errors.New("capture: AT-SPI text unavailable")
	}
	if !utf8.ValidString(text) {
		return "", errors.New("capture: invalid AT-SPI text")
	}
	bounded, truncated, err := boundedATSPIText(text, max, more)
	if err != nil {
		return "", err
	}
	if truncated {
		return bounded, errAXValueTruncated
	}
	return bounded, nil
}

func (n *atspiAXNode) object() dbus.BusObject { return n.conn.Object(n.bus, n.path) }

func NewLinuxPipeline(source *platformlinux.Adapter, limits Limits, exclusions Exclusions, profiles []AppProfile) Pipeline {
	profileCopy := make([]AppProfile, len(profiles))
	for i, profile := range profiles {
		profileCopy[i] = AppProfile{AppID: profile.AppID, IgnoreRoles: append([]string(nil), profile.IgnoreRoles...)}
	}
	exclusions.Apps = append([]string(nil), exclusions.Apps...)
	exclusions.Domains = append([]string(nil), exclusions.Domains...)
	return Pipeline{
		Source: source, Limits: limits, Exclusions: exclusions, Profiles: profileCopy,
		Read: func(ctx context.Context, probe platform.CaptureProbe, limits Limits) (rawFrame, error) {
			return readLinuxATSPI(ctx, source, probe, limits)
		},
	}
}

func readLinuxATSPI(ctx context.Context, source *platformlinux.Adapter, probe platform.CaptureProbe, limits Limits) (rawFrame, error) {
	var frame rawFrame
	if source == nil {
		return frame, errors.New("capture: Linux AT-SPI source unavailable")
	}
	var focused platformlinux.FocusedContext
	err := source.WithVerifiedTarget(ctx, probe.TargetID, func(ctx context.Context, target platformlinux.FocusedContext) error {
		if err := verifyATSPIFocus(ctx, target.Conn, target.FocusedBus, target.FocusedPath); err != nil {
			return err
		}
		root, err := newATSPIAXNode(target.Conn, target.WindowBus, target.WindowPath)
		if err != nil {
			return err
		}
		if err := verifyLinuxBrowserWindow(ctx, target, probe.AppID); err != nil {
			root.release()
			return err
		}
		frame, err = collectAXFrame(ctx, root, limits)
		if err != nil {
			return err
		}
		focused = target
		return verifyATSPIFocus(ctx, target.Conn, target.FocusedBus, target.FocusedPath)
	})
	if err != nil {
		return frame, err
	}
	wrapAXValues(frame.roots, func(ctx context.Context, max int, value func(context.Context, int) (string, error)) (string, error) {
		var text string
		readErr := source.WithVerifiedTarget(ctx, probe.TargetID, func(ctx context.Context, target platformlinux.FocusedContext) error {
			if target.Conn != focused.Conn || target.WindowBus != focused.WindowBus || target.WindowPath != focused.WindowPath {
				return platform.ErrInvalidLease
			}
			if err := verifyLinuxBrowserWindow(ctx, target, probe.AppID); err != nil {
				return err
			}
			if err := verifyATSPIFocus(ctx, target.Conn, target.FocusedBus, target.FocusedPath); err != nil {
				return err
			}
			var err error
			text, err = value(ctx, max)
			if focusErr := verifyATSPIFocus(ctx, target.Conn, target.FocusedBus, target.FocusedPath); focusErr != nil {
				return focusErr
			}
			if err := verifyLinuxBrowserWindow(ctx, target, probe.AppID); err != nil {
				return err
			}
			return err
		})
		return text, readErr
	})
	return frame, nil
}

func verifyLinuxBrowserWindow(ctx context.Context, target platformlinux.FocusedContext, appID string) error {
	if appidentity.IsBrowserApp(appID) && platformlinux.IsPrivateBrowserWindow(ctx, target.Conn, target.WindowBus, target.WindowPath, appID) {
		return errors.New("capture: browser privacy state unavailable")
	}
	return nil
}

func verifyATSPIFocus(ctx context.Context, conn *dbus.Conn, bus string, path dbus.ObjectPath) error {
	if conn == nil || !platformlinux.ValidAccessibleObject(bus, path) {
		return platform.ErrInvalidLease
	}
	var states []uint32
	if err := conn.Object(bus, path).CallWithContext(ctx, atspiAccessibleIface+".GetState", 0).Store(&states); err != nil || len(states) > 128 {
		return platform.ErrInvalidLease
	}
	for _, state := range states {
		if state == atspiStateFocused {
			return nil
		}
	}
	return platform.ErrInvalidLease
}
