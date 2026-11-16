package client

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/golang-gui/goui/layout"
	"github.com/golang-gui/goui/theme/modern"
	"github.com/golang-gui/goui/ui"
	"github.com/hazcod/blur/internal/profile"
)

type profileViewState struct {
	page                     profile.BrowsePage
	loaded, loading, editing bool
	request                  uint64
	cancel                   context.CancelFunc
	query, loadError         string
	filterKind               profile.Kind
	filterState              profile.State
	after                    string
	before                   []string
	editor                   *profile.Record
	kind                     profile.Kind
	text                     string
	scope                    profile.Scope
	pendingDelete            string
}
type profileSubmission struct {
	Input         string
	Record        profile.Record
	DraftRevision uint64
}

func profileScopeCopy(scope profile.Scope) string {
	parts := []string{}
	for _, field := range []struct{ label, value string }{{"App", scope.AppID}, {"Website", scope.Domain}, {"Project", scope.Project}, {"Audience", scope.Audience}} {
		if field.value != "" {
			parts = append(parts, field.label+": "+field.value)
		}
	}
	if len(parts) == 0 {
		return "All contexts"
	}
	return strings.Join(parts, "; ")
}

func (d *desktop) profileView() ui.View {
	if _, ok := d.client.profileRepository(); !ok {
		return wrapLabel("Preferences require encrypted storage.")
	}
	x := &d.profileUI
	rows := []ui.View{wrapLabel("Preferences and writing rules").Style(styleSectionTitle), wrapLabel("Review what you have asked Blur to remember. Paused items are kept but do not apply.").Style(modern.MutedText)}
	rows = append(rows, d.profileBrowseToolbar())
	if d.status != "" {
		rows = append(rows, wrapLabel(d.status).Style(styleMeta))
	}
	if x.editing {
		rows = append(rows, d.profileEditorView())
	} else {
		rows = append(rows, hug(primaryButton("Add a preference or writing rule").Enabled(!d.loading).OnClick(func() {
			x.editing, x.editor, x.kind, x.text, x.scope = true, nil, profile.Fact, "", profile.Scope{}
			d.app.RequestUpdate()
		})))
	}
	if x.loadError != "" {
		rows = append(rows, surfaceBox(12, 8, wrapLabel(x.loadError), hug(flatButton("Try again").Enabled(!d.loading && !x.loading).OnClick(func() { d.loadProfilePage(x.after) }))))
	} else if x.loading {
		label := "Loading saved items…"
		if x.loaded {
			label = "Updating saved items…"
		}
		rows = append(rows, wrapLabel(label).Style(styleMeta))
	} else if !x.loaded {
		rows = append(rows, hug(flatButton("Load saved items").Enabled(!d.loading).OnClick(func() { d.loadProfilePage("") })))
	}
	if x.loaded && x.loadError == "" {
		rows = append(rows, d.profileBrowsePager())
	}
	for _, record := range x.page.Records {
		r := record
		kind, state := "Preference", "Active"
		if r.Kind == profile.WritingRule {
			kind = "Writing rule"
		}
		if !r.Enabled {
			state = "Paused"
		}
		buttons := []ui.View{flatButton("Edit").Enabled(!d.loading && !x.loading).OnClick(func() {
			copy := r
			x.editing, x.editor, x.kind, x.text, x.scope = true, &copy, r.Kind, r.Text, r.Scope
			d.app.RequestUpdate()
		})}
		label := "Pause"
		if !r.Enabled {
			label = "Enable"
		}
		buttons = append(buttons, flatButton(label).Enabled(!d.loading && !x.loading).OnClick(func() { r.Enabled = !r.Enabled; d.updateProfileRecord(r) }))
		if x.pendingDelete == r.ID {
			buttons = append(buttons, dangerButton("Delete permanently").Enabled(!d.loading && !x.loading).OnClick(func() { d.removeProfileRecord(r) }), flatButton("Keep").OnClick(func() { x.pendingDelete = ""; d.app.RequestUpdate() }))
		} else {
			buttons = append(buttons, flatButton("Delete").Enabled(!d.loading && !x.loading).OnClick(func() { x.pendingDelete = r.ID; d.app.RequestUpdate() }))
		}
		rows = append(rows, surfaceBox(14, 8, wrapLabel(kind+"; "+state).Style(styleMeta), wrapLabel(r.Text), wrapLabel(profileScopeCopy(r.Scope)).Style(modern.MutedText), ui.HBox(buttons...).Spacing(8)))
	}
	if x.loaded && !x.loading && x.loadError == "" && len(x.page.Records) == 0 {
		if x.page.TotalRecords == 0 {
			rows = append(rows, wrapLabel("No saved items yet. Add a preference or writing rule, or tell Quick ask what to remember.").Style(modern.MutedText))
		} else {
			rows = append(rows, wrapLabel("No saved items match this search. Try different words or clear the filters.").Style(modern.MutedText))
		}
	}
	return pageScroll(ui.VBox(hugButtons(rows)...).Spacing(14).CrossAlign(layout.CrossStretch))
}

const profileDisplayPageSize = 20

func (d *desktop) profileBrowseToolbar() ui.View {
	x := &d.profileUI
	kinds := []ui.View{wrapLabel("Type").Style(styleMeta)}
	for _, option := range []struct {
		label string
		kind  profile.Kind
	}{{"All", ""}, {"Preferences", profile.Fact}, {"Writing rules", profile.WritingRule}} {
		value := option.kind
		kinds = append(kinds, choiceButton(option.label, x.filterKind == value).Enabled(!d.loading).OnClick(func() { d.setProfileBrowse(x.query, value, x.filterState) }))
	}
	states := []ui.View{wrapLabel("Status").Style(styleMeta)}
	for _, option := range []struct {
		label string
		state profile.State
	}{{"All", ""}, {"Active", profile.Active}, {"Paused", profile.Paused}} {
		value := option.state
		states = append(states, choiceButton(option.label, x.filterState == value).Enabled(!d.loading).OnClick(func() { d.setProfileBrowse(x.query, x.filterKind, value) }))
	}
	return ui.VBox(
		wrapLabel("Search saved items").Style(styleMeta),
		ui.HBox(ui.TextInput().ID("profile-search").Text(x.query).Placeholder("Text, app, website, project, or audience").Enabled(!d.loading).OnText(func(value string) { d.setProfileBrowse(value, x.filterKind, x.filterState) }).OnSubmit(func() { x.before = nil; d.loadProfilePage("") }).MainWeight(1), flatButton("Refresh").Enabled(!d.loading && !x.loading).OnClick(func() { d.loadProfilePage(x.after) })).Spacing(8),
		ui.HBox(kinds...).Spacing(6).CrossAlign(layout.CrossCenter),
		ui.HBox(append(states, flatButton("Clear filters").Enabled(!d.loading && (strings.TrimSpace(x.query) != "" || x.filterKind != "" || x.filterState != "")).OnClick(func() { d.setProfileBrowse("", "", "") }))...).Spacing(6).CrossAlign(layout.CrossCenter),
	).Spacing(8).CrossAlign(layout.CrossStretch)
}

func (d *desktop) profileBrowsePager() ui.View {
	x := &d.profileUI
	summary := fmt.Sprintf("%d saved items", x.page.TotalRecords)
	if x.page.Total == 0 && x.page.TotalRecords > 0 {
		summary = fmt.Sprintf("0 matches · %d saved items", x.page.TotalRecords)
	}
	if x.page.Total > 0 {
		summary = fmt.Sprintf("%d–%d of %d items · Page %d of %d", x.page.Offset+1, x.page.Offset+len(x.page.Records), x.page.Total, x.page.Offset/profileDisplayPageSize+1, (x.page.Total+profileDisplayPageSize-1)/profileDisplayPageSize)
		if x.page.Total != x.page.TotalRecords {
			summary += fmt.Sprintf(" · %d saved in total", x.page.TotalRecords)
		}
	}
	return ui.HBox(wrapLabel(summary).Style(styleMeta).MainWeight(1),
		flatButton("Previous").Enabled(!d.loading && !x.loading && x.page.Offset > 0).OnClick(func() { d.loadProfilePage(strconv.Itoa(max(0, x.page.Offset-profileDisplayPageSize))) }),
		flatButton("Next").Enabled(!d.loading && !x.loading && x.page.Offset+len(x.page.Records) < x.page.Total).OnClick(func() { d.loadProfilePage(strconv.Itoa(x.page.Offset + profileDisplayPageSize)) }),
	).Spacing(8).CrossAlign(layout.CrossCenter)
}

func (d *desktop) setProfileBrowse(query string, kind profile.Kind, state profile.State) {
	x := &d.profileUI
	if x.query == query && x.filterKind == kind && x.filterState == state {
		return
	}
	x.query, x.filterKind, x.filterState = query, kind, state
	x.after, x.before, x.pendingDelete = "", nil, ""
	x.loaded, x.page, x.loadError = false, profile.BrowsePage{}, ""
	d.loadProfilePage("")
}

func (d *desktop) profileEditorView() ui.View {
	x := &d.profileUI
	kinds := []ui.View{}
	for _, kind := range []profile.Kind{profile.Fact, profile.WritingRule} {
		value, label := kind, "Preference"
		if kind == profile.WritingRule {
			label = "Writing rule"
		}
		kinds = append(kinds, choiceButton(label, x.kind == kind).Enabled(!d.loading && x.editor == nil).OnClick(func() { x.kind = value; d.app.RequestUpdate() }))
	}
	return surfaceBox(14, 10,
		ui.HBox(kinds...).Spacing(8),
		ui.VBox(quickNoteInput(x.text, func(value string) { x.text = value })).MinHeight(90),
		wrapLabel("Scope (optional)").Style(styleSectionTitle),
		wrapLabel("Every filled scope must match. Leave all fields empty to apply everywhere.").Style(styleMeta),
		ui.HBox(ui.TextInput().Text(x.scope.AppID).Placeholder("App bundle ID, e.g. com.tinyspeck.slackmacgap").OnText(func(value string) { x.scope.AppID = value }).MainWeight(1), ui.TextInput().Text(x.scope.Domain).Placeholder("Website, e.g. github.com").OnText(func(value string) { x.scope.Domain = value }).MainWeight(1)).Spacing(8),
		ui.HBox(ui.TextInput().Text(x.scope.Project).Placeholder("Project name").OnText(func(value string) { x.scope.Project = value }).MainWeight(1), ui.TextInput().Text(x.scope.Audience).Placeholder("Audience, e.g. executive team").OnText(func(value string) { x.scope.Audience = value }).MainWeight(1)).Spacing(8),
		ui.HBox(flatButton("Cancel").Enabled(!d.loading).OnClick(func() { x.editing, x.editor, x.text, x.scope = false, nil, "", profile.Scope{}; d.app.RequestUpdate() }), primaryButton("Save").Enabled(!d.loading && strings.TrimSpace(x.text) != "").OnClick(d.saveProfileEditor)).Spacing(8),
	).Enabled(!d.loading)
}

func (d *desktop) loadProfilePage(after string) {
	if d.client == nil {
		return
	}
	epoch, stable := d.client.BeginMemoryRead()
	if !stable {
		return
	}
	x := &d.profileUI
	if x.cancel != nil {
		x.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	x.cancel, x.loading, x.loadError = cancel, true, ""
	x.request++
	request := x.request
	offset, _ := strconv.Atoi(after)
	query := profile.BrowseRequest{Query: x.query, Kind: x.filterKind, State: x.filterState, Offset: offset, Limit: profileDisplayPageSize}
	d.app.RequestUpdate()
	go func() {
		defer cancel()
		page, err := d.client.BrowseProfile(ctx, query)
		d.app.Post(func() {
			if request != d.profileUI.request {
				return
			}
			if !d.client.PublishMemoryRead(epoch, func() {
				d.profileUI.loading = false
				d.profileUI.cancel = nil
				if err != nil {
					d.profileUI.loadError = "Saved items could not be loaded. Try again."
					if errors.Is(err, context.DeadlineExceeded) {
						d.profileUI.loadError = "The search took too long. Try again."
					} else if len(query.Query) > 512 {
						d.profileUI.loadError = "The search could not be read. Try a shorter search."
					}
					d.profileUI.page, d.profileUI.loaded = profile.BrowsePage{}, false
					return
				}
				d.profileUI.page, d.profileUI.loaded, d.profileUI.after = page, true, strconv.Itoa(page.Offset)
			}) {
				d.profileUI.loading, d.profileUI.cancel = false, nil
				d.profileUI.page, d.profileUI.loaded = profile.BrowsePage{}, false
			}
			d.app.RequestUpdate()
		})
	}()
}

func (d *desktop) saveProfileEditor() {
	if d.loading || d.client == nil {
		return
	}
	x := d.profileUI
	if x.editor != nil {
		record := *x.editor
		record.Text, record.Scope = x.text, x.scope
		d.runProfileAction(func(epoch uint64) (uint64, error) {
			result, err := d.client.updateProfileAtEpoch(context.Background(), record, epoch)
			return result.Epoch, err
		}, "Preference updated.", true)
		return
	}
	d.runProfileAction(func(epoch uint64) (uint64, error) {
		result, err := d.client.createProfileAtEpoch(context.Background(), x.kind, x.text, x.scope, epoch)
		return result.Epoch, err
	}, "Preference saved.", true)
}
func (d *desktop) updateProfileRecord(record profile.Record) {
	d.runProfileAction(func(epoch uint64) (uint64, error) {
		result, err := d.client.updateProfileAtEpoch(context.Background(), record, epoch)
		return result.Epoch, err
	}, "Preference updated.", false)
}
func (d *desktop) removeProfileRecord(record profile.Record) {
	d.runProfileAction(func(epoch uint64) (uint64, error) {
		return d.client.deleteProfileAtEpoch(context.Background(), record.ID, record.Revision, epoch)
	}, "Preference deleted.", false)
}
func profileErrorCopy(err error) string {
	if errors.Is(err, profile.ErrConflict) {
		return "This item changed. Reload it before editing or deleting again. Your draft is still here."
	}
	if errors.Is(err, profile.ErrInvalid) {
		return "Check the text (up to 1,024 bytes) and scope fields. Your draft is still here."
	}
	return "The preference could not be saved. Your draft is still here."
}
func (d *desktop) runProfileAction(action func(uint64) (uint64, error), success string, closeEditor bool) {
	if d.loading || d.client == nil {
		return
	}
	epoch, stable := d.client.BeginMemoryRead()
	if !stable {
		return
	}
	view := d.profileUI
	if view.cancel != nil {
		view.cancel()
	}
	d.loading, d.status = true, "Saving encrypted preferences…"
	d.app.RequestUpdate()
	go func() {
		committed, err := action(epoch)
		if err == nil {
			epoch = committed
		}
		d.app.Post(func() {
			if !d.client.PublishMemoryRead(epoch, func() {
				d.loading = false
				if err != nil {
					d.status = profileErrorCopy(err)
					return
				}
				// An explicit write clears cached evidence synchronously. Restore
				// this action's draft and search only after its new epoch is current;
				// a concurrent destructive mutation never enters this callback.
				d.profileUI.query, d.profileUI.filterKind, d.profileUI.filterState = view.query, view.filterKind, view.filterState
				d.profileUI.editing, d.profileUI.editor, d.profileUI.kind = view.editing, view.editor, view.kind
				d.profileUI.text, d.profileUI.scope = view.text, view.scope
				if closeEditor {
					d.profileUI.editing, d.profileUI.editor, d.profileUI.text = false, nil, ""
				}
				d.profileUI.loaded, d.profileUI.pendingDelete, d.profileUI.before = false, "", nil
				d.status = success
			}) {
				d.loading = false
				d.app.RequestUpdate()
				return
			}
			if err == nil {
				d.loadProfilePage("")
			}
			d.app.RequestUpdate()
		})
	}()
}

func (d *desktop) submitQuickProfile(kind quickInputKind, text string) {
	d.submitQuickProfileWithDraft(kind, text, d.quickInput, d.quickUI.draftRevision)
}

func (d *desktop) submitQuickProfileWithDraft(kind quickInputKind, text, input string, draftRevision uint64) {
	if d.client == nil || d.loading || d.askLoading {
		return
	}
	if !profile.ValidText(strings.TrimSpace(text)) {
		d.status = "Enter a preference or writing rule of up to 1,024 bytes. Your text is still here."
		d.app.RequestUpdate()
		return
	}
	if d.quickProfileSubmission == nil || d.quickProfileSubmission.Input != input || d.quickProfileSubmission.DraftRevision != draftRevision {
		id, err := newTaskRequestID()
		if err != nil {
			d.status = "The preference could not be prepared. Your text is still here."
			d.app.RequestUpdate()
			return
		}
		pkind := profile.Fact
		if kind == quickInputRule {
			pkind = profile.WritingRule
		}
		at := time.Now().UTC()
		d.quickProfileSubmission = &profileSubmission{Input: input, DraftRevision: draftRevision, Record: profile.Record{ID: id, Revision: 1, Kind: pkind, Text: strings.TrimSpace(text), Enabled: true, UserAuthored: true, CreatedAt: at, UpdatedAt: at}}
	}
	record := d.quickProfileSubmission.Record
	epoch, stable := d.client.BeginMemoryRead()
	if !stable {
		return
	}
	d.loading, d.status = true, "Remembering…"
	d.app.RequestUpdate()
	go func() {
		result, err := d.client.writeProfileAtEpoch(context.Background(), record, 0, epoch)
		if err == nil {
			epoch = result.Epoch
		}
		d.app.Post(func() {
			if !d.client.PublishMemoryRead(epoch, func() {
				d.loading = false
				if err != nil {
					d.status = profileErrorCopy(err)
					if d.quickUI.draftRevision != draftRevision {
						d.status = "Earlier preference: " + d.status
					}
					return
				}
				if d.quickUI.draftRevision == draftRevision && d.quickInput == input {
					d.quickInput = ""
				}
				d.quickProfileSubmission = nil
				d.status = "Remembered. You can review, pause, or edit it in Preferences."
				if d.quickUI.draftRevision != draftRevision {
					d.status = ""
				}
			}) {
				d.loading = false
			}
			d.resizeQuickPanel(false)
			d.app.RequestUpdate()
		})
	}()
}
