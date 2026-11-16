package darwin

import (
	"errors"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/ebitengine/purego/objc"
)

var ErrSaveFileCancelled = errors.New("darwin: save canceled")
var ErrSaveFileUnavailable = errors.New("darwin: save dialog unavailable")

// ChooseMarkdownExport asks the user for an export destination. Call on the
// AppKit main thread. NSSavePanel owns any overwrite confirmation.
func ChooseMarkdownExport(name string) (string, error) {
	if name == "" || len(name) > 512 || !utf8.ValidString(name) || strings.ContainsAny(name, "/\\\x00") || filepath.Base(name) != name || !strings.EqualFold(filepath.Ext(name), ".md") {
		return "", ErrSaveFileUnavailable
	}
	class := objc.GetClass("NSSavePanel")
	if class == 0 {
		return "", ErrSaveFileUnavailable
	}
	panel := objc.Send[objc.ID](objc.ID(class), sel("savePanel"))
	if panel == 0 {
		return "", ErrSaveFileUnavailable
	}
	objc.Send[objc.ID](panel, sel("setNameFieldStringValue:"), nsStringFrom(name))
	objc.Send[objc.ID](panel, sel("setCanCreateDirectories:"), true)
	objc.Send[objc.ID](panel, sel("setAllowsOtherFileTypes:"), false)
	types := objc.Send[objc.ID](objc.ID(objc.GetClass("NSArray")), sel("arrayWithObject:"), nsStringFrom("md"))
	objc.Send[objc.ID](panel, sel("setAllowedFileTypes:"), types)
	objc.Send[objc.ID](panel, sel("setPrompt:"), nsStringFrom("Export"))
	if objc.Send[int64](panel, sel("runModal")) != nsModalResponseOK {
		return "", ErrSaveFileCancelled
	}
	url := objc.Send[objc.ID](panel, sel("URL"))
	path, ok := urlPath(url)
	if !ok || !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
		return "", ErrSaveFileUnavailable
	}
	return path, nil
}

// ChooseMarkdownArchive asks for a ZIP destination for an explicit notes export.
// Call it on the AppKit main thread; NSSavePanel owns overwrite confirmation.
func ChooseMarkdownArchive(name string) (string, error) {
	if name == "" || len(name) > 512 || !utf8.ValidString(name) || strings.ContainsAny(name, "/\\\x00") || filepath.Base(name) != name || !strings.EqualFold(filepath.Ext(name), ".zip") {
		return "", ErrSaveFileUnavailable
	}
	class := objc.GetClass("NSSavePanel")
	if class == 0 {
		return "", ErrSaveFileUnavailable
	}
	panel := objc.Send[objc.ID](objc.ID(class), sel("savePanel"))
	if panel == 0 {
		return "", ErrSaveFileUnavailable
	}
	objc.Send[objc.ID](panel, sel("setNameFieldStringValue:"), nsStringFrom(name))
	objc.Send[objc.ID](panel, sel("setCanCreateDirectories:"), true)
	objc.Send[objc.ID](panel, sel("setAllowsOtherFileTypes:"), false)
	types := objc.Send[objc.ID](objc.ID(objc.GetClass("NSArray")), sel("arrayWithObject:"), nsStringFrom("zip"))
	objc.Send[objc.ID](panel, sel("setAllowedFileTypes:"), types)
	objc.Send[objc.ID](panel, sel("setPrompt:"), nsStringFrom("Export"))
	if objc.Send[int64](panel, sel("runModal")) != nsModalResponseOK {
		return "", ErrSaveFileCancelled
	}
	url := objc.Send[objc.ID](panel, sel("URL"))
	path, ok := urlPath(url)
	if !ok || !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
		return "", ErrSaveFileUnavailable
	}
	return path, nil
}
