package darwin

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/ebitengine/purego"
)

var screenTextLibraries struct {
	sync.Mutex
	byPath map[string]screenTextNativeAPI
}

// OpenScreenTextBridge loads an explicitly selected bundled native bridge. It
// never searches the network, PATH, environment variables or other app bundles.
// Bundle construction must sign this library with Blur's existing certificate.
func OpenScreenTextBridge(path string) (*ScreenTextBridge, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, ErrScreenTextInvalid
	}
	// The caller selects Blur's bundled native library, not a path from captured
	// content. Absolute/canonical validation above rejects traversal syntax;
	// Hardened Runtime library validation enforces the signing trust boundary.
	info, err := os.Stat(path) // #nosec G703 -- Intentionally stat the caller-selected, absolute bundled FFI library; no file contents are read here.
	if err != nil || !info.Mode().IsRegular() {
		return nil, ErrScreenTextUnavailable
	}
	screenTextLibraries.Lock()
	defer screenTextLibraries.Unlock()
	if api, ok := screenTextLibraries.byPath[path]; ok {
		return newScreenTextBridge(api), nil
	}
	library, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return nil, ErrScreenTextUnavailable
	}
	start, err := purego.Dlsym(library, "blur_screen_text_start")
	if err != nil {
		return nil, ErrScreenTextUnavailable
	}
	poll, err := purego.Dlsym(library, "blur_screen_text_poll")
	if err != nil {
		return nil, ErrScreenTextUnavailable
	}
	cancel, err := purego.Dlsym(library, "blur_screen_text_cancel")
	if err != nil {
		return nil, ErrScreenTextUnavailable
	}
	var api screenTextNativeAPI
	purego.RegisterFunc(&api.start, start)
	purego.RegisterFunc(&api.poll, poll)
	purego.RegisterFunc(&api.cancel, cancel)
	if screenTextLibraries.byPath == nil {
		screenTextLibraries.byPath = make(map[string]screenTextNativeAPI)
	}
	screenTextLibraries.byPath[path] = api
	return newScreenTextBridge(api), nil
}
