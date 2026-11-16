package client

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/golang-gui/goui/layout"
	"github.com/golang-gui/goui/theme/modern"
	"github.com/golang-gui/goui/ui"
	"github.com/hazcod/blur/internal/knowledge"
)

func (d *desktop) searchLibrary() {
	x := &d.libraryBrowser
	if len(x.query) > MaxQueryBytes || !utf8.ValidString(x.query) || strings.ContainsRune(x.query, 0) {
		d.status = fmt.Sprintf("Search Library with up to %d bytes of text.", MaxQueryBytes)
		d.app.RequestUpdate()
		return
	}
	x.appliedQuery = strings.TrimSpace(x.query)
	d.loadLibraryBrowserPage(0, false)
}

func (d *desktop) libraryBrowserControls() ui.View {
	x := &d.libraryBrowser
	search := ui.TextInput().ID("library-search").Text(x.query).Placeholder("Search names, facts, and memories").MainWeight(1).
		OnText(func(value string) { x.query = value; d.app.RequestUpdate() }).OnSubmit(d.searchLibrary)
	filters := []ui.View{}
	for _, filter := range []struct {
		label string
		kind  knowledge.Kind
	}{{"All", ""}, {"People", knowledge.Person}, {"Projects", knowledge.Project}, {"Topics", knowledge.Topic}, {"Places", knowledge.Place}, {"Workflows", knowledge.Workflow}} {
		kind := filter.kind
		filters = append(filters, choiceButton(filter.label, x.kind == kind).OnClick(func() {
			x.kind = kind
			d.searchLibrary()
		}))
	}
	count := "Search your complete local Library."
	if d.libraryLoading && !x.refreshing {
		count = "Searching your local Library…"
	} else if d.libraryLoaded && x.active {
		noun := "entries"
		if x.total == 1 {
			noun = "entry"
		}
		count = fmt.Sprintf("%d %s · Page %d of %d", x.total, noun, x.page+1, max(1, (x.total+libraryPageSize-1)/libraryPageSize))
	}
	rows := []ui.View{
		ui.HBox(search, primaryButton("Search").Enabled(len(x.query) <= MaxQueryBytes).OnClick(d.searchLibrary), flatButton("Refresh Library").Enabled(!d.libraryLoading).OnClick(func() { d.loadLibraryBrowserPage(x.page, false) }), flatButton("Clear").Enabled(x.query != "" || x.appliedQuery != "").OnClick(func() {
			x.query = ""
			d.searchLibrary()
		})).Spacing(8).CrossAlign(layout.CrossCenter),
		ui.HBox(filters...).Spacing(8),
		wrapLabel(count).Style(modern.MutedText),
	}
	if x.updatesAvailable {
		rows = append(rows, wrapLabel("New memories are ready. Refresh Library to load them.").Style(modern.MutedText))
	}
	return ui.VBox(rows...).Spacing(8).CrossAlign(layout.CrossStretch)
}

func (d *desktop) libraryBrowserPagination() ui.View {
	x := &d.libraryBrowser
	return ui.HBox(
		flatButton("Previous page").Enabled(x.page > 0 && !d.libraryLoading).OnClick(func() { d.loadLibraryBrowserPage(x.page-1, false) }),
		wrapLabel(fmt.Sprintf("Page %d of %d", x.page+1, max(1, (x.total+libraryPageSize-1)/libraryPageSize))).Style(styleMeta),
		flatButton("Next page").Enabled(d.library.HasMore && !d.libraryLoading).OnClick(func() { d.loadLibraryBrowserPage(x.page+1, false) }),
	).Spacing(8).CrossAlign(layout.CrossCenter)
}
