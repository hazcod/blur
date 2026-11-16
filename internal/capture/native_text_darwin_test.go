//go:build darwin

package capture

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/ebitengine/purego"
	"github.com/hazcod/blur/internal/platform"
	"github.com/hazcod/blur/internal/platform/fake"
)

type alternateTextFixture struct {
	text, mode     string
	visible        *nativeTextRange
	count          int64
	parameterReads int
	onParameter    func()
	onVisible      func()
}

func TestNativeUnsupportedRangeDoesNotDiscardReadableSibling(t *testing.T) {
	n, _ := alternateNativeText(t, &alternateTextFixture{text: "unavailable range", mode: "range", count: 17})
	n.api.parameterizedValue = func(uintptr, uintptr, uintptr, *uintptr) int32 { return -25213 }
	p := Pipeline{
		Source: &fake.CaptureSource{Report: available(), ProbeResult: platform.CaptureProbe{TargetID: "t"}},
		Limits: limits(),
		Read: func(context.Context, platform.CaptureProbe, Limits) (rawFrame, error) {
			return rawFrame{roots: []*rawNode{
				{boundedValue: true, declaredBytes: -1, value: n.value},
				{boundedValue: true, declaredBytes: -1, value: func(context.Context, int) (string, error) { return "Mira owns rollback.", nil }},
			}}, nil
		},
	}
	got, err := p.Capture(context.Background())
	if err != nil || got.Text() != "Mira owns rollback." {
		t.Fatalf("unsupported optional range discarded sibling: text=%q err=%v", got.Text(), err)
	}
}

func alternateNativeText(t *testing.T, f *alternateTextFixture) (*nativeAXNode, *readOnlyAXFixture) {
	t.Helper()
	meta := &readOnlyAXFixture{role: "AXTextArea", text: f.text}
	n := newReadOnlyAXFixture(t, meta)
	base := nativeAX.api
	copyValue := n.api.copyValue
	var attributedCreate func(uintptr, uintptr, uintptr) uintptr
	var numberCreate func(uintptr, int32, *int64) uintptr
	purego.RegisterLibFunc(&attributedCreate, base.core, "CFAttributedStringCreate")
	purego.RegisterLibFunc(&numberCreate, base.core, "CFNumberCreate")
	n.api.copyValue = func(element, attribute uintptr, out *uintptr) int32 {
		name, _, err := copyCFString(base, attribute, 64)
		if err != nil {
			t.Fatal(err)
		}
		switch name {
		case "AXValue":
			if f.mode == "range" || f.mode == "attributed range" {
				return axAttributeUnsupported
			}
			status := copyValue(element, attribute, out)
			if status == axSuccess && f.mode == "attributed" {
				value := *out
				*out = attributedCreate(0, value, 0)
				base.release(value)
			}
			return status
		case "AXVisibleCharacterRange":
			if f.visible == nil {
				return axAttributeUnsupported
			}
			*out = base.valueCreate(4, f.visible)
			if f.onVisible != nil {
				f.onVisible()
			}
			return axSuccess
		case "AXNumberOfCharacters":
			*out = numberCreate(0, 4, &f.count)
			return axSuccess
		default:
			return copyValue(element, attribute, out)
		}
	}
	n.api.parameterizedValue = func(element, attribute, parameter uintptr, out *uintptr) int32 {
		name, _, err := copyCFString(base, attribute, 64)
		var r nativeTextRange
		if err != nil || element != 42 || name != "AXStringForRange" || base.valueGetRange(parameter, 4, &r) == 0 {
			t.Fatalf("invalid range request: %s %v", name, err)
		}
		if r.Location < 0 || r.Length < 0 || r.Length > 257 {
			t.Fatalf("unbounded native range: %+v", r)
		}
		units := utf16.Encode([]rune(f.text))
		if r.Location > int64(len(units)) || r.Length > int64(len(units))-r.Location {
			t.Fatalf("out-of-source range %+v", r)
		}
		text := string(utf16.Decode(units[r.Location : r.Location+r.Length]))
		bytes, ptr := nativeCString(text)
		*out = base.stringNew(0, ptr, cfUTF8)
		runtime.KeepAlive(bytes)
		if f.mode == "attributed range" {
			value := *out
			*out = attributedCreate(0, value, 0)
			base.release(value)
		}
		f.parameterReads++
		if f.onParameter != nil {
			f.onParameter()
		}
		return axSuccess
	}
	if sensitive, err := n.sensitive(context.Background(), "AXTextArea"); err != nil || sensitive {
		t.Fatalf("ordinary content excluded: %t %v", sensitive, err)
	}
	return n, meta
}

func TestNativeTextReadsAttributedValuesAndRangeOnlyContent(t *testing.T) {
	for _, mode := range []string{"attributed", "range", "attributed range"} {
		t.Run(mode, func(t *testing.T) {
			f := &alternateTextFixture{text: "Visible release notes: owner Mira.", mode: mode, count: 34}
			n, _ := alternateNativeText(t, f)
			got, err := n.value(context.Background(), 256)
			if err != nil || got != "Visible release notes: owner Mira." {
				t.Fatalf("native text lost: %q %v", got, err)
			}
		})
	}
}

func TestNativeTextKeepsVisibleTailInsteadOfOldScrollbackPrefix(t *testing.T) {
	for _, mode := range []string{"plain", "range"} {
		t.Run(mode, func(t *testing.T) {
			f := &alternateTextFixture{text: strings.Repeat("old log line\n", 1000) + "Mira owns rollback.", mode: mode, visible: &nativeTextRange{Location: 13000, Length: 19}, count: 13019}
			n, _ := alternateNativeText(t, f)
			got, err := n.value(context.Background(), 256)
			if !strings.Contains(got, "Mira owns rollback.") || !errors.Is(err, errAXValueTruncated) {
				t.Fatalf("visible current content lost or complete coverage claimed: %q %v", got, err)
			}
		})
	}
}

func TestNativeRangeReadRejectsSecureTransitionBeforeReturningText(t *testing.T) {
	f := &alternateTextFixture{text: "private edited content", mode: "range", count: 22}
	n, meta := alternateNativeText(t, f)
	f.onParameter = func() { meta.subrole = "AXSecureTextArea" }
	text, err := n.value(context.Background(), 256)
	if err == nil || text != "" || f.parameterReads != 1 {
		t.Fatalf("changed secure field escaped: %q reads=%d err=%v", text, f.parameterReads, err)
	}
}

func TestNativeRangeReadRejectsNegativeOrOverflowingVisibleRanges(t *testing.T) {
	for _, r := range []nativeTextRange{{Location: -1, Length: 2}, {Location: 0, Length: -1}, {Location: 1<<63 - 1, Length: 2}, {Location: 8, Length: 20}} {
		f := &alternateTextFixture{text: "visible text", mode: "range", visible: &r, count: 12}
		n, _ := alternateNativeText(t, f)
		text, err := n.value(context.Background(), 256)
		if err == nil || text != "" || f.parameterReads != 0 {
			t.Fatalf("invalid range reached native text: %+v %q calls=%d err=%v", r, text, f.parameterReads, err)
		}
	}
}

func TestNativeTextRejectsSecureTransitionAfterVisibleMetadata(t *testing.T) {
	f := &alternateTextFixture{text: strings.Repeat("old log line\n", 1000) + "Mira owns rollback.", mode: "plain", visible: &nativeTextRange{Location: 13000, Length: 19}, count: 13019}
	n, meta := alternateNativeText(t, f)
	f.onVisible = func() { meta.subrole = "AXSecureTextArea" }
	text, err := n.value(context.Background(), 256)
	if err == nil || text != "" {
		t.Fatalf("visible metadata changed to secure content: %q %v", text, err)
	}
}
