//go:build darwin

package darwin

import (
	"os"
	"unsafe"

	"github.com/ebitengine/purego/objc"
)

const (
	nsModalResponseOK                        = 1
	nsURLBookmarkCreationWithSecurityScope   = 1 << 11
	nsURLBookmarkResolutionWithSecurityScope = 1 << 10
	nsUTF8StringEncoding                     = 4
)

func sel(name string) objc.SEL { return objc.RegisterName(name) }

func nsStringFrom(value string) objc.ID {
	raw := append([]byte(value), 0)
	return objc.Send[objc.ID](objc.ID(objc.GetClass("NSString")), sel("stringWithUTF8String:"), unsafe.Pointer(&raw[0]))
}

func goStringFrom(str objc.ID) (string, bool) {
	if str == 0 {
		return "", false
	}
	buffer := make([]byte, 4097)
	if !objc.Send[bool](str, sel("getCString:maxLength:encoding:"), unsafe.Pointer(&buffer[0]), uint64(len(buffer)), uint64(nsUTF8StringEncoding)) {
		return "", false
	}
	for i, b := range buffer {
		if b == 0 {
			return string(buffer[:i]), i > 0
		}
	}
	return "", false
}

func urlPath(url objc.ID) (string, bool) {
	if url == 0 {
		return "", false
	}
	return goStringFrom(objc.Send[objc.ID](url, sel("path")))
}

func fileURL(path string) objc.ID {
	return objc.Send[objc.ID](objc.ID(objc.GetClass("NSURL")), sel("fileURLWithPath:isDirectory:"), nsStringFrom(path), true)
}

// ChooseFolder shows a modal folder picker starting at startPath (see
// DefaultFolderStart) and returns the chosen path plus a security-scoped
// bookmark so a sandboxed app can reopen the folder after relaunch. Call on
// the AppKit main thread.
func ChooseFolder(startPath string) (string, []byte, error) {
	panelClass := objc.GetClass("NSOpenPanel")
	if panelClass == 0 {
		return "", nil, ErrFolderPickerUnavailable
	}
	panel := objc.Send[objc.ID](objc.ID(panelClass), sel("openPanel"))
	if panel == 0 {
		return "", nil, ErrFolderPickerUnavailable
	}
	objc.Send[objc.ID](panel, sel("setCanChooseFiles:"), false)
	objc.Send[objc.ID](panel, sel("setCanChooseDirectories:"), true)
	objc.Send[objc.ID](panel, sel("setCanCreateDirectories:"), true)
	objc.Send[objc.ID](panel, sel("setAllowsMultipleSelection:"), false)
	objc.Send[objc.ID](panel, sel("setPrompt:"), nsStringFrom("Choose"))
	if startPath != "" {
		if start := fileURL(startPath); start != 0 {
			objc.Send[objc.ID](panel, sel("setDirectoryURL:"), start)
		}
	}
	if objc.Send[int64](panel, sel("runModal")) != nsModalResponseOK {
		return "", nil, ErrFolderPickerCancelled
	}
	url := objc.Send[objc.ID](panel, sel("URL"))
	path, ok := urlPath(url)
	if !ok {
		return "", nil, ErrFolderPickerUnavailable
	}
	bookmark, err := bookmarkForURL(url)
	if err != nil {
		return "", nil, err
	}
	return path, bookmark, nil
}

func bookmarkForURL(url objc.ID) ([]byte, error) {
	var bookmarkError objc.ID
	data := objc.Send[objc.ID](url, sel("bookmarkDataWithOptions:includingResourceValuesForKeys:relativeToURL:error:"),
		uint64(nsURLBookmarkCreationWithSecurityScope), objc.ID(0), objc.ID(0), unsafe.Pointer(&bookmarkError))
	bookmark, ok := dataBytes(data)
	if !ok {
		return nil, ErrBookmarkInvalid
	}
	return bookmark, nil
}

// CreateBookmark makes fresh security-scoped bookmark data for path. Use it to
// replace a stale bookmark while access to the folder is active.
func CreateBookmark(path string) ([]byte, error) {
	url := fileURL(path)
	if url == 0 {
		return nil, ErrBookmarkInvalid
	}
	return bookmarkForURL(url)
}

func dataBytes(data objc.ID) ([]byte, bool) {
	if data == 0 {
		return nil, false
	}
	length := objc.Send[uint64](data, sel("length"))
	if length == 0 || length > MaxBookmarkBytes {
		return nil, false
	}
	out := make([]byte, length)
	objc.Send[objc.ID](data, sel("getBytes:length:"), unsafe.Pointer(&out[0]), length)
	return out, true
}

// ResolveBookmark resolves saved security-scoped bookmark data, starts access
// to the folder and returns its path. The caller must call release when done
// (it stops access). stale reports that the bookmark should be re-created.
// Safe to call from any goroutine.
func ResolveBookmark(data []byte) (path string, release func(), stale bool, err error) {
	if len(data) == 0 || len(data) > MaxBookmarkBytes {
		return "", nil, false, ErrBookmarkInvalid
	}
	nsData := objc.Send[objc.ID](objc.ID(objc.GetClass("NSData")), sel("dataWithBytes:length:"), unsafe.Pointer(&data[0]), uint64(len(data)))
	if nsData == 0 {
		return "", nil, false, ErrBookmarkInvalid
	}
	var isStale bool
	var resolveError objc.ID
	url := objc.Send[objc.ID](objc.ID(objc.GetClass("NSURL")), sel("URLByResolvingBookmarkData:options:relativeToURL:bookmarkDataIsStale:error:"),
		nsData, uint64(nsURLBookmarkResolutionWithSecurityScope), objc.ID(0), unsafe.Pointer(&isStale), unsafe.Pointer(&resolveError))
	if url == 0 {
		return "", nil, false, ErrBookmarkInvalid
	}
	path, ok := urlPath(url)
	if !ok {
		return "", nil, false, ErrBookmarkInvalid
	}
	// Unsandboxed builds return false here; the path is still readable.
	started := objc.Send[bool](url, sel("startAccessingSecurityScopedResource"))
	if _, statErr := os.Stat(path); statErr != nil {
		if started {
			objc.Send[objc.ID](url, sel("stopAccessingSecurityScopedResource"))
		}
		return "", nil, false, ErrBookmarkInvalid
	}
	release = func() {
		if started {
			objc.Send[objc.ID](url, sel("stopAccessingSecurityScopedResource"))
			started = false
		}
	}
	return path, release, isStale, nil
}
