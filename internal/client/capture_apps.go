package client

import (
	"errors"
	"sort"
	"strings"
	"sync"

	"github.com/golang-gui/goui/layout"
	"github.com/golang-gui/goui/theme/modern"
	"github.com/golang-gui/goui/ui"

	"github.com/hazcod/blur/internal/platform/darwin"
)

const (
	blurBundleID        = "com.hazcod.blur"
	maxRunningAppChoice = 40
)

// The platform helpers return nothing off macOS, so the add menu then offers
// only the (unavailable) picker and the rows fall back to the raw identifier.
var appNames = struct {
	sync.Mutex
	byID map[string]string
}{byID: map[string]string{}}

func rememberAppName(bundleID, name string) {
	if bundleID == "" || name == "" {
		return
	}
	appNames.Lock()
	appNames.byID[bundleID] = name
	appNames.Unlock()
}

// appDisplayName resolves a stored identifier to a readable name; ok is false
// when the app is not installed (or not on macOS).
func appDisplayName(bundleID string) (string, bool) {
	appNames.Lock()
	name, cached := appNames.byID[bundleID]
	appNames.Unlock()
	if cached {
		return name, name != ""
	}
	name, _ = darwin.ApplicationName(bundleID)
	appNames.Lock()
	appNames.byID[bundleID] = name
	appNames.Unlock()
	return name, name != ""
}

func draftEntries(draft string) []string {
	var out []string
	for _, line := range strings.Split(draft, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

func draftContains(draft, id string) bool {
	for _, entry := range draftEntries(draft) {
		if strings.EqualFold(entry, id) {
			return true
		}
	}
	return false
}

func draftWith(draft, id string) string {
	if draftContains(draft, id) {
		return draft
	}
	return strings.Join(append(draftEntries(draft), id), "\n")
}

func draftWithout(draft, id string) string {
	var kept []string
	for _, entry := range draftEntries(draft) {
		if !strings.EqualFold(entry, id) {
			kept = append(kept, entry)
		}
	}
	return strings.Join(kept, "\n")
}

// addableApps lists running regular apps (not Blur, not already listed in
// either capture list), sorted by name, at most limit.
func addableApps(running []darwin.Application, inList func(string) bool, limit int) []darwin.Application {
	var out []darwin.Application
	for _, app := range running {
		if app.BundleID == "" || strings.EqualFold(app.BundleID, blurBundleID) || inList(app.BundleID) {
			continue
		}
		out = append(out, app)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if a != b {
			return a < b
		}
		return out[i].BundleID < out[j].BundleID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// captureAppList renders one app list: a row per entry (name, bundle id,
// Remove), an empty-state line, and an "Add app" menu.
func (d *desktop) captureAppList(draft *string, emptyText string) []ui.View {
	var rows []ui.View
	entries := draftEntries(*draft)
	if len(entries) == 0 {
		rows = append(rows, wrapLabel(emptyText).Style(modern.MutedText))
	}
	for _, id := range entries {
		id := id
		name, ok := appDisplayName(id)
		text := ui.VBox(wrapLabel(id)).Spacing(2)
		if ok {
			text = ui.VBox(wrapLabel(name), wrapLabel(id).Style(modern.MutedText)).Spacing(2)
		}
		rows = append(rows, ui.HBox(
			text.MainWeight(1),
			flatButton("Remove").Enabled(!d.loading).OnClick(func() {
				*draft = draftWithout(*draft, id)
				d.exclusionValidation = ""
				d.app.RequestUpdate()
			}),
		).Spacing(8).CrossAlign(layout.CrossCenter))
	}
	rows = append(rows, hug(d.addAppMenu(draft)))
	return rows
}

func (d *desktop) addAppMenu(draft *string) ui.View {
	add := func(app darwin.Application) {
		rememberAppName(app.BundleID, app.Name)
		*draft = draftWith(*draft, app.BundleID)
		d.exclusionValidation = ""
		d.app.RequestUpdate()
	}
	inList := func(id string) bool {
		return draftContains(d.includedAppsDraft, id) || draftContains(d.excludedAppsDraft, id)
	}
	var items []*ui.MenuItemView
	for _, app := range addableApps(darwin.RunningApplications(), inList, maxRunningAppChoice) {
		app := app
		items = append(items, ui.MenuItem(app.Name, func() { add(app) }))
	}
	items = append(items, ui.MenuItem("Choose from Applications…", func() {
		app, err := darwin.ChooseApplication()
		switch {
		case errors.Is(err, darwin.ErrApplicationPickerCancelled):
			return
		case err != nil:
			d.exclusionValidation = "That app could not be read. Choose another one."
			d.app.RequestUpdate()
		default:
			add(app)
		}
	}))
	return ui.MenuButton("Add app").Menu(items...).Enabled(!d.loading)
}
