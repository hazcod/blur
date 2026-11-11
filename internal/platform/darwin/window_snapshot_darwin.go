package darwin

import (
	"errors"
	"math"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

var (
	errInvalidSnapshotRequest = errors.New("darwin: invalid window snapshot request")
	errSnapshotUnavailable    = errors.New("darwin: window snapshot unavailable")
)

const (
	cgWindowListOptionIncludingWindow = 1 << 3
	cgWindowImageBoundsIgnoreFraming  = 1 << 0
	cgWindowImageBestResolution       = 1 << 3
	cfStringEncodingUTF8              = 0x08000100
	nsBitmapImageFileTypePNG          = 4
)

type cgRect struct{ X, Y, Width, Height float64 }

type snapshotAPI struct {
	createImage      func(cgRect, uint32, uint32, uint32) uintptr
	release          func(uintptr)
	urlFromPath      func(allocator uintptr, path *byte, length int, directory bool) uintptr
	stringFromCStr   func(allocator uintptr, value *byte, encoding uint32) uintptr
	destinationNew   func(url, imageType uintptr, count uint64, options uintptr) uintptr
	destinationAdd   func(destination, image, properties uintptr)
	destinationFinal func(destination uintptr) bool
	hasCreateImage   bool
}

func loadSnapshotAPI() (*snapshotAPI, error) {
	cg, err := purego.Dlopen("/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return nil, errSnapshotUnavailable
	}
	cf, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return nil, errSnapshotUnavailable
	}
	io, err := purego.Dlopen("/System/Library/Frameworks/ImageIO.framework/ImageIO", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return nil, errSnapshotUnavailable
	}
	api := &snapshotAPI{}
	purego.RegisterLibFunc(&api.release, cf, "CFRelease")
	purego.RegisterLibFunc(&api.urlFromPath, cf, "CFURLCreateFromFileSystemRepresentation")
	purego.RegisterLibFunc(&api.stringFromCStr, cf, "CFStringCreateWithCString")
	purego.RegisterLibFunc(&api.destinationNew, io, "CGImageDestinationCreateWithURL")
	purego.RegisterLibFunc(&api.destinationAdd, io, "CGImageDestinationAddImage")
	purego.RegisterLibFunc(&api.destinationFinal, io, "CGImageDestinationFinalize")
	// The symbol is deprecated in recent SDKs; tolerate its absence and use the
	// AppKit view-cache fallback instead.
	if _, err := purego.Dlsym(cg, "CGWindowListCreateImage"); err == nil {
		purego.RegisterLibFunc(&api.createImage, cg, "CGWindowListCreateImage")
		api.hasCreateImage = true
	}
	return api, nil
}

// SnapshotWindow writes a PNG of one of Blur's own NSWindows. An application
// may capture its own windows without Screen Recording permission. Call on the
// AppKit main thread with a handle from PlatformWindow().NativeHandle().
func SnapshotWindow(handle uintptr, path string) error {
	if handle == 0 || path == "" {
		return errInvalidSnapshotRequest
	}
	api, err := loadSnapshotAPI()
	if err != nil {
		return err
	}
	window := objc.ID(handle)
	if api.hasCreateImage {
		windowID := objc.Send[uint32](window, objc.RegisterName("windowNumber"))
		if windowID != 0 {
			nullRect := cgRect{X: math.Inf(1), Y: math.Inf(1)}
			image := api.createImage(nullRect, cgWindowListOptionIncludingWindow, windowID, cgWindowImageBoundsIgnoreFraming|cgWindowImageBestResolution)
			if image != 0 {
				defer api.release(image)
				if api.writePNG(image, path) == nil {
					return nil
				}
			}
		}
	}
	return snapshotViaCache(window, path)
}

func (api *snapshotAPI) writePNG(image uintptr, path string) error {
	raw := append([]byte(path), 0)
	url := api.urlFromPath(0, &raw[0], len(path), false)
	if url == 0 {
		return errSnapshotUnavailable
	}
	defer api.release(url)
	typeName := append([]byte("public.png"), 0)
	imageType := api.stringFromCStr(0, &typeName[0], cfStringEncodingUTF8)
	if imageType == 0 {
		return errSnapshotUnavailable
	}
	defer api.release(imageType)
	destination := api.destinationNew(url, imageType, 1, 0)
	if destination == 0 {
		return errSnapshotUnavailable
	}
	defer api.release(destination)
	api.destinationAdd(destination, image, 0)
	if !api.destinationFinal(destination) {
		return errSnapshotUnavailable
	}
	return nil
}

type nsRect struct{ X, Y, Width, Height float64 }

func snapshotViaCache(window objc.ID, path string) error {
	content := objc.Send[objc.ID](window, objc.RegisterName("contentView"))
	if content == 0 {
		return errSnapshotUnavailable
	}
	bounds := objc.Send[nsRect](content, objc.RegisterName("bounds"))
	rep := objc.Send[objc.ID](content, objc.RegisterName("bitmapImageRepForCachingDisplayInRect:"), bounds)
	if rep == 0 {
		return errSnapshotUnavailable
	}
	objc.Send[objc.ID](content, objc.RegisterName("cacheDisplayInRect:toBitmapImageRep:"), bounds, rep)
	data := objc.Send[objc.ID](rep, objc.RegisterName("representationUsingType:properties:"), uint64(nsBitmapImageFileTypePNG), objc.ID(0))
	if data == 0 {
		return errSnapshotUnavailable
	}
	raw := append([]byte(path), 0)
	nsPath := objc.Send[objc.ID](objc.ID(objc.GetClass("NSString")), objc.RegisterName("stringWithUTF8String:"), unsafe.Pointer(&raw[0]))
	if nsPath == 0 || !objc.Send[bool](data, objc.RegisterName("writeToFile:atomically:"), nsPath, true) {
		return errSnapshotUnavailable
	}
	return nil
}

// DisplayWindowNow forces a synchronous paint of a window's content so a
// capture taken right after sees real pixels. Call on the AppKit main thread.
func DisplayWindowNow(handle uintptr) {
	if handle == 0 {
		return
	}
	window := objc.ID(handle)
	if content := objc.Send[objc.ID](window, objc.RegisterName("contentView")); content != 0 {
		objc.Send[objc.ID](content, objc.RegisterName("display"))
	}
	objc.Send[objc.ID](window, objc.RegisterName("displayIfNeeded"))
}
