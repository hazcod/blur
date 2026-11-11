//go:build darwin

package capture

import (
	"runtime"
	"strings"
	"testing"

	"github.com/hazcod/blur/internal/platform/darwin"
)

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
