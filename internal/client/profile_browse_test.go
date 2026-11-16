package client

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/golang-gui/goui/ui"
	"github.com/hazcod/blur/internal/profile"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

func profileBrowseClient(t *testing.T) *Client {
	t.Helper()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient(store)
	t.Cleanup(func() { c.Close(); _ = store.Close() })
	return c
}

func finishProfileBrowse(t *testing.T, d *desktop, app *pausedAskApp) {
	t.Helper()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for d.loading || d.profileUI.loading || !d.profileUI.loaded && d.profileUI.loadError == "" {
		select {
		case publish := <-app.posted:
			publish()
		case <-timer.C:
			t.Fatal("saved-item operation did not complete")
		}
	}
}

func TestProfilePauseKeepsUnrelatedEditorDraft(t *testing.T) {
	c := profileBrowseClient(t)
	first, err := c.CreateProfile(context.Background(), profile.Fact, "I prefer Dutch.", profile.Scope{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.CreateProfile(context.Background(), profile.WritingRule, "Use short sentences.", profile.Scope{})
	if err != nil {
		t.Fatal(err)
	}
	app := &pausedAskApp{posted: make(chan func(), 8)}
	d := &desktop{app: app, client: c, page: "Preferences", profileUI: profileViewState{query: "Dutch", filterKind: profile.Fact, filterState: profile.Active, editing: true, editor: &first.Record, text: "An unfinished draft.", scope: profile.Scope{Project: "Atlas"}}}
	c.SetMemoryInvalidator(func() {
		done := make(chan struct{})
		app.Post(func() { d.invalidateMemoryViews(); close(done) })
		<-done
	})
	second.Record.Enabled = false
	d.updateProfileRecord(second.Record)
	finishProfileBrowse(t, d, app)
	// The real desktop also receives the queued mutation notification. It must
	// refresh the already-invalidated view without clearing the restored draft.
	done := make(chan struct{})
	d.watchCaptureChanges(done)
	defer close(done)
	select {
	case publish := <-app.posted:
		publish()
	case <-time.After(3 * time.Second):
		t.Fatal("queued memory-change notification was not delivered")
	}
	finishProfileBrowse(t, d, app)
	if !d.profileUI.editing || d.profileUI.editor == nil || d.profileUI.editor.ID != first.Record.ID || d.profileUI.text != "An unfinished draft." || d.profileUI.scope.Project != "Atlas" {
		t.Fatal("pausing another item discarded the editor draft", d.profileUI)
	}
	if d.profileUI.query != "Dutch" || d.profileUI.filterKind != profile.Fact || d.profileUI.filterState != profile.Active || d.profileUI.page.Total != 1 {
		t.Fatal("queued notification discarded the search/filter or returned the wrong total", d.profileUI)
	}
}

func TestDestructiveMemoryChangeStillClearsProfileSearchAndDraft(t *testing.T) {
	c := profileBrowseClient(t)
	app := &pausedAskApp{posted: make(chan func(), 8)}
	d := &desktop{app: app, client: c, profileUI: profileViewState{query: "Dutch", filterKind: profile.Fact, editing: true, text: "An unfinished draft.", scope: profile.Scope{Project: "Atlas"}}}
	c.SetMemoryInvalidator(func() {
		done := make(chan struct{})
		app.Post(func() { d.invalidateMemoryViews(); close(done) })
		<-done
	})
	mutating := make(chan struct{})
	go func() { c.NotifyMemoryChanging(); close(mutating) }()
	select {
	case publish := <-app.posted:
		publish()
	case <-time.After(3 * time.Second):
		t.Fatal("destructive invalidation did not reach the UI")
	}
	<-mutating
	if d.profileUI.query != "" || d.profileUI.filterKind != "" || d.profileUI.editing || d.profileUI.text != "" || d.profileUI.scope != (profile.Scope{}) || d.profileUI.loaded {
		t.Fatal("destructive invalidation retained old profile state", d.profileUI)
	}
	c.NotifyMemoryStable()
}

func TestProfileSearchResetsPagesAndPreservesTheEditorDraft(t *testing.T) {
	c := profileBrowseClient(t)
	first, err := c.CreateProfile(context.Background(), profile.Fact, "I prefer Dutch.", profile.Scope{Project: "Atlas"})
	if err != nil {
		t.Fatal(err)
	}
	app := &pausedAskApp{posted: make(chan func(), 8)}
	d := &desktop{app: app, client: c, profileUI: profileViewState{after: "40", before: []string{"", "20"}, editing: true, editor: &first.Record, text: "An unfinished draft.", scope: profile.Scope{Audience: "Executive team"}}}
	d.setProfileBrowse("ATLAS", profile.Fact, profile.Active)
	finishProfileBrowse(t, d, app)
	x := d.profileUI
	if !x.loaded || x.loading || x.after != "0" || len(x.before) != 0 || x.page.Total != 1 || len(x.page.Records) != 1 {
		t.Fatal("query retained the old page or missed a scoped preference", x)
	}
	if !x.editing || x.editor == nil || x.editor.ID != first.Record.ID || x.text != "An unfinished draft." || x.scope.Audience != "Executive team" {
		t.Fatal("query discarded the editor draft", x)
	}
}

func TestProfileSearchDiscardsAReplacedInFlightQuery(t *testing.T) {
	c := profileBrowseClient(t)
	if _, err := c.CreateProfile(context.Background(), profile.Fact, "I prefer Dutch.", profile.Scope{}); err != nil {
		t.Fatal(err)
	}
	rule, err := c.CreateProfile(context.Background(), profile.WritingRule, "Use a formal tone.", profile.Scope{})
	if err != nil {
		t.Fatal(err)
	}
	rule.Record.Enabled = false
	if _, err := c.UpdateProfile(context.Background(), rule.Record); err != nil {
		t.Fatal(err)
	}
	app := &pausedAskApp{posted: make(chan func(), 8)}
	d := &desktop{app: app, client: c}
	d.setProfileBrowse("Dutch", profile.Fact, profile.Active)
	d.setProfileBrowse("formal", profile.WritingRule, profile.Paused)
	finishProfileBrowse(t, d, app)
	if d.profileUI.loadError != "" || !d.profileUI.loaded || d.profileUI.page.Total != 1 || len(d.profileUI.page.Records) != 1 || d.profileUI.page.Records[0].ID != rule.Record.ID {
		t.Fatal("an obsolete query replaced the latest filtered results", d.profileUI)
	}
}

type unavailableProfileBrowse struct {
	*testData
	profile.Repository
}

func (*unavailableProfileBrowse) EncryptedAtRest() bool { return true }
func (*unavailableProfileBrowse) ListProfile(context.Context, profile.PageRequest) (profile.Page, error) {
	return profile.Page{}, errors.New("unavailable")
}

func TestProfileSearchFailureShowsAnErrorWithoutAnEmptyResult(t *testing.T) {
	c := NewClient(&unavailableProfileBrowse{testData: &testData{}})
	defer c.Close()
	app := &pausedAskApp{posted: make(chan func(), 8)}
	d := &desktop{app: app, client: c}
	d.setProfileBrowse("Dutch", "", "")
	finishProfileBrowse(t, d, app)
	if d.profileUI.loaded || d.profileUI.loading || d.profileUI.loadError == "" || len(d.profileUI.page.Records) != 0 {
		t.Fatal("failed search was presented as a completed empty result", d.profileUI)
	}
}

func TestProfileEditorDisablesItsInputsDuringAMutation(t *testing.T) {
	c := profileBrowseClient(t)
	app := &pausedAskApp{posted: make(chan func(), 8)}
	d := &desktop{app: app, client: c, profileUI: profileViewState{editing: true, kind: profile.Fact, text: "An unfinished draft.", scope: profile.Scope{Project: "Atlas"}}}
	// Enabled on the editor's container restricts every descendant control,
	// including multiline text, kind choices, and all four scope inputs.
	declaredFlags := func(view ui.View) uint64 {
		return reflect.ValueOf(view).Elem().FieldByName("ViewBase").FieldByName("viewBase").FieldByName("fields").FieldByName("Mask").Uint()
	}
	enabledFlag := declaredFlags(ui.VBox().Enabled(true))
	editorEnabled := func() bool {
		view := d.profileEditorView()
		if declaredFlags(view)&enabledFlag == 0 {
			return true // An omitted Enabled declaration leaves native inputs enabled.
		}
		return reflect.ValueOf(view).Elem().FieldByName("enabled").Bool()
	}
	if !editorEnabled() {
		t.Fatal("idle profile editor is disabled")
	}
	started, release := make(chan struct{}), make(chan struct{})
	releaseAction := sync.OnceFunc(func() { close(release) })
	defer releaseAction()
	d.runProfileAction(func(epoch uint64) (uint64, error) {
		close(started)
		<-release
		return epoch, nil
	}, "Preference updated.", false)
	<-started
	if editorEnabled() {
		t.Fatal("profile editor accepts input while its captured draft can be restored")
	}
	releaseAction()
	finishProfileBrowse(t, d, app)
	if !editorEnabled() || !d.profileUI.editing || d.profileUI.text != "An unfinished draft." || d.profileUI.scope.Project != "Atlas" {
		t.Fatal("completed action did not re-enable the unchanged editor draft", d.profileUI)
	}
}
