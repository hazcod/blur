//go:build darwin

package capture

import (
	"context"
	"errors"
	"runtime"
	"testing"

	nativeffi "github.com/goexlib/cgo"
	"github.com/hazcod/blur/internal/platform"
	"github.com/hazcod/blur/internal/platform/fake"
)

// Only the cross-process AX boundary is replaced. CoreFoundation ownership,
// native role/sensitivity checks, tree traversal, policy and sanitization are real.
type readOnlyAXFixture struct {
	role, subrole, text string
	settable            uint8
	editable            *bool
	legacyEditable      *bool
	subroleReads        int
	valueReads          int
	childReads          int
	onValue             func()
}

func newReadOnlyAXFixture(t *testing.T, f *readOnlyAXFixture) *nativeAXNode {
	t.Helper()
	nativeAX.once.Do(loadNativeAXAPI)
	if nativeAX.err != nil {
		t.Fatal(nativeAX.err)
	}
	base := nativeAX.api
	falseValue, err := nativeffi.GetExternVariant[uintptr](base.core, "kCFBooleanFalse")
	if err != nil || falseValue == nil || *falseValue == 0 {
		t.Fatal("native false boolean unavailable", err)
	}
	api := base
	api.copyNames = func(uintptr, *uintptr) int32 { return axAttributeUnsupported }
	api.setTimeout = func(uintptr, float32) int32 { return axSuccess }
	api.copyValue = func(element, attribute uintptr, out *uintptr) int32 {
		if element != 42 {
			t.Fatalf("unexpected native element %d", element)
		}
		name, _, err := copyCFString(base, attribute, 64)
		if err != nil {
			t.Fatal(err)
		}
		var text string
		switch name {
		case "AXRole":
			text = f.role
		case "AXSubrole":
			f.subroleReads++
			text = f.subrole
		case "AXEditable", "AXIsEditable":
			editable := f.editable
			if name == "AXIsEditable" {
				editable = f.legacyEditable
			}
			if editable == nil {
				return axAttributeUnsupported
			}
			boolean := *falseValue
			if *editable {
				boolean = base.trueValue
			}
			*out = base.retain(boolean)
			return axSuccess
		case "AXValue":
			f.valueReads++
			text = f.text
			if f.onValue != nil {
				f.onValue()
			}
		default:
			t.Fatalf("unexpected attribute read %q", name)
		}
		bytes, ptr := nativeCString(text)
		*out = base.stringNew(0, ptr, cfUTF8)
		runtime.KeepAlive(bytes)
		return axSuccess
	}
	api.valueCount = func(uintptr, uintptr, *int64) int32 {
		f.childReads++
		return axAttributeUnsupported
	}
	api.release = func(value uintptr) {
		if value != 42 {
			base.release(value)
		}
	}
	return &nativeAXNode{api: api, element: 42}
}

func TestNativeSensitivitySkipsSubroleReadsForDisplayAndContainerRoles(t *testing.T) {
	for _, role := range []string{"AXStaticText", "AXButton", "AXScrollArea"} {
		t.Run(role, func(t *testing.T) {
			f := &readOnlyAXFixture{role: role}
			node := newReadOnlyAXFixture(t, f)
			sensitive, err := node.sensitive(context.Background(), role)
			if err != nil || sensitive || f.subroleReads != 0 || node.guardSensitive.Load() {
				t.Fatalf("safe role performed secure-subrole work: sensitive=%t subrole_reads=%d guard=%t err=%v", sensitive, f.subroleReads, node.guardSensitive.Load(), err)
			}
		})
	}
}

func TestNativeSensitivityRechecksEditableTextSubroles(t *testing.T) {
	f := &readOnlyAXFixture{role: "AXTextArea"}
	node := newReadOnlyAXFixture(t, f)
	sensitive, err := node.sensitive(context.Background(), f.role)
	if err != nil || sensitive || f.subroleReads != 1 || !node.guardSensitive.Load() {
		t.Fatalf("editable text did not retain secure-subrole protection: sensitive=%t subrole_reads=%d guard=%t err=%v", sensitive, f.subroleReads, node.guardSensitive.Load(), err)
	}
}

func TestNativeReadOnlyTextAreaCaptureDoesNotRequireAnAppProfile(t *testing.T) {
	for _, app := range []string{"com.apple.terminal", "com.googlecode.iterm2", "org.unknown.reader"} {
		t.Run(app, func(t *testing.T) {
			f := &readOnlyAXFixture{role: "AXTextArea", text: "Build complete. Rollback owner is Mira."}
			p := Pipeline{Source: &fake.CaptureSource{Report: available(), ProbeResult: platform.CaptureProbe{TargetID: "fixture", AppID: app}}, Limits: limits(),
				Read: func(ctx context.Context, _ platform.CaptureProbe, l Limits) (rawFrame, error) {
					return collectAXFrame(ctx, newReadOnlyAXFixture(t, f), l)
				},
			}
			got, err := p.Capture(context.Background())
			if err != nil || got.Text() != "Build complete. Rollback owner is Mira." || got.AppID() != app || f.valueReads != 1 {
				t.Fatalf("read-only content skipped: text=%q reads=%d err=%v", got.Text(), f.valueReads, err)
			}
		})
	}
}

func TestNativeCaptureReadsEditableVisibleContentWithoutAnAppAllowlist(t *testing.T) {
	yes := true
	for _, role := range []string{"AXTextArea", "AXTextField", "AXSearchField", "AXComboBox"} {
		t.Run(role, func(t *testing.T) {
			f := &readOnlyAXFixture{role: role, text: "Editing rollout notes. Rollback owner is Mira.", settable: 1, editable: &yes}
			p := Pipeline{Source: &fake.CaptureSource{Report: available(), ProbeResult: platform.CaptureProbe{TargetID: "fixture", AppID: "org.unknown.editor"}}, Limits: limits(),
				Read: func(ctx context.Context, _ platform.CaptureProbe, l Limits) (rawFrame, error) {
					return collectAXFrame(ctx, newReadOnlyAXFixture(t, f), l)
				},
			}
			got, err := p.Capture(context.Background())
			if err != nil || got.Text() != "Editing rollout notes. Rollback owner is Mira." || f.valueReads != 1 {
				t.Fatalf("visible editor content skipped: text=%q reads=%d err=%v", got.Text(), f.valueReads, err)
			}
		})
	}
}

func TestNativeTextCapturePrunesSecureFieldsBeforeTextAndChildren(t *testing.T) {
	for _, tc := range []struct {
		name string
		f    readOnlyAXFixture
	}{
		{"secure role", readOnlyAXFixture{role: "AXSecureTextField"}},
		{"secure area", readOnlyAXFixture{role: "AXSecureTextArea"}},
		{"password role", readOnlyAXFixture{role: "AXPasswordField"}},
		{"secure subrole", readOnlyAXFixture{role: "AXTextArea", subrole: "AXSecureTextField"}},
		{"secure area subrole", readOnlyAXFixture{role: "AXTextArea", subrole: "AXSecureTextArea"}},
		{"password subrole", readOnlyAXFixture{role: "AXTextField", subrole: "AXPasswordField"}},
		{"unprefixed password subrole", readOnlyAXFixture{role: "AXTextArea", subrole: "PasswordField"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.f
			f.text = "unsent private input"
			frame, err := collectAXFrame(context.Background(), newReadOnlyAXFixture(t, &f), limits())
			if err != nil {
				t.Fatal(err)
			}
			defer frame.cleanup()
			if f.valueReads != 0 || f.childReads != 0 || !frame.roots[0].sensitive || frame.roots[0].value != nil {
				t.Fatalf("input was not pruned before text/children: reads=%d children=%d", f.valueReads, f.childReads)
			}
		})
	}
}

func TestNativeTextRechecksSensitivityBeforeDeferredRead(t *testing.T) {
	f := &readOnlyAXFixture{role: "AXTextArea", text: "now an unsent draft"}
	frame, err := collectAXFrame(context.Background(), newReadOnlyAXFixture(t, f), limits())
	if err != nil {
		t.Fatal(err)
	}
	defer frame.cleanup()
	read := frame.roots[0].value
	if read == nil {
		t.Fatal("read-only area was skipped")
	}
	f.subrole = "AXSecureTextField"
	text, err := read(context.Background(), 256)
	if err == nil || text != "" || f.valueReads != 0 {
		t.Fatalf("changed input was read: %q reads=%d err=%v", text, f.valueReads, err)
	}
}

func TestNativeTextRejectsSensitivityChangeDuringRead(t *testing.T) {
	f := &readOnlyAXFixture{role: "AXTextArea", text: "changed while AX was answering"}
	frame, err := collectAXFrame(context.Background(), newReadOnlyAXFixture(t, f), limits())
	if err != nil {
		t.Fatal(err)
	}
	defer frame.cleanup()
	read := frame.roots[0].value
	if read == nil {
		t.Fatal("read-only area was skipped")
	}
	f.onValue = func() { f.subrole = "AXSecureTextArea" }
	text, err := read(context.Background(), 256)
	if err == nil || text != "" {
		t.Fatalf("changed content escaped: %q err=%v", text, err)
	}
}

func TestNativeReadOnlyTextAreaHonorsCancellationWithoutReading(t *testing.T) {
	f := &readOnlyAXFixture{role: "AXTextArea"}
	node := newReadOnlyAXFixture(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := node.sensitive(ctx, "AXTextArea")
	if !errors.Is(err, context.Canceled) || f.valueReads != 0 {
		t.Fatalf("cancelled metadata was accepted: reads=%d err=%v", f.valueReads, err)
	}
}
