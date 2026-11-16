package client

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/knowledge/enrich"
)

// Control only the storage read boundary: a real directory query must stop its
// active read when cancelled, rather than waiting for the next page boundary.
type libraryReadData struct {
	*testData
	read func(context.Context, knowledge.PageRequest) (knowledge.Page, error)
}

func (d *libraryReadData) ListKnowledge(ctx context.Context, request knowledge.PageRequest) (knowledge.Page, error) {
	return d.read(ctx, request)
}

type libraryInferredReadData struct {
	*libraryReadData
	enrich.Repository
	readInferred func(context.Context, enrich.PageRequest) (knowledge.Page, error)
}

func (d *libraryInferredReadData) ListInferredKnowledge(ctx context.Context, request enrich.PageRequest) (knowledge.Page, error) {
	return d.readInferred(ctx, request)
}

func TestLibraryDirectoryCancelsActiveManualAndInferredReads(t *testing.T) {
	for _, branch := range []string{"manual only", "manual with inferred directory", "inferred fill", "inferred probe", "inferred continuation"} {
		t.Run(branch, func(t *testing.T) {
			entered := make(chan struct{}, 1)
			release := make(chan struct{})
			t.Cleanup(func() { close(release) })
			blocked := func(ctx context.Context) (knowledge.Page, error) {
				entered <- struct{}{}
				select {
				case <-ctx.Done():
					return knowledge.Page{}, ctx.Err()
				case <-release:
					return knowledge.Page{}, nil
				}
			}
			manual := &libraryReadData{testData: &testData{}, read: func(ctx context.Context, _ knowledge.PageRequest) (knowledge.Page, error) { return blocked(ctx) }}
			var data Data = manual
			if branch != "manual only" {
				inferred := &libraryInferredReadData{libraryReadData: manual}
				inferred.readInferred = func(ctx context.Context, _ enrich.PageRequest) (knowledge.Page, error) { return blocked(ctx) }
				if branch != "manual with inferred directory" {
					manual.read = func(context.Context, knowledge.PageRequest) (knowledge.Page, error) { return knowledge.Page{}, nil }
				}
				if branch == "inferred probe" {
					manual.read = func(context.Context, knowledge.PageRequest) (knowledge.Page, error) {
						return knowledge.Page{Entities: make([]knowledge.Entity, 50)}, nil
					}
				}
				if branch == "inferred continuation" {
					inferred.readInferred = func(ctx context.Context, request enrich.PageRequest) (knowledge.Page, error) {
						if request.After != "" {
							return blocked(ctx)
						}
						id := "auto-" + strings.Repeat("a", 64)
						return knowledge.Page{Entities: []knowledge.Entity{{ID: id, Name: "Topic", Kind: knowledge.Topic}}, HasMore: true, Next: id}, nil
					}
				}
				data = inferred
			}
			client := NewClient(data)
			defer client.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			finished := make(chan error, 1)
			go func() { _, err := client.libraryDirectoryPage(ctx, "", "", 0); finished <- err }()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("the directory query did not reach its active storage read")
			}
			cancel()
			select {
			case err := <-finished:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancelled active read returned %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("cancellation did not stop the active Library storage read")
			}
		})
	}
}

func TestLibraryDirectoryRejectsAFinalPageReturnedAfterCancellation(t *testing.T) {
	for _, inferred := range []bool{false, true} {
		t.Run(map[bool]string{false: "manual", true: "inferred"}[inferred], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			final := func() (knowledge.Page, error) {
				cancel()
				return knowledge.Page{Entities: []knowledge.Entity{{ID: "entry", Name: "Private result"}}}, nil
			}
			manual := &libraryReadData{testData: &testData{}, read: func(context.Context, knowledge.PageRequest) (knowledge.Page, error) { return final() }}
			var data Data = manual
			if inferred {
				manual.read = func(context.Context, knowledge.PageRequest) (knowledge.Page, error) { return knowledge.Page{}, nil }
				data = &libraryInferredReadData{libraryReadData: manual, readInferred: func(context.Context, enrich.PageRequest) (knowledge.Page, error) { return final() }}
			}
			client := NewClient(data)
			defer client.Close()
			page, err := client.libraryDirectoryPage(ctx, "", "", 0)
			if !errors.Is(err, context.Canceled) || len(page.Entities) != 0 {
				t.Fatalf("cancelled final read exposed results: rows=%d err=%v", len(page.Entities), err)
			}
		})
	}
}

func TestLibraryBrowserDeleteAllClearsSearchAndCancelsPriorReads(t *testing.T) {
	client := NewClient(&testData{}).WithDeletion(&testAllDeleter{})
	defer client.Close()
	app := &libraryRefreshApp{posted: make(chan func(), 16)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := &desktop{app: app, client: client, page: "Settings", pendingDeleteAll: true,
		libraryBrowser: libraryBrowserState{active: true, query: "private medical detail", appliedQuery: "private medical detail", kind: knowledge.Person, page: 4, total: 81, request: 41, refreshing: true, cancel: cancel}}
	client.SetMemoryInvalidator(func() {
		done := make(chan struct{})
		app.Post(func() { d.invalidateMemoryViews(); close(done) })
		<-done
	})
	d.deleteAllMemory()
	clearedBeforeInvalidation := d.libraryBrowser.query == "" && d.libraryBrowser.appliedQuery == "" && d.libraryBrowser.total == 0 && errors.Is(ctx.Err(), context.Canceled)
	publishLibraryCallback(t, app) // synchronous memory invalidation
	// A search entered while deletion is pending is cleared at completion too.
	d.libraryBrowser.query, d.libraryBrowser.appliedQuery = "new private query", "new private query"
	publishLibraryCallback(t, app) // completed deletion
	if !clearedBeforeInvalidation {
		t.Fatal("Delete All left the prior query visible until its mutation callback")
	}
	if d.libraryBrowser.query != "" || d.libraryBrowser.appliedQuery != "" || d.libraryBrowser.total != 0 || d.libraryBrowser.kind != "" || d.libraryBrowser.page != 0 {
		t.Fatalf("Delete All retained the prior Library search: %+v", d.libraryBrowser)
	}
	if !errors.Is(ctx.Err(), context.Canceled) || d.libraryBrowser.request <= 41 || d.libraryBrowser.cancel != nil || d.libraryBrowser.refreshing {
		t.Fatal("Delete All did not cancel and invalidate the prior directory read")
	}
}
