//go:build darwin

package capture

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
	"unsafe"

	"github.com/ebitengine/purego"

	"github.com/hazcod/blur/internal/platform/darwin"
)

func TestFocusedWindowFallsBackToFocusedElementAncestorWhenWindowAttributeCannotComplete(t *testing.T) {
	nativeAX.once.Do(loadNativeAXAPI)
	if nativeAX.err != nil {
		t.Fatal(nativeAX.err)
	}
	base := nativeAX.api
	api := base
	api.createApp = func(pid int32) uintptr {
		if pid != 7 {
			t.Fatalf("unexpected pid: %d", pid)
		}
		return 42
	}
	api.setTimeout = func(uintptr, float32) int32 { return axSuccess }
	api.equal = func(a, b uintptr) bool { return a == b }
	api.release = func(value uintptr) {
		if value == 42 || value == 50 || value == 51 || value == 77 {
			return
		}
		base.release(value)
	}
	api.copyValue = func(element, attribute uintptr, out *uintptr) int32 {
		name, _, err := copyCFString(base, attribute, 64)
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case element == 42 && name == "AXFocusedWindow":
			return -25204
		case element == 42 && name == "AXFocusedUIElement":
			*out = 50
			return axSuccess
		case name == "AXRole":
			role := map[uintptr]string{50: "AXTextField", 51: "AXGroup", 77: "AXWindow"}[element]
			if role == "" {
				return axAttributeUnsupported
			}
			storage, ptr := nativeCString(role)
			*out = base.stringNew(0, ptr, cfUTF8)
			runtime.KeepAlive(storage)
			if *out == 0 || base.getTypeID(*out) != base.stringType() {
				t.Fatalf("invalid mocked AX role string for %q", role)
			}
			return axSuccess
		case name == "AXParent":
			parent := map[uintptr]uintptr{50: 51, 51: 77}[element]
			if parent == 0 {
				return axNoValue
			}
			*out = parent
			return axSuccess
		default:
			return axAttributeUnsupported
		}
	}
	window, err := focusedWindow(context.Background(), api, 7)
	if err != nil || window != 77 {
		t.Fatalf("focused UI element did not resolve its containing AXWindow: window=%d err=%v issue=%+v", window, err, ReadIssueForError(err))
	}
	api.release(window)
}

func TestFocusedWindowFailsClosedWhenFocusedElementIsUnavailable(t *testing.T) {
	nativeAX.once.Do(loadNativeAXAPI)
	if nativeAX.err != nil {
		t.Fatal(nativeAX.err)
	}
	base := nativeAX.api
	api := base
	api.createApp = func(int32) uintptr { return 42 }
	api.setTimeout = func(uintptr, float32) int32 { return axSuccess }
	api.release = func(value uintptr) {
		if value != 42 {
			base.release(value)
		}
	}
	fallbackAttempted := false
	api.copyValue = func(element, attribute uintptr, _ *uintptr) int32 {
		name, _, err := copyCFString(base, attribute, 64)
		if err != nil {
			t.Fatal(err)
		}
		if element != 42 {
			t.Fatalf("unexpected AX element: %d", element)
		}
		switch name {
		case "AXFocusedWindow":
			return axCannotComplete
		case "AXFocusedUIElement":
			fallbackAttempted = true
			return axAttributeUnsupported
		default:
			t.Fatalf("unexpected AX attribute: %s", name)
			return axAttributeUnsupported
		}
	}
	window, err := focusedWindow(context.Background(), api, 7)
	issue := ReadIssueForError(err)
	if window != 0 || !fallbackAttempted || issue.Stage != ReadStageWindow || issue.Status != axCannotComplete {
		t.Fatalf("unverified window escaped failed AX fallback: window=%d fallback=%t issue=%+v err=%v", window, fallbackAttempted, issue, err)
	}
}

func TestManualAccessibilityRequestUsesNativeBooleanAndIsBoundedPerPID(t *testing.T) {
	nativeAX.once.Do(loadNativeAXAPI)
	if nativeAX.err != nil {
		t.Fatal(nativeAX.err)
	}
	base := nativeAX.api
	api := base
	api.createApp = func(pid int32) uintptr {
		if pid != 7 {
			t.Fatalf("unexpected pid: %d", pid)
		}
		return 1
	}
	api.release = func(value uintptr) {
		if value != 1 {
			base.release(value)
		}
	}
	api.setTimeout = func(uintptr, float32) int32 { return axSuccess }
	calls := 0
	api.setValue = func(app, attribute, value uintptr) int32 {
		name, _, err := copyCFString(base, attribute, 64)
		if err != nil || name != "AXManualAccessibility" || app != 1 || value != base.trueValue || base.booleanValue(value) != 1 {
			t.Fatalf("invalid lazy AX request: attribute=%q err=%v", name, err)
		}
		calls++
		return axAttributeUnsupported // Optional on apps that expose AX without a request.
	}
	requestManualAccessibility(context.Background(), api, 7)
	if calls != 1 {
		t.Fatalf("requests=%d", calls)
	}
	tracker := manualAXTracker{}
	now := time.Now()
	if !tracker.admit(7, now) || tracker.admit(7, now.Add(time.Second)) || !tracker.admit(7, now.Add(time.Minute)) {
		t.Fatal("manual AX requests are not rate bounded")
	}
	for pid := int32(1); pid < 200; pid++ {
		tracker.admit(pid, now)
	}
	if len(tracker.attempts) > 128 {
		t.Fatal("manual AX PID cache exceeded bound")
	}
}

func TestNativePagePathValidationReadsOnlyOneChildAndChecksCount(t *testing.T) {
	nativeAX.once.Do(loadNativeAXAPI)
	if nativeAX.err != nil {
		t.Fatal(nativeAX.err)
	}
	base := nativeAX.api
	api := base
	api.setTimeout = func(uintptr, float32) int32 { return axSuccess }
	count := int64(4)
	api.valueCount = func(_, _ uintptr, out *int64) int32 { *out = count; return axSuccess }
	api.copyValues = func(_, _ uintptr, index, size int64, out *uintptr) int32 {
		if index != 2 || size != 1 {
			t.Fatalf("unbounded path read: index=%d size=%d", index, size)
		}
		*out = 999
		return axSuccess
	}
	api.arrayType = func() uintptr { return 8 }
	api.getTypeID = func(uintptr) uintptr { return 8 }
	api.arrayCount = func(uintptr) int64 { return 1 }
	api.arrayValue = func(uintptr, int64) uintptr { return 77 }
	api.equal = func(a, b uintptr) bool { return a == b }
	api.release = func(value uintptr) {
		if value != 999 {
			base.release(value)
		}
	}
	node := &nativeAXNode{api: api, element: 42}
	if matches, err := node.childMatches(context.Background(), 2, 4, 77); err != nil || !matches {
		t.Fatalf("valid path rejected: %v %v", matches, err)
	}
	count = 5
	if matches, err := node.childMatches(context.Background(), 2, 4, 77); err == nil || matches {
		t.Fatal("changed child count accepted")
	}
}

func TestBrowserDocumentDomainAcceptsNativeCFURLAndWindowURLFallback(t *testing.T) {
	nativeAX.once.Do(loadNativeAXAPI)
	if nativeAX.err != nil {
		t.Fatal(nativeAX.err)
	}
	a := nativeAX.api
	var createURL func(uintptr, uintptr, uintptr) uintptr
	purego.RegisterLibFunc(&createURL, a.core, "CFURLCreateWithString")
	bytes, pointer := nativeCString("https://user:password@Docs.Example.test:8443/a?token=secret")
	text := a.stringNew(0, pointer, cfUTF8)
	runtime.KeepAlive(bytes)
	defer a.release(text)
	url := createURL(0, text, 0)
	if url == 0 {
		t.Fatal("CFURLCreateWithString failed")
	}
	defer a.release(url)
	for _, attribute := range []string{"AXDocument", "AXURL"} {
		t.Run(attribute, func(t *testing.T) {
			api := a
			api.setTimeout = func(uintptr, float32) int32 { return axSuccess }
			api.copyValue = func(_ uintptr, name uintptr, out *uintptr) int32 {
				value, _, err := copyCFString(a, name, 64)
				if err != nil {
					t.Error(err)
					return axNoValue
				}
				if value != attribute {
					return axAttributeUnsupported
				}
				*out = a.retain(url)
				return axSuccess
			}
			domain, known, err := readBrowserDocumentDomain(context.Background(), api, 1)
			if err != nil || !known || domain != "docs.example.test" {
				t.Fatalf("domain=%q known=%v err=%v", domain, known, err)
			}
		})
	}
}

func TestNewDarwinPipelineWiresPassiveAXReader(t *testing.T) {
	source := darwin.New()
	profiles := []AppProfile{{AppID: "org.example.editor", IgnoreRoles: []string{"AXToolbar"}}}
	pipeline := NewDarwinPipeline(source, limits(), Exclusions{Apps: []string{"org.private.app"}}, profiles)
	if pipeline.Source != source || pipeline.Read == nil || pipeline.Limits != limits() || len(pipeline.Profiles) != 1 {
		t.Fatalf("Darwin pipeline not wired: %+v", pipeline)
	}
	_ = source.Close()
}

func TestNativeCFStringBytesABISmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("native CoreFoundation ABI smoke test")
	}
	nativeAX.once.Do(loadNativeAXAPI)
	if nativeAX.err != nil {
		t.Fatal("CoreFoundation API unavailable")
	}
	want := "ABI ✓"
	bytes, ptr := nativeCString(want)
	value := nativeAX.api.stringNew(0, ptr, cfUTF8)
	runtime.KeepAlive(bytes)
	if value == 0 {
		t.Fatal("CFStringCreateWithCString returned nil")
	}
	defer nativeAX.api.release(value)
	got, truncated, err := copyCFString(nativeAX.api, value, 32)
	if err != nil || truncated || got != want || !strings.Contains(got, "✓") {
		t.Fatalf("CFStringGetBytes result=%q truncated=%t err=%v", got, truncated, err)
	}
}

func TestNativeCFStringLargeValueReturnsBoundedPrefix(t *testing.T) {
	nativeAX.once.Do(loadNativeAXAPI)
	if nativeAX.err != nil {
		t.Fatal(nativeAX.err)
	}
	for _, text := range []string{strings.Repeat("readable paragraph ", 40000), strings.Repeat("A🙂é日本語 ", 40000)} {
		storage, ptr := nativeCString(text)
		value := nativeAX.api.stringNew(0, ptr, cfUTF8)
		runtime.KeepAlive(storage)
		if value == 0 {
			t.Fatal("CFString fixture failed")
		}
		got, truncated, err := copyCFString(nativeAX.api, value, 32)
		nativeAX.api.release(value)
		want, _ := truncateUTF8(text, 32)
		if err != nil || !truncated || got != want {
			t.Fatalf("large AX value lost: gotBytes=%d wantBytes=%d truncated=%t err=%v", len(got), len(want), truncated, err)
		}
	}
}

func TestNativeCFStringRangeReadsCompleteValueBeyondRecordLimit(t *testing.T) {
	nativeAX.once.Do(loadNativeAXAPI)
	if nativeAX.err != nil {
		t.Fatal(nativeAX.err)
	}
	text := strings.Repeat("A🙂é日本語 \n", 100000)
	if len(text) <= MaxObservationTextBytes {
		t.Fatal("fixture is not larger than one source")
	}
	storage, ptr := nativeCString(text)
	value := nativeAX.api.stringNew(0, ptr, cfUTF8)
	runtime.KeepAlive(storage)
	if value == 0 {
		t.Fatal("native string fixture failed")
	}
	defer nativeAX.api.release(value)
	var result strings.Builder
	var offset int64
	for {
		part, next, more, err := copyCFStringRange(context.Background(), nativeAX.api, value, offset, 4096)
		if err != nil || next <= offset || len(part) > 4096 || !utf8.ValidString(part) {
			t.Fatal("invalid native text continuation", len(part), offset, next, more, err)
		}
		result.WriteString(part)
		offset = next
		if !more {
			break
		}
	}
	if result.String() != text {
		t.Fatal("native text continuation skipped or repeated text", len(result.String()), len(text))
	}
}

func TestNativeCFStringRangePreservesNULAndSurrogateBoundaries(t *testing.T) {
	nativeAX.once.Do(loadNativeAXAPI)
	if nativeAX.err != nil {
		t.Fatal(nativeAX.err)
	}
	var create func(uintptr, uintptr, int64) uintptr
	purego.RegisterLibFunc(&create, nativeAX.api.core, "CFStringCreateWithCharacters")
	units := []uint16{'a', 'b', 0, 'c', 'd', 0xd83d, 0xde42}
	value := create(0, uintptr(unsafe.Pointer(&units[0])), int64(len(units)))
	runtime.KeepAlive(units)
	if value == 0 {
		t.Fatal("native UTF16 fixture failed")
	}
	defer nativeAX.api.release(value)
	text, more, err := copyCFString(nativeAX.api, value, 32)
	if err != nil || more || text != "ab cd🙂" {
		t.Fatal("embedded NUL discarded text", text, more, err)
	}
	if _, _, _, err := copyCFStringRange(context.Background(), nativeAX.api, value, 6, 32); err == nil {
		t.Fatal("continuation admitted middle of a surrogate pair")
	}
	text, next, more, err := copyCFStringRange(context.Background(), nativeAX.api, value, 5, 3)
	if err != nil || text != "" || next != 5 || !more {
		t.Fatal("UTF8 rune was split", text, next, more, err)
	}
}

func TestNativeCFStringRangeHonorsCancellationDuringConversion(t *testing.T) {
	nativeAX.once.Do(loadNativeAXAPI)
	if nativeAX.err != nil {
		t.Fatal(nativeAX.err)
	}
	text := strings.Repeat("readable ", 10000)
	storage, ptr := nativeCString(text)
	value := nativeAX.api.stringNew(0, ptr, cfUTF8)
	runtime.KeepAlive(storage)
	if value == 0 {
		t.Fatal("native string fixture failed")
	}
	defer nativeAX.api.release(value)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	api := nativeAX.api
	original := api.stringCharacter
	calls := 0
	api.stringCharacter = func(value uintptr, index int64) uint16 {
		calls++
		if calls == 100 {
			cancel()
		}
		return original(value, index)
	}
	got, next, _, err := copyCFStringRange(ctx, api, value, 0, 4096)
	if !errors.Is(err, context.Canceled) || got != "" || next != 0 || calls > 192 {
		t.Fatal("native conversion ignored cancellation", len(got), next, calls, err)
	}
}

func TestNativeChildrenPagesReachBeyondFrameLimitAndFenceCountChanges(t *testing.T) {
	nativeAX.once.Do(loadNativeAXAPI)
	if nativeAX.err != nil {
		t.Fatal(nativeAX.err)
	}
	base := nativeAX.api
	api := base
	api.setTimeout = func(uintptr, float32) int32 { return axSuccess }
	count := int64(6000)
	api.valueCount = func(_, _ uintptr, out *int64) int32 { *out = count; return axSuccess }
	var start, size int64
	calls, retained, released := 0, 0, 0
	api.copyValues = func(_, _ uintptr, index, limit int64, out *uintptr) int32 {
		calls++
		start, size = index, limit
		*out = 999
		return axSuccess
	}
	api.arrayType = func() uintptr { return 8 }
	api.getTypeID = func(value uintptr) uintptr {
		if value == 999 {
			return 8
		}
		return axElementTypeID()
	}
	api.arrayCount = func(uintptr) int64 { return size }
	api.arrayValue = func(_ uintptr, index int64) uintptr { return uintptr(100000 + start + index) }
	api.retain = func(value uintptr) uintptr { retained++; return value }
	api.release = func(value uintptr) {
		if value == 999 {
			return
		}
		if value >= 100000 && value < 106001 {
			released++
			return
		}
		base.release(value)
	}
	node := &nativeAXNode{api: api, element: 42}
	children, total, err := node.childrenPage(context.Background(), 4096, 128, 6000)
	if err != nil || len(children) != 128 || total != 6000 || start != 4096 || size != 128 || children[0].identity() != 104096 || retained != 128 {
		t.Fatal("native child continuation read the wrong page", len(children), total, start, size, retained, err)
	}
	releaseAXNodes(children)
	if released != 128 {
		t.Fatal("continuation leaked native child references", released)
	}
	count = 6001
	if _, _, err := node.childrenPage(context.Background(), 4224, 128, 6000); !errors.Is(err, errBrowserPageChanged) || calls != 1 {
		t.Fatal("changed parent admitted continuation", calls, err)
	}
	count = 6000
	node.api.arrayCount = func(uintptr) int64 { return 64 }
	children, total, err = node.childrenPage(context.Background(), 4096, 128, 6000)
	if err != nil || len(children) != 64 || total != 6000 {
		t.Fatal("bounded short read discarded valid children", len(children), total, err)
	}
	releaseAXNodes(children)
	if released != 192 {
		t.Fatal("short child page leaked references", released)
	}
	node.api.arrayCount = func(uintptr) int64 { return 0 }
	if _, _, err := node.childrenPage(context.Background(), 4096, 128, 6000); err == nil {
		t.Fatal("zero-progress child page was admitted")
	}
}

func TestNativeCFStringFastPathChecksCancellationBeforeReturningText(t *testing.T) {
	nativeAX.once.Do(loadNativeAXAPI)
	if nativeAX.err != nil {
		t.Fatal(nativeAX.err)
	}
	storage, ptr := nativeCString("Small readable text")
	value := nativeAX.api.stringNew(0, ptr, cfUTF8)
	runtime.KeepAlive(storage)
	if value == 0 {
		t.Fatal("native string fixture failed")
	}
	defer nativeAX.api.release(value)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	api := nativeAX.api
	original := api.stringCString
	api.stringCString = func(value, buffer uintptr, size int64, encoding uint32) uint8 {
		result := original(value, buffer, size, encoding)
		cancel()
		return result
	}
	text, next, _, err := copyCFStringRange(ctx, api, value, 0, 64)
	if !errors.Is(err, context.Canceled) || text != "" || next != 0 {
		t.Fatal("cancelled fast conversion returned text", len(text), next, err)
	}
}
