package linux

import (
	"bytes"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
)

func TestFocusEventUsesOnlyValidatedBusIdentityAndPath(t *testing.T) {
	signal := &dbus.Signal{
		Sender: ":1.42",
		Path:   dbus.ObjectPath("/org/a11y/atspi/accessible/42"),
		Name:   "org.a11y.atspi.Event.Focus.Focus",
		Body:   []any{"sensitive message body", int32(0), int32(0), dbus.MakeVariant("must remain ignored")},
	}
	ref, ok := focusRefFromSignal(signal)
	if !ok || ref.busName != ":1.42" || ref.path != signal.Path {
		t.Fatalf("focus reference = %#v, %t", ref, ok)
	}
	for _, invalid := range []*dbus.Signal{
		{Sender: "org.a11y.atspi.Registry", Path: signal.Path, Name: signal.Name},
		{Sender: ":1.42", Path: dbus.ObjectPath("/"), Name: signal.Name},
		{Sender: ":1.42", Path: signal.Path, Name: "org.a11y.atspi.Event.Object.TextChanged"},
		{Sender: ":1.bad!", Path: signal.Path, Name: signal.Name},
	} {
		if ref, ok := focusRefFromSignal(invalid); ok {
			t.Fatalf("accepted invalid event as %#v", ref)
		}
	}
}

func TestFocusTrackerIssuesOpaqueChangingLeases(t *testing.T) {
	tracker := newFocusTracker(bytes.NewReader([]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32}))
	firstRef := atspiObjectRef{busName: ":1.42", path: dbus.ObjectPath("/org/a11y/atspi/accessible/42")}
	firstEvent, err := tracker.update(firstRef, firstRef, "text-editor")
	if err != nil {
		t.Fatal(err)
	}
	if firstEvent.Kind != "focus" || firstEvent.TargetID == firstRef.busName || firstEvent.TargetID == string(firstRef.path) {
		t.Fatalf("focus event exposed its D-Bus identity: %#v", firstEvent)
	}
	if _, ok := tracker.target(firstEvent.TargetID); !ok {
		t.Fatal("current focus lease did not resolve")
	}
	secondRef := atspiObjectRef{busName: ":1.43", path: dbus.ObjectPath("/org/a11y/atspi/accessible/43")}
	secondEvent, err := tracker.update(secondRef, secondRef, "chat")
	if err != nil {
		t.Fatal(err)
	}
	if secondEvent.TargetID == firstEvent.TargetID {
		t.Fatal("focus change reused the previous lease")
	}
	if _, ok := tracker.target(firstEvent.TargetID); ok {
		t.Fatal("stale focus lease remained valid")
	}
	if _, ok := tracker.target(secondEvent.TargetID); !ok {
		t.Fatal("latest focus lease did not resolve")
	}
	if _, ok := tracker.target(""); ok {
		t.Fatal("invalid lease unexpectedly resolved")
	}
}

func TestFocusTrackerInvalidatesOldLeaseWhenNonceGenerationFails(t *testing.T) {
	tracker := newFocusTracker(bytes.NewReader(make([]byte, 16)))
	ref := atspiObjectRef{busName: ":1.42", path: dbus.ObjectPath("/org/a11y/atspi/accessible/42")}
	issued, err := tracker.update(ref, ref, "editor")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tracker.update(ref, ref, "editor"); err == nil {
		t.Fatal("focus change without a fresh nonce was accepted")
	}
	if _, ok := tracker.target(issued.TargetID); ok {
		t.Fatal("failed focus update left a stale lease valid")
	}
}

func TestATSPIDBusAddressAcceptsOnlyBoundedLocalUnixAddresses(t *testing.T) {
	for _, tc := range []struct {
		address string
		want    bool
	}{{"unix:path=/run/user/1000/at-spi/bus_0,guid=0123456789abcdef0123456789abcdef", true}, {"unix:abstract=at-spi-bus", true}, {"tcp:host=127.0.0.1,port=1234", false}, {"unix:path=/tmp/bus;tcp:host=127.0.0.1,port=1234", false}, {"", false}, {"unix:path=", false}} {
		if got := validATSPIDBusAddress(tc.address); got != tc.want {
			t.Errorf("validATSPIDBusAddress(%q) = %t, want %t", tc.address, got, tc.want)
		}
	}
	if validATSPIDBusAddress("unix:path=" + strings.Repeat("x", 4096)) {
		t.Fatal("oversized D-Bus address accepted")
	}
}

func TestATSPISubscriptionSpecsExcludeRawPropertyPayloads(t *testing.T) {
	want := []string{"focus:", "object:text-changed", "object:children-changed", "window:activate"}
	got := atspiSubscriptions()
	if len(got) != len(want) {
		t.Fatalf("subscriptions = %v, want %v", got, want)
	}
	for i := range want {
		if got[i].eventType != want[i] {
			t.Errorf("subscription %d = %q, want %q", i, got[i].eventType, want[i])
		}
		if got[i].eventType == "object:property-change:accessible-value" {
			t.Fatal("raw property values must not be subscribed to")
		}
	}
}

func TestEventSignalHandlerReleasesUninterpretedPayload(t *testing.T) {
	signal := &dbus.Signal{
		Sender: ":1.42", Path: dbus.ObjectPath("/org/a11y/atspi/accessible/42"),
		Name: "org.a11y.atspi.Event.Object.PropertyChange",
		Body: []any{"private value", int32(1)},
	}
	discardATSPISignalBody(signal)
	if signal.Body != nil {
		t.Fatal("event body was retained after filtering")
	}
}

func TestATSPIRefreshSignalClassifierUsesHeadersOnly(t *testing.T) {
	valid := &dbus.Signal{
		Sender: ":1.42", Path: dbus.ObjectPath("/org/a11y/atspi/accessible/42"),
		Name: "org.a11y.atspi.Event.Object.TextChanged", Body: []any{"private message text"},
	}
	if !atspiRefreshSignal(valid) {
		t.Fatal("valid text-change event was ignored")
	}
	if !atspiRefreshSignal(&dbus.Signal{Sender: ":1.42", Path: valid.Path, Name: "org.a11y.atspi.Event.Window.Activate"}) {
		t.Fatal("valid active-window event was ignored")
	}
	for _, invalid := range []*dbus.Signal{
		{Sender: ":1.42", Path: valid.Path, Name: "org.a11y.atspi.Event.Object.PropertyChange", Body: valid.Body},
		{Sender: "org.a11y.atspi.Registry", Path: valid.Path, Name: valid.Name},
		{Sender: ":1.42", Path: dbus.ObjectPath("/"), Name: valid.Name},
	} {
		if atspiRefreshSignal(invalid) {
			t.Fatal("untrusted or irrelevant event header was accepted")
		}
	}
}

func TestATSPIRoleMappingMarksPasswordAndEditableFieldsSensitive(t *testing.T) {
	for _, tc := range []struct {
		role      uint32
		want      string
		sensitive bool
	}{{11, "AXComboBox", true}, {12, "AXTextField", true}, {40, "AXSecureTextField", true}, {61, "AXTextArea", true}, {77, "AXTextField", true}, {79, "AXTextField", true}, {23, "AXWindow", false}, {27, "AXImage", false}, {29, "AXStaticText", false}, {999, "AXUnknown", false}} {
		got, sensitive := atspiRole(tc.role)
		if got != tc.want || sensitive != tc.sensitive {
			t.Errorf("atspiRole(%d) = %q, %t; want %q, %t", tc.role, got, sensitive, tc.want, tc.sensitive)
		}
	}
}

func TestOnlyBlurItselfIsAlwaysPrivate(t *testing.T) {
	if !isPrivateLinuxApp("blur") {
		t.Fatal("Blur itself must remain excluded")
	}
	for _, appID := range []string{"google-chrome", "chromium", "firefox", "brave", "microsoft-edge", "org.gnome.TextEditor"} {
		if isPrivateLinuxApp(appID) {
			t.Errorf("%q must use app-specific browser window policy, not unconditional exclusion", appID)
		}
	}
}
