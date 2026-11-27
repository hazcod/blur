package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/hazcod/blur/internal/knowledge/enrich"
)

func TestSourceCompactionRuntimeWorksWithLibraryExtractionPaused(t *testing.T) {
	_, store, _ := todayTaskClient(t)
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	controller := &localAIController{path: filepath.Join(t.TempDir(), "local-ai.json"), settings: LocalAISettings{Enabled: true, Provider: ProviderOllama, Endpoint: server.URL, Model: "synthetic-model", BackgroundSummaries: true}}
	defer controller.Close()
	controller.SetLibraryRuntime(store, store, enrich.Options{})
	if controller.libraryService() == nil {
		t.Fatal("pausing Library extraction disabled source compaction")
	}
	if err := controller.SetBackgroundSummaries(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if controller.libraryService() != nil {
		t.Fatal("explicitly pausing both background functions retained a worker")
	}
	if err := controller.SetBackgroundSummaries(context.Background(), true); err != nil || controller.libraryService() == nil {
		t.Fatal("enabling summaries did not restart queued source processing", err)
	}
	if controller.Settings().AutomaticLibrary {
		t.Fatal("summary processing changed the user's separate Library consent")
	}
}

func TestSourceCompactionStatusReportsMissingSummaryOnlyWorker(t *testing.T) {
	for _, tc := range []struct {
		name     string
		settings LocalAISettings
		want     string
	}{
		{"summaries only", LocalAISettings{Enabled: true, BackgroundSummaries: true}, "unavailable"},
		{"Library only", LocalAISettings{Enabled: true, AutomaticLibrary: true}, "unavailable"},
		{"both paused", LocalAISettings{Enabled: true}, "disabled"},
		{"global pause", LocalAISettings{AutomaticLibrary: true, BackgroundSummaries: true}, "disabled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &Client{localAI: &localAIController{settings: tc.settings}}
			if got := c.LibraryStatus(); got.State != tc.want {
				t.Fatalf("missing source worker state=%q want %q", got.State, tc.want)
			}
		})
	}
}
