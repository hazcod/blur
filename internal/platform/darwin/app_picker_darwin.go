//go:build darwin

package darwin

import (
	"strings"

	"github.com/ebitengine/purego/objc"
)

const nsApplicationActivationPolicyRegular = 0

func withAutoreleasePool(fn func()) {
	pool := objc.ID(objc.GetClass("NSAutoreleasePool")).Send(sel("alloc"))
	pool = pool.Send(sel("init"))
	if pool != 0 {
		defer pool.Send(sel("drain"))
	}
	fn()
}

func sharedWorkspace() objc.ID {
	class := objc.GetClass("NSWorkspace")
	if class == 0 {
		return 0
	}
	return objc.ID(class).Send(sel("sharedWorkspace"))
}

func displayNameAtPath(path string) string {
	manager := objc.ID(objc.GetClass("NSFileManager")).Send(sel("defaultManager"))
	if manager == 0 {
		return ""
	}
	name, _ := goStringFrom(objc.Send[objc.ID](manager, sel("displayNameAtPath:"), nsStringFrom(path)))
	return strings.TrimSuffix(name, ".app")
}

// RunningApplications lists the regular (Dock-visible) applications that are
// running now, deduplicated by bundle identifier. Call on the AppKit main thread.
func RunningApplications() []Application {
	var out []Application
	withAutoreleasePool(func() {
		workspace := sharedWorkspace()
		if workspace == 0 {
			return
		}
		list := workspace.Send(sel("runningApplications"))
		if list == 0 {
			return
		}
		count := objc.Send[uint64](list, sel("count"))
		seen := map[string]bool{}
		for i := uint64(0); i < count; i++ {
			app := objc.Send[objc.ID](list, sel("objectAtIndex:"), i)
			if app == 0 || objc.Send[int64](app, sel("activationPolicy")) != nsApplicationActivationPolicyRegular {
				continue
			}
			id, ok := goStringFrom(objc.Send[objc.ID](app, sel("bundleIdentifier")))
			if !ok || seen[id] {
				continue
			}
			seen[id] = true
			name, _ := goStringFrom(objc.Send[objc.ID](app, sel("localizedName")))
			if name == "" {
				name = id
			}
			out = append(out, Application{Name: name, BundleID: id})
		}
	})
	return out
}

// ChooseApplication shows a modal panel on /Applications and returns the
// chosen .app bundle. Call on the AppKit main thread.
func ChooseApplication() (Application, error) {
	var result Application
	err := ErrApplicationPickerUnavailable
	withAutoreleasePool(func() {
		panelClass := objc.GetClass("NSOpenPanel")
		if panelClass == 0 {
			return
		}
		panel := objc.Send[objc.ID](objc.ID(panelClass), sel("openPanel"))
		if panel == 0 {
			return
		}
		types := objc.Send[objc.ID](objc.ID(objc.GetClass("NSArray")), sel("arrayWithObject:"), nsStringFrom("app"))
		objc.Send[objc.ID](panel, sel("setCanChooseFiles:"), true)
		objc.Send[objc.ID](panel, sel("setCanChooseDirectories:"), false)
		objc.Send[objc.ID](panel, sel("setTreatsFilePackagesAsDirectories:"), false)
		objc.Send[objc.ID](panel, sel("setAllowsMultipleSelection:"), false)
		objc.Send[objc.ID](panel, sel("setAllowedFileTypes:"), types)
		objc.Send[objc.ID](panel, sel("setPrompt:"), nsStringFrom("Choose"))
		if start := fileURL("/Applications"); start != 0 {
			objc.Send[objc.ID](panel, sel("setDirectoryURL:"), start)
		}
		if objc.Send[int64](panel, sel("runModal")) != nsModalResponseOK {
			err = ErrApplicationPickerCancelled
			return
		}
		url := objc.Send[objc.ID](panel, sel("URL"))
		path, ok := urlPath(url)
		if !ok {
			return
		}
		bundle := objc.Send[objc.ID](objc.ID(objc.GetClass("NSBundle")), sel("bundleWithURL:"), url)
		if bundle == 0 {
			return
		}
		id, ok := goStringFrom(objc.Send[objc.ID](bundle, sel("bundleIdentifier")))
		if !ok {
			return
		}
		name := displayNameAtPath(path)
		if name == "" {
			name = id
		}
		result, err = Application{Name: name, BundleID: id}, nil
	})
	return result, err
}

// ApplicationName resolves an installed application's display name from its
// bundle identifier.
func ApplicationName(bundleID string) (string, bool) {
	if bundleID == "" {
		return "", false
	}
	var name string
	withAutoreleasePool(func() {
		workspace := sharedWorkspace()
		if workspace == 0 {
			return
		}
		url := objc.Send[objc.ID](workspace, sel("URLForApplicationWithBundleIdentifier:"), nsStringFrom(bundleID))
		if path, ok := urlPath(url); ok {
			name = displayNameAtPath(path)
		}
	})
	return name, name != ""
}
