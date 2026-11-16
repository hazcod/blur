package client

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-gui/goui/ui"
	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

// Keep the UI queue under the test's control while using the real Client and
// encrypted directory reads. No native window or provider is started.
type libraryRefreshApp struct {
	ui.App
	posted  chan func()
	updates int
}

func (a *libraryRefreshApp) RequestUpdate() { a.updates++ }
func (a *libraryRefreshApp) Post(f func())  { a.posted <- f }

func publishLibraryCallback(t *testing.T, app *libraryRefreshApp) {
	t.Helper()
	select {
	case publish := <-app.posted:
		publish()
	case <-time.After(5 * time.Second):
		t.Fatal("Library callback did not arrive")
	}
}

func libraryRefreshFixture(t *testing.T) (*desktop, *durable.Store, *libraryRefreshApp) {
	t.Helper()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for i := range 101 {
		entity := knowledge.Entity{ID: fmt.Sprintf("manual-%03d", i), Kind: knowledge.Topic, Name: fmt.Sprintf("Topic %d", i), State: knowledge.Confirmed}
		if err := store.PutKnowledgeEntity(context.Background(), entity); err != nil {
			t.Fatal(err)
		}
	}
	client := NewClient(store)
	t.Cleanup(func() { client.Close() })
	first, err := client.Knowledge("")
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.Knowledge(first.Next)
	if err != nil {
		t.Fatal(err)
	}
	page, err := appendKnowledgePage(first, second, first.Next)
	if err != nil {
		t.Fatal(err)
	}
	app := &libraryRefreshApp{posted: make(chan func(), 16)}
	d := &desktop{app: app, client: client, page: "Library", library: page, libraryLoaded: true}
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	d.watchCaptureChanges(done)
	return d, store, app
}

func TestLibraryIndexProgressKeepsLoadedRowsAndPaginationWithoutRedrawing(t *testing.T) {
	d, _, app := libraryRefreshFixture(t)
	d.status = "A draft is ready."
	d.knowledgeName = "Draft topic"
	d.client.notifyMemoryIndexChanged()
	publishLibraryCallback(t, app) // index signal starts the background read
	if !d.libraryLoaded || len(d.library.Entities) != 100 || d.library.Next != "manual-099" || !d.library.HasMore {
		t.Fatalf("background index progress replaced the browsed directory: loaded=%t page=%+v", d.libraryLoaded, d.library)
	}
	if app.updates != 0 {
		t.Fatalf("unchanged index progress redrew the Library before its read: %d updates", app.updates)
	}
	publishLibraryCallback(t, app) // unchanged read completes
	if !d.libraryLoaded || d.libraryLoading || len(d.library.Entities) != 100 || d.library.Next != "manual-099" || !d.library.HasMore {
		t.Fatalf("background refresh lost the loaded continuation: %+v", d.library)
	}
	if app.updates != 0 || d.status != "A draft is ready." || d.knowledgeName != "Draft topic" {
		t.Fatalf("unchanged read disturbed browsing: updates=%d status=%q draft=%q", app.updates, d.status, d.knowledgeName)
	}
}

func TestLibraryBackgroundChangeRefreshesThroughTheExistingBrowseBoundary(t *testing.T) {
	d, store, app := libraryRefreshFixture(t)
	changed := knowledge.Entity{ID: "manual-000", Kind: knowledge.Topic, Name: "Updated topic", State: knowledge.Confirmed}
	if err := store.PutKnowledgeEntity(context.Background(), changed); err != nil {
		t.Fatal(err)
	}
	inserted := knowledge.Entity{ID: "manual-000a", Kind: knowledge.Topic, Name: "Newly enriched topic", State: knowledge.Confirmed}
	if err := store.PutKnowledgeEntity(context.Background(), inserted); err != nil {
		t.Fatal(err)
	}
	d.client.notifyMemoryIndexChanged()
	publishLibraryCallback(t, app)
	publishLibraryCallback(t, app)
	if len(d.library.Entities) != 101 || d.library.Entities[0].Name != "Updated topic" || d.library.Entities[1].ID != "manual-000a" || d.library.Entities[100].ID != "manual-099" || d.library.Next != "manual-099" || !d.library.HasMore {
		t.Fatalf("background change collapsed or advanced the browsed range: %+v", d.library)
	}
	if app.updates != 1 {
		t.Fatalf("changed directory should publish one update, got %d", app.updates)
	}
	// The original continuation must still load the remaining row exactly once.
	d.loadLibraryAfter(d.library.Next)
	publishLibraryCallback(t, app)
	if len(d.library.Entities) != 102 || d.library.HasMore || d.library.Entities[101].ID != "manual-100" {
		t.Fatalf("refreshed continuation no longer loads the remaining row: %+v", d.library)
	}
}

func TestLibraryIndexProgressDefersWhileInspectingAndEditing(t *testing.T) {
	d, _, app := libraryRefreshFixture(t)
	inspected := &knowledge.Entity{ID: "inspected"}
	source := &SearchHit{SourceID: "source"}
	d.libraryInspect, d.librarySource = inspected, source
	d.libraryEditFact, d.libraryEditText = "fact", "Unfinished correction"
	d.client.notifyMemoryIndexChanged()
	publishLibraryCallback(t, app)
	if d.libraryInspect != inspected || d.librarySource != source || d.libraryEditFact != "fact" || d.libraryEditText != "Unfinished correction" || !d.libraryRefreshPending || d.libraryLoading || app.updates != 0 {
		t.Fatalf("index progress disturbed an active inspector: pending=%t loading=%t updates=%d", d.libraryRefreshPending, d.libraryLoading, app.updates)
	}
}

func TestLibraryRefreshCompletingAfterInspectionStartsLeavesThePageUntouched(t *testing.T) {
	d, store, app := libraryRefreshFixture(t)
	if err := store.PutKnowledgeEntity(context.Background(), knowledge.Entity{ID: "manual-000", Kind: knowledge.Topic, Name: "Changed in background", State: knowledge.Confirmed}); err != nil {
		t.Fatal(err)
	}
	d.client.notifyMemoryIndexChanged()
	publishLibraryCallback(t, app)
	inspected := &knowledge.Entity{ID: "inspected"}
	d.libraryInspect = inspected
	d.libraryEditFact, d.libraryEditText = "fact", "My draft"
	publishLibraryCallback(t, app)
	if d.libraryInspect != inspected || d.libraryEditFact != "fact" || d.libraryEditText != "My draft" || d.library.Entities[0].Name != "Topic 0" || !d.libraryRefreshPending || d.libraryLoading || app.updates != 0 {
		t.Fatalf("in-flight refresh overwrote inspection state: pending=%t loading=%t updates=%d first=%q", d.libraryRefreshPending, d.libraryLoading, app.updates, d.library.Entities[0].Name)
	}
}

func TestLibraryInitialLoadUsesAnExplicitPage(t *testing.T) {
	d, _, app := libraryRefreshFixture(t)
	d.library, d.libraryLoaded = knowledge.Page{}, false
	d.loadLibrary()
	publishLibraryCallback(t, app)
	if len(d.library.Entities) != 20 || d.library.Entities[0].ID != "manual-000" || d.library.Entities[19].ID != "manual-019" || !d.library.HasMore {
		t.Fatalf("initial Library page has %d entries, want the first 20 with a continuation", len(d.library.Entities))
	}
}
