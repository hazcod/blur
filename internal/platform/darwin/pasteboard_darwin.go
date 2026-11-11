package darwin

import (
	"errors"
	"strings"
	"unsafe"

	"github.com/ebitengine/purego/objc"
)

var errPasteboardUnavailable = errors.New("darwin: pasteboard unavailable")

// CopyToPasteboard replaces the general pasteboard's contents with plain text.
// Call on the AppKit main thread.
func CopyToPasteboard(text string) error {
	text = strings.ToValidUTF8(strings.ReplaceAll(text, "\x00", ""), "")
	board := objc.Send[objc.ID](objc.ID(objc.GetClass("NSPasteboard")), objc.RegisterName("generalPasteboard"))
	if board == 0 {
		return errPasteboardUnavailable
	}
	nsString := func(value string) objc.ID {
		raw := append([]byte(value), 0)
		return objc.Send[objc.ID](objc.ID(objc.GetClass("NSString")), objc.RegisterName("stringWithUTF8String:"), unsafe.Pointer(&raw[0]))
	}
	value, kind := nsString(text), nsString("public.utf8-plain-text")
	if value == 0 || kind == 0 {
		return errPasteboardUnavailable
	}
	objc.Send[int64](board, objc.RegisterName("clearContents"))
	if !objc.Send[bool](board, objc.RegisterName("setString:forType:"), value, kind) {
		return errPasteboardUnavailable
	}
	return nil
}
