package client

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-gui/goui/ui"
	"github.com/hazcod/blur/internal/model"
)

type automaticModelStatusApp struct {
	ui.App
	posted chan func()
}

func (a *automaticModelStatusApp) Post(fn func())                            { a.posted <- fn }
func (*automaticModelStatusApp) RequestUpdate()                              {}
func (*automaticModelStatusApp) TimeoutFunc(time.Duration, func()) *ui.Timer { return nil }

func TestAISettingsDiscoverChangedEndpointInBackgroundWithoutButton(t *testing.T) {
	entered, release := make(chan struct{}, 1), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			entered <- struct{}{}
			<-release
			_, _ = fmt.Fprintf(w, `{"models":[{"name":"answers","digest":"sha256:%s"}]}`, strings.Repeat("a", 64))
		case "/api/show":
			_, _ = fmt.Fprint(w, `{"capabilities":["completion"]}`)
		default:
			t.Errorf("unexpected discovery request: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	defer close(release)
	a := &automaticModelStatusApp{posted: make(chan func(), 8)}
	d := &desktop{app: a, dashboardOpen: true, powerStatus: "checked", appleChecking: true, modelProvider: ProviderOllama, modelEndpoint: server.URL}
	d.modelDiscovery.attempted = true // an earlier check does not cover a new address
	d.openSettings(settingsGroupAI)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("AI settings require a manual Detect button after the endpoint changes")
	}
	if !d.modelDiscovery.checking {
		t.Fatal("background check did not expose loading state")
	}
	release <- struct{}{}
	select {
	case fn := <-a.posted:
		fn()
	case <-time.After(time.Second):
		t.Fatal("background catalog did not publish on the UI loop")
	}
	if len(d.modelDiscovery.catalogs[server.URL]) != 1 || d.modelDiscovery.checking {
		t.Fatal("background discovery did not update choices", d.modelDiscovery.catalogs)
	}
	d.openSettings(settingsGroupAI)
	if d.modelDiscovery.checking {
		t.Fatal("opening unchanged settings repeated a fresh availability check")
	}
}

func TestAppleSelectionDoesNotProbeSeparateEmbeddingServer(t *testing.T) {
	a := &automaticModelStatusApp{posted: make(chan func(), 8)}
	d := &desktop{app: a, modelProvider: ProviderApple, appleChecking: true}
	d.refreshDetectedModels()
	if d.modelDiscovery.checking {
		t.Fatal("Apple selection launched a separate Ollama search-model discovery")
	}
}

func TestDetectedModelSelectionRespectsCapabilityAndEndpoint(t *testing.T) {
	d := &desktop{modelProvider: ProviderOllama, modelEndpoint: "http://127.0.0.1:11434", memoryEndpoint: "http://127.0.0.1:11434", modelName: "old-answer", memoryModel: "old-search"}
	d.modelDiscovery.catalogs = map[string][]model.InstalledModel{d.modelEndpoint: {{Name: "answers", Completion: true}, {Name: "vectors", Embedding: true}}}
	if !d.selectDetectedModel("answers", false) || d.modelName != "answers" {
		t.Fatal("installed answer not selectable")
	}
	if d.selectDetectedModel("vectors", false) || d.modelName != "answers" {
		t.Fatal("embedding-only model selected for answers")
	}
	if !d.selectDetectedModel("vectors", true) || d.memoryModel != "vectors" {
		t.Fatal("installed embedding not selectable")
	}
	if d.selectDetectedModel("answers", true) || d.memoryModel != "vectors" {
		t.Fatal("answer-only model selected for search")
	}
	d.memoryEndpoint = "http://127.0.0.1:11435"
	if d.selectDetectedModel("vectors", true) {
		t.Fatal("stale endpoint catalog selected")
	}
	d.modelProvider = ProviderOpenAICompatible
	if d.selectDetectedModel("answers", false) {
		t.Fatal("local catalog changed cloud draft")
	}
}
