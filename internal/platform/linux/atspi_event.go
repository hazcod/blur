package linux

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/godbus/dbus/v5"
	"github.com/hazcod/blur/internal/appidentity"
	"github.com/hazcod/blur/internal/browserprivacy"
	"github.com/hazcod/blur/internal/platform"
)

const atspiFocusSignal = "org.a11y.atspi.Event.Focus.Focus"
const a11yAccessible = "org.a11y.atspi.Accessible"

type atspiSubscription struct {
	eventType string
	iface     string
	member    string
}

func atspiSubscriptions() []atspiSubscription {
	return []atspiSubscription{
		{eventType: "focus:", iface: "org.a11y.atspi.Event.Focus", member: "Focus"},
		{eventType: "object:text-changed", iface: "org.a11y.atspi.Event.Object", member: "TextChanged"},
		{eventType: "object:children-changed", iface: "org.a11y.atspi.Event.Object", member: "ChildrenChanged"},
		{eventType: "window:activate", iface: "org.a11y.atspi.Event.Window", member: "Activate"},
	}
}

// atspiObjectRef stays inside the adapter. It is derived only from D-Bus
// headers; event bodies can contain user text and must never become identity.
type atspiObjectRef struct {
	busName string
	path    dbus.ObjectPath
}

type focusTarget struct {
	focused  atspiObjectRef
	window   atspiObjectRef
	appID    string
	token    string
	private  bool
	elevated bool
}

type focusTracker struct {
	mu     sync.RWMutex
	random io.Reader
	active focusTarget
}

func newFocusTracker(random io.Reader) *focusTracker {
	if random == nil {
		random = rand.Reader
	}
	return &focusTracker{random: random}
}

func (t *focusTracker) update(focused, window atspiObjectRef, appID string) (platform.CaptureEvent, error) {
	return t.updateWithStatus(focused, window, appID, false, false)
}

func (t *focusTracker) updateWithStatus(focused, window atspiObjectRef, appID string, private, elevated bool) (platform.CaptureEvent, error) {
	appID = strings.ToLower(strings.TrimSpace(appID))
	if t == nil || !validATSPISender(focused.busName) || !validObjectPath(focused.path) ||
		!validATSPISender(window.busName) || !validObjectPath(window.path) || !validLinuxAppID(appID) {
		return platform.CaptureEvent{}, platform.ErrInvalidLease
	}
	var nonce [16]byte
	if _, err := io.ReadFull(t.random, nonce[:]); err != nil {
		t.mu.Lock()
		t.active = focusTarget{}
		t.mu.Unlock()
		return platform.CaptureEvent{}, platform.ErrInvalidLease
	}
	token := "atspi:" + hex.EncodeToString(nonce[:])
	clear(nonce[:])
	t.mu.Lock()
	t.active = focusTarget{focused: focused, window: window, appID: appID, token: token, private: private, elevated: elevated}
	t.mu.Unlock()
	return platform.CaptureEvent{Kind: "focus", TargetID: token}, nil
}

func (t *focusTracker) current() (focusTarget, bool) {
	if t == nil {
		return focusTarget{}, false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.active, t.active.token != ""
}

func (t *focusTracker) clear() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.active = focusTarget{}
	t.mu.Unlock()
}

func (t *focusTracker) target(token string) (focusTarget, bool) {
	if t == nil || token == "" {
		return focusTarget{}, false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.active.token != token {
		return focusTarget{}, false
	}
	return t.active, true
}

func validLinuxAppID(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z') && !(char >= '0' && char <= '9') && char != '.' && char != '_' && char != '-' {
			return false
		}
	}
	return true
}

func isPrivateLinuxApp(appID string) bool {
	return appID == "blur"
}

func isPrivateLinuxBrowserWindow(appID, title string, titleErr error) bool {
	if !appidentity.IsBrowserApp(appID) {
		return false
	}
	if titleErr != nil || len(title) > 512 || !utf8.ValidString(title) {
		return true
	}
	return browserprivacy.IsPrivateWindowTitle(title)
}

// IsPrivateBrowserWindow reads only the focused window's bounded accessible
// name and returns a boolean; title text never escapes the platform adapter.
// Unknown, missing, malformed, or private-marked browser titles fail closed.
func IsPrivateBrowserWindow(ctx context.Context, conn *dbus.Conn, bus string, path dbus.ObjectPath, appID string) bool {
	if !appidentity.IsBrowserApp(appID) {
		return false
	}
	if ctx == nil || ctx.Err() != nil || conn == nil || !validATSPISender(bus) || !validObjectPath(path) {
		return true
	}
	var title string
	err := conn.Object(bus, path).CallWithContext(ctx, a11yAccessible+".GetName", 0).Store(&title)
	return isPrivateLinuxBrowserWindow(appID, title, err)
}

func validATSPIDBusAddress(address string) bool {
	if len(address) == 0 || len(address) > 4096 || !strings.HasPrefix(address, "unix:") || strings.Contains(address, ";") {
		return false
	}
	options := strings.Split(address[len("unix:"):], ",")
	if len(options) == 0 || len(options) > 4 {
		return false
	}
	seen := make(map[string]struct{}, len(options))
	for i, option := range options {
		key, value, ok := strings.Cut(option, "=")
		if !ok || value == "" {
			return false
		}
		if _, duplicate := seen[key]; duplicate {
			return false
		}
		seen[key] = struct{}{}
		switch key {
		case "path":
			if i != 0 || !strings.HasPrefix(value, "/") {
				return false
			}
		case "abstract":
			if i != 0 {
				return false
			}
		case "guid":
			if i == 0 || len(value) != 32 {
				return false
			}
			for _, c := range value {
				if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') && !(c >= 'A' && c <= 'F') {
					return false
				}
			}
		default:
			return false
		}
	}
	_, path := seen["path"]
	_, abstract := seen["abstract"]
	return path != abstract
}

func focusRefFromSignal(signal *dbus.Signal) (atspiObjectRef, bool) {
	if signal == nil || signal.Name != atspiFocusSignal || !validATSPISender(signal.Sender) || !validObjectPath(signal.Path) {
		return atspiObjectRef{}, false
	}
	return atspiObjectRef{busName: signal.Sender, path: signal.Path}, true
}

func discardATSPISignalBody(signal *dbus.Signal) {
	if signal != nil {
		signal.Body = nil
	}
}

func atspiRefreshSignal(signal *dbus.Signal) bool {
	if signal == nil || !validATSPISender(signal.Sender) || !validObjectPath(signal.Path) {
		return false
	}
	switch signal.Name {
	case "org.a11y.atspi.Event.Object.TextChanged", "org.a11y.atspi.Event.Object.ChildrenChanged", "org.a11y.atspi.Event.Window.Activate":
		return true
	default:
		return false
	}
}

func validATSPISender(name string) bool {
	if len(name) < 5 || len(name) > 255 || name[0] != ':' {
		return false
	}
	parts := strings.Split(name[1:], ".")
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for i := 0; i < len(part); i++ {
			c := part[i]
			if !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') && !(c >= '0' && c <= '9') && c != '_' && c != '-' {
				return false
			}
		}
	}
	return true
}

// ValidAccessibleObject validates the opaque (bus name, object path) pair
// returned by AT-SPI. Null/root references are never suitable tree nodes.
func ValidAccessibleObject(busName string, path dbus.ObjectPath) bool {
	return validATSPISender(busName) && validObjectPath(path) && path != "/org/a11y/atspi/null"
}

// RoleForCapture maps the standard AT-SPI role ID into Blur's normalized AX
// vocabulary and identifies roles whose values must not be requested.
func RoleForCapture(role uint32) (string, bool) { return atspiRole(role) }

// atspiRole normalizes a small set of stable AT-SPI role IDs to the shared AX
// vocabulary. Unknown roles remain non-sensitive and are retained as AXUnknown.
func atspiRole(role uint32) (name string, sensitive bool) {
	switch role {
	case 11:
		return "AXComboBox", true
	case 12, 77:
		// Date editors and toolbar edit bars accept user-entered text just like
		// form entries; prune them before requesting values or descendants.
		return "AXTextField", true
	case 7:
		return "AXCheckBox", false
	case 16, 23, 69:
		return "AXWindow", false
	case 25:
		return "AXWebArea", false
	case 27:
		return "AXImage", false
	case 29:
		return "AXStaticText", false
	case 31:
		return "AXList", false
	case 37:
		return "AXTab", false
	case 38:
		return "AXTabGroup", false
	case 39:
		return "AXGroup", false
	case 40:
		return "AXSecureTextField", true
	case 43:
		return "AXButton", false
	case 44:
		return "AXRadioButton", false
	case 48:
		return "AXScrollBar", false
	case 50:
		return "AXSeparator", false
	case 51:
		return "AXSlider", false
	case 54:
		return "AXStatusBar", false
	case 61:
		return "AXTextArea", true
	case 63:
		return "AXToolbar", false
	case 65:
		return "AXTree", false
	case 73:
		return "AXParagraph", false
	case 75:
		return "AXApplication", false
	case 79:
		return "AXTextField", true
	case 87:
		return "AXGroup", false
	case 88:
		return "AXLink", false
	case 95:
		return "AXWebArea", false
	default:
		return "AXUnknown", false
	}
}
