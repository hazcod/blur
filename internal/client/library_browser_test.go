package client

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/knowledge/derive"
	"github.com/hazcod/blur/internal/knowledge/enrich"
)

func TestLibraryDirectorySearchFindsManualAndInferredEntriesBeyondTheLoadedPage(t *testing.T) {
	d, store, _ := libraryRefreshFixture(t)
	ctx := context.Background()
	manual := knowledge.Entity{ID: "manual-080", Kind: knowledge.Project, Name: "Herdr", Aliases: []string{"Workspace"}, State: knowledge.Confirmed, Facts: []knowledge.Fact{{ID: "fact", Text: "The deployment uses Kubernetes.", State: knowledge.Confirmed, Sources: []knowledge.Source{{UserAuthored: true}}}}}
	if err := store.PutKnowledgeEntity(ctx, manual); err != nil {
		t.Fatal(err)
	}
	text := "Ada maintains the Herdr API."
	source, err := capture.SanitizeObservation("editor.app", "code.example", text, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(ctx, source, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyEnrichment(ctx, enrich.Chunk{SourceID: source.ContentID(), ModelKey: enrich.ModelKey("fixture"), End: len(text), Next: len(text), Complete: true, Proposals: []derive.Proposal{{Kind: knowledge.Person, Name: "Ada", Fact: text, SourceID: source.ContentID()}}}); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		query string
		kind  knowledge.Kind
		want  string
	}{
		{query: "HERDR kubernetes", kind: knowledge.Project, want: "Herdr"},
		{query: "workspace", kind: knowledge.Project, want: "Herdr"},
		{query: "Herdr API", kind: knowledge.Person, want: "Ada"},
	} {
		page, err := d.client.libraryDirectoryPage(ctx, test.query, test.kind, 0)
		if err != nil || page.total != 1 || len(page.Entities) != 1 || page.Entities[0].Name != test.want || page.HasMore {
			t.Fatalf("global search %q kind=%q: total=%d entries=%+v err=%v", test.query, test.kind, page.total, page.Entities, err)
		}
	}
	page, err := d.client.libraryDirectoryPage(ctx, "Kubernetes", knowledge.Person, 0)
	if err != nil || page.total != 0 || len(page.Entities) != 0 {
		t.Fatalf("kind filter included a project in People: %+v %v", page, err)
	}
}

func TestLibraryDirectoryUsesStableNaturalNameOrderInsteadOfInternalIDs(t *testing.T) {
	d, store, _ := libraryRefreshFixture(t)
	ctx := context.Background()
	for _, entity := range []knowledge.Entity{
		{ID: "manual-z-sort", Kind: knowledge.Topic, Name: "Alpha sortcheck 2", State: knowledge.Confirmed},
		{ID: "manual-a-sort", Kind: knowledge.Topic, Name: "Alpha sortcheck 12", State: knowledge.Confirmed},
		{ID: "manual-m-sort", Kind: knowledge.Topic, Name: "Beta sortcheck", State: knowledge.Confirmed},
	} {
		if err := store.PutKnowledgeEntity(ctx, entity); err != nil {
			t.Fatal(err)
		}
	}
	page, err := d.client.libraryDirectoryPage(ctx, "sortcheck", knowledge.Topic, 0)
	if err != nil || page.total != 3 || len(page.Entities) != 3 {
		t.Fatalf("sorted Library results = %+v err=%v", page, err)
	}
	got := []string{page.Entities[0].Name, page.Entities[1].Name, page.Entities[2].Name}
	want := []string{"Alpha sortcheck 2", "Alpha sortcheck 12", "Beta sortcheck"}
	if !slices.Equal(got, want) {
		t.Fatalf("Library order = %v, want %v", got, want)
	}
}

func TestLibraryDirectoryPrioritizesSubjectMatchesOverFactMentions(t *testing.T) {
	d, store, _ := libraryRefreshFixture(t)
	ctx := context.Background()
	for _, entity := range []knowledge.Entity{
		{ID: "manual-fact-mention", Kind: knowledge.Person, Name: "Ada", State: knowledge.Confirmed, Facts: []knowledge.Fact{{ID: "fact-mention", Text: "Maintains Kubernetes deployments.", State: knowledge.Confirmed, Sources: []knowledge.Source{{UserAuthored: true}}}}},
		{ID: "manual-subject-exact", Kind: knowledge.Topic, Name: "Kubernetes", State: knowledge.Confirmed},
		{ID: "manual-subject-prefix", Kind: knowledge.Project, Name: "Kubernetes rollout", State: knowledge.Confirmed},
	} {
		if err := store.PutKnowledgeEntity(ctx, entity); err != nil {
			t.Fatal(err)
		}
	}
	page, err := d.client.libraryDirectoryPage(ctx, "Kubernetes", "", 0)
	if err != nil || page.total != 3 || len(page.Entities) != 3 {
		t.Fatalf("Library search omitted a subject or content match: %+v err=%v", page, err)
	}
	got := []string{page.Entities[0].Name, page.Entities[1].Name, page.Entities[2].Name}
	want := []string{"Kubernetes", "Kubernetes rollout", "Ada"}
	if !slices.Equal(got, want) {
		t.Fatalf("Library relevance order = %v, want %v", got, want)
	}
}

func TestLibraryDirectorySearchUsesTermBoundaries(t *testing.T) {
	email := knowledge.Entity{ID: "email-project", Kind: knowledge.Project, Name: "Email migration", State: knowledge.Confirmed}
	ai := knowledge.Entity{ID: "ai-project", Kind: knowledge.Project, Name: "AI assistant", State: knowledge.Confirmed}
	if matchesLibraryEntity(email, "AI", "") {
		t.Fatal("AI search matched the incidental letters in Email")
	}
	if !matchesLibraryEntity(ai, "AI", "") {
		t.Fatal("AI search missed a whole subject term")
	}
	for _, name := range []string{"C++", ".NET", "C#"} {
		entity := knowledge.Entity{ID: "technical-" + name, Kind: knowledge.Topic, Name: name, State: knowledge.Confirmed}
		if !matchesLibraryEntity(entity, name, "") {
			t.Fatalf("technical search missed exact symbol-bearing name %q", name)
		}
	}
}

func TestLibraryDirectoryGroupsSpacingVariantsAndFindsEitherName(t *testing.T) {
	d, store, _ := libraryRefreshFixture(t)
	ctx := context.Background()
	for _, entity := range []knowledge.Entity{
		{ID: "manual-cs-one", Kind: knowledge.Project, Name: "CraftedSignal", State: knowledge.Confirmed},
		{ID: "manual-cs-two", Kind: knowledge.Project, Name: "Crafted Signal", State: knowledge.Confirmed},
		{ID: "manual-cs-three", Kind: knowledge.Project, Name: "Crafted-Signal", State: knowledge.Confirmed},
		{ID: "manual-cs-four", Kind: knowledge.Project, Name: "Crafted_Signal", State: knowledge.Confirmed},
	} {
		if err := store.PutKnowledgeEntity(ctx, entity); err != nil {
			t.Fatal(err)
		}
	}
	all, err := d.client.libraryDirectoryPage(ctx, "", knowledge.Project, 0)
	if err != nil || all.total != 1 || len(all.groups) != 1 || len(all.groups[0]) != 4 {
		t.Fatalf("spacing variants appeared as separate Library subjects: total=%d groups=%+v err=%v", all.total, all.groups, err)
	}
	search, err := d.client.libraryDirectoryPage(ctx, "CraftedSignal", knowledge.Project, 0)
	if err != nil || search.total != 1 || len(search.groups) != 1 || len(search.groups[0]) != 4 {
		t.Fatalf("search by one spelling missed the other Library record: total=%d groups=%+v err=%v", search.total, search.groups, err)
	}
}

func TestLibraryDirectoryNameGroupingPreservesTechnicalSymbols(t *testing.T) {
	d, store, _ := libraryRefreshFixture(t)
	for _, entity := range []knowledge.Entity{
		{ID: "manual-language-c", Kind: knowledge.Workflow, Name: "C", State: knowledge.Confirmed},
		{ID: "manual-language-cpp", Kind: knowledge.Workflow, Name: "C++", State: knowledge.Confirmed},
		{ID: "manual-language-csharp", Kind: knowledge.Workflow, Name: "C#", State: knowledge.Confirmed},
	} {
		if err := store.PutKnowledgeEntity(context.Background(), entity); err != nil {
			t.Fatal(err)
		}
	}
	page, err := d.client.libraryDirectoryPage(context.Background(), "", knowledge.Workflow, 0)
	if err != nil || page.total != 3 || len(page.groups) != 3 {
		t.Fatalf("technical names with different symbols were merged: total=%d groups=%+v err=%v", page.total, page.groups, err)
	}
}

func TestLibraryDirectorySearchDoesNotMatchDismissedKnowledge(t *testing.T) {
	d, store, _ := libraryRefreshFixture(t)
	entity := knowledge.Entity{
		ID: "manual-dismissed", Kind: knowledge.Project, Name: "Atlas", State: knowledge.Confirmed,
		Facts:     []knowledge.Fact{{ID: "retired-fact", Text: "Atlas uses the retired launch date.", State: knowledge.Dismissed, Sources: []knowledge.Source{{UserAuthored: true}}}},
		OpenLoops: []knowledge.OpenLoop{{ID: "retired-loop", Text: "Ask about the old billing issue", State: knowledge.Dismissed, Sources: []knowledge.Source{{UserAuthored: true}}}},
	}
	if err := store.PutKnowledgeEntity(context.Background(), entity); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"retired launch date", "old billing issue"} {
		page, err := d.client.libraryDirectoryPage(context.Background(), query, knowledge.Project, 0)
		if err != nil || page.total != 0 || len(page.Entities) != 0 {
			t.Fatalf("dismissed memory %q remained searchable: %+v err=%v", query, page, err)
		}
	}
	page, err := d.client.libraryDirectoryPage(context.Background(), "Atlas", knowledge.Project, 0)
	if err != nil || page.total != 1 || len(page.Entities) != 1 || page.Entities[0].Name != "Atlas" {
		t.Fatalf("dismissed details hid the live subject: %+v err=%v", page, err)
	}
}

func TestLibraryDirectoryPagesCoverEveryMatchAndClampAfterRemoval(t *testing.T) {
	d, _, _ := libraryRefreshFixture(t)
	ctx := context.Background()
	seen := map[string]bool{}
	for number := range 6 {
		page, err := d.client.libraryDirectoryPage(ctx, "topic", knowledge.Topic, number)
		if err != nil || page.number != number || page.total != 101 || page.HasMore != (number < 5) {
			t.Fatalf("page %d = %+v err=%v", number, page, err)
		}
		for _, entity := range page.Entities {
			if seen[entity.ID] {
				t.Fatalf("entry %s appeared on multiple pages", entity.ID)
			}
			seen[entity.ID] = true
		}
	}
	if len(seen) != 101 {
		t.Fatalf("pagination reached %d entries, want all 101", len(seen))
	}
	page, err := d.client.libraryDirectoryPage(ctx, "topic", knowledge.Topic, 100)
	if err != nil || page.number != 5 || len(page.Entities) != 1 || page.Entities[0].ID != "manual-100" {
		t.Fatalf("page beyond the current directory did not clamp to its last row: %+v %v", page, err)
	}
}

func TestLibraryDirectorySearchRejectsInvalidRequests(t *testing.T) {
	d, _, _ := libraryRefreshFixture(t)
	for _, test := range []struct {
		query string
		kind  knowledge.Kind
		page  int
	}{
		{query: strings.Repeat("x", MaxQueryBytes+1)},
		{query: "\xff"},
		{kind: "unknown"},
		{page: -1},
	} {
		if _, err := d.client.libraryDirectoryPage(context.Background(), test.query, test.kind, test.page); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("invalid Library query=%q kind=%q page=%d: %v", test.query, test.kind, test.page, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.client.libraryDirectoryPage(ctx, "", "", 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled search = %v", err)
	}
}

func TestLibraryBrowserBackgroundRefreshKeepsTheCurrentQueryAndPage(t *testing.T) {
	d, store, app := libraryRefreshFixture(t)
	d.libraryBrowser.appliedQuery, d.libraryBrowser.query = "topic", "Unsubmitted draft"
	d.libraryBrowser.kind = knowledge.Topic
	d.loadLibraryBrowserPage(3, false)
	publishLibraryCallback(t, app)
	app.updates = 0
	d.client.notifyMemoryIndexChanged()
	publishLibraryCallback(t, app)
	publishLibraryCallback(t, app)
	if app.updates != 0 || d.libraryBrowser.page != 3 || d.libraryBrowser.query != "Unsubmitted draft" || d.libraryBrowser.appliedQuery != "topic" || d.libraryBrowser.kind != knowledge.Topic || d.library.Entities[0].ID != "manual-060" {
		t.Fatalf("unchanged progress disturbed the active query/page: %+v updates=%d", d.libraryBrowser, app.updates)
	}
	if err := store.PutKnowledgeEntity(context.Background(), knowledge.Entity{ID: "manual-060", Kind: knowledge.Topic, Name: "Topic changed in background", State: knowledge.Confirmed}); err != nil {
		t.Fatal(err)
	}
	d.client.notifyMemoryIndexChanged()
	publishLibraryCallback(t, app)
	publishLibraryCallback(t, app)
	containsRenamedEntity := false
	for _, entity := range d.library.Entities {
		containsRenamedEntity = containsRenamedEntity || entity.ID == "manual-060"
	}
	if app.updates != 1 || d.libraryBrowser.page != 3 || d.libraryBrowser.query != "Unsubmitted draft" || d.libraryBrowser.appliedQuery != "topic" || containsRenamedEntity {
		t.Fatalf("real changes failed to preserve the active query/page: %+v updates=%d", d.libraryBrowser, app.updates)
	}
}

func TestLibraryBrowserDeletionRefillsThePageAndUpdatesTheResultCount(t *testing.T) {
	d, _, app := libraryRefreshFixture(t)
	d.loadLibraryBrowserPage(5, false)
	publishLibraryCallback(t, app)
	d.pendingDeleteKnowledge = "manual-100"
	d.deleteKnowledge("manual-100")
	publishLibraryCallback(t, app)
	if !d.libraryLoading {
		t.Fatalf("deleting the last result left an empty page with a stale count: page=%d count=%d", d.libraryBrowser.page, d.libraryBrowser.total)
	}
	publishLibraryCallback(t, app)
	if d.libraryBrowser.page != 4 || d.libraryBrowser.total != 100 || len(d.library.Entities) != 20 || d.library.HasMore {
		t.Fatalf("deletion did not refill the last valid page: page=%d count=%d rows=%d", d.libraryBrowser.page, d.libraryBrowser.total, len(d.library.Entities))
	}
}

func TestLibraryBrowserLatestSearchWinsWhenEarlierReadsFinishLater(t *testing.T) {
	d, _, app := libraryRefreshFixture(t)
	d.libraryBrowser.appliedQuery = "Topic 1"
	d.loadLibraryBrowserPage(0, false)
	var older func()
	select {
	case older = <-app.posted:
	case <-time.After(5 * time.Second):
		t.Fatal("the earlier Library read did not finish")
	}
	d.libraryBrowser.appliedQuery = "Topic 80"
	d.loadLibraryBrowserPage(0, false)
	publishLibraryCallback(t, app)
	older() // Deliver the completed older read after the latest results.
	if d.libraryBrowser.appliedQuery != "Topic 80" || d.libraryBrowser.total != 1 || len(d.library.Entities) != 1 || d.library.Entities[0].ID != "manual-080" || d.libraryLoading {
		t.Fatalf("an older search replaced the latest results: query=%q count=%d rows=%+v", d.libraryBrowser.appliedQuery, d.libraryBrowser.total, d.library.Entities)
	}
}

func TestLibraryBrowserUpdatesOnlyWhenTheProcessingStatusCopyChanges(t *testing.T) {
	d, _, app := libraryRefreshFixture(t)
	d.client.localAI = &localAIController{}
	d.loadLibraryBrowserPage(0, false)
	publishLibraryCallback(t, app)
	app.updates = 0
	// Only the fixture's in-memory status changes; no settings file, model, or
	// provider operation is involved.
	d.client.localAI.mu.Lock()
	d.client.localAI.settings.AutomaticLibrary = true
	d.client.localAI.mu.Unlock()
	d.client.notifyMemoryIndexChanged()
	publishLibraryCallback(t, app)
	publishLibraryCallback(t, app)
	if app.updates != 1 {
		t.Fatalf("a changed processing status was not reflected: updates=%d", app.updates)
	}
	d.client.notifyMemoryIndexChanged()
	publishLibraryCallback(t, app)
	publishLibraryCallback(t, app)
	if app.updates != 1 {
		t.Fatalf("unchanged processing status redrew Library again: updates=%d", app.updates)
	}
}

func TestLibraryBrowserQueuedRefreshCannotRestoreAManuallyDeletedEntry(t *testing.T) {
	d, _, app := libraryRefreshFixture(t)
	d.loadLibraryBrowserPage(0, false)
	publishLibraryCallback(t, app)
	d.refreshLibrary()
	var beforeDeletion func()
	select {
	case beforeDeletion = <-app.posted:
	case <-time.After(5 * time.Second):
		t.Fatal("the pre-deletion Library read did not finish")
	}
	d.pendingDeleteKnowledge = "manual-000"
	d.deleteKnowledge("manual-000")
	publishLibraryCallback(t, app)
	beforeDeletion()
	for _, entity := range d.library.Entities {
		if entity.ID == "manual-000" {
			t.Fatal("a queued background read restored the manually deleted entry")
		}
	}
	publishLibraryCallback(t, app)
	if d.libraryBrowser.total != 100 || d.library.Entities[0].ID != "manual-001" {
		t.Fatalf("the directory did not settle after deletion: count=%d rows=%+v", d.libraryBrowser.total, d.library.Entities)
	}
}
