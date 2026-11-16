package client

import (
	"context"
	"strings"
	"testing"

	"github.com/golang-gui/goui/gui"
	"github.com/golang-gui/goui/ui"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/knowledge/enrich"
)

func mountedLibraryText(t *testing.T, view ui.View) string {
	t.Helper()
	var labels []string
	var walk func(gui.Widget)
	walk = func(widget gui.Widget) {
		if label, ok := widget.(*gui.Label); ok {
			labels = append(labels, label.Text())
		}
		for _, child := range widget.Children() {
			walk(child)
		}
	}
	walk(mountVoicePopupView(t, view))
	return strings.Join(labels, "\n")
}

func TestLibraryMountedSubjectsLeadWithLearnedContentAndPreserveTrust(t *testing.T) {
	c, _, _ := todayTaskClient(t)
	entity := knowledge.Entity{
		ID: enrich.EntityID(knowledge.Project, "Atlas", "com.example.editor", "code.example"), Name: "Atlas", Kind: knowledge.Project, State: knowledge.Proposed,
		Inference: &knowledge.Inference{AppID: "com.example.editor", Domain: "code.example"},
		Facts: []knowledge.Fact{
			{ID: "candidate", Text: "Atlas may ship a desktop client in December.", State: knowledge.Proposed, Sources: []knowledge.Source{{ID: strings.Repeat("a", 64)}}},
			{ID: "summary", Text: "Atlas is our desktop project. Mira owns the rollout and the team uses Go.", State: knowledge.Confirmed, Sources: []knowledge.Source{{UserAuthored: true}}},
			{ID: "dismissed", Text: "Atlas uses an outdated launch date.", State: knowledge.Dismissed, Sources: []knowledge.Source{{ID: strings.Repeat("b", 64)}}},
		},
	}
	d := &desktop{client: c, libraryLoaded: true, library: knowledge.Page{Entities: []knowledge.Entity{entity}}}
	text := mountedLibraryText(t, d.libraryView())
	for _, required := range []string{"Atlas is our desktop project. Mira owns the rollout and the team uses Go.", "Atlas may ship a desktop client in December.", "Confirmed details", "Inferred details to review", "Your correction", "View source"} {
		if !strings.Contains(text, required) {
			t.Errorf("mounted Library omits %q:\n%s", required, text)
		}
	}
	if strings.Contains(text, "outdated launch date") {
		t.Errorf("dismissed content was presented as learned knowledge:\n%s", text)
	}
	confirmed := strings.Index(text, "Atlas is our desktop project")
	proposed := strings.Index(text, "Atlas may ship")
	if confirmed < 0 || proposed < confirmed || strings.Index(text, "Add to library") < confirmed || strings.Index(text, "Automatic Library") < confirmed {
		t.Errorf("subject content does not lead management controls or inferred suggestions:\n%s", text)
	}
	if entity.Facts[0].State != knowledge.Proposed {
		t.Fatal("rendering changed an inferred detail's trust state")
	}
}

func TestLibraryMountedNameOnlySubjectExplainsMissingKnowledge(t *testing.T) {
	c, _, _ := todayTaskClient(t)
	entity := knowledge.Entity{ID: "manual-atlas", Name: "Atlas", Kind: knowledge.Project, State: knowledge.Confirmed}
	d := &desktop{client: c, libraryLoaded: true, library: knowledge.Page{Entities: []knowledge.Entity{entity}}}
	text := mountedLibraryText(t, d.libraryView())
	if !strings.Contains(text, "No learned details about Atlas yet.") || !strings.Contains(text, "Only the subject name has been saved.") {
		t.Fatalf("name-only subject has no truthful knowledge state:\n%s", text)
	}
}

func TestLibraryMountedGroupedSubjectDeduplicatesSentencePunctuationOnly(t *testing.T) {
	c, _, _ := todayTaskClient(t)
	entities := []knowledge.Entity{
		{
			ID: "atlas-editor", Name: "Atlas", Kind: knowledge.Project, State: knowledge.Confirmed,
			Inference: &knowledge.Inference{AppID: "com.example.editor"},
			Facts: []knowledge.Fact{
				{ID: "owner-a", Text: "Mira owns the rollback.", State: knowledge.Confirmed, Sources: []knowledge.Source{{UserAuthored: true}}},
				{ID: "cpp", Text: "The client uses C++.", State: knowledge.Confirmed, Sources: []knowledge.Source{{UserAuthored: true}}},
				{ID: "csharp", Text: "The client uses C#.", State: knowledge.Confirmed, Sources: []knowledge.Source{{UserAuthored: true}}},
			},
			OpenLoops: []knowledge.OpenLoop{{ID: "review-a", Text: "Review the migration plan.", State: knowledge.Confirmed, Sources: []knowledge.Source{{UserAuthored: true}}}},
		},
		{
			ID: "atlas-web", Name: "atlas", Kind: knowledge.Topic, State: knowledge.Confirmed,
			Inference: &knowledge.Inference{Domain: "atlas.example"},
			Facts:     []knowledge.Fact{{ID: "owner-b", Text: "Mira owns the rollback", State: knowledge.Confirmed, Sources: []knowledge.Source{{UserAuthored: true}}}},
			OpenLoops: []knowledge.OpenLoop{{ID: "review-b", Text: "Review the migration plan", State: knowledge.Confirmed, Sources: []knowledge.Source{{UserAuthored: true}}}},
		},
	}
	d := &desktop{client: c}
	text := mountedLibraryText(t, d.librarySubjectGroupCard(entities))
	for _, duplicate := range []string{"Mira owns the rollback", "Review the migration plan"} {
		if count := strings.Count(text, duplicate); count != 1 {
			t.Errorf("repeated subject detail %q rendered %d times:\n%s", duplicate, count, text)
		}
	}
	combinedContexts := "Inferred from Editor · Inferred from saved sources / atlas.example"
	if !strings.Contains(text, combinedContexts) {
		t.Errorf("grouped subject hid a supporting context %q:\n%s", combinedContexts, text)
	}
	for _, distinct := range []string{"The client uses C++.", "The client uses C#."} {
		if !strings.Contains(text, distinct) {
			t.Errorf("meaningful symbol in detail %q was collapsed:\n%s", distinct, text)
		}
	}
}

func TestLibraryMountedSubjectDetailShowsFullAccountAndCapturedEvidence(t *testing.T) {
	c, store, at := todayTaskClient(t)
	source, err := capture.SanitizeObservation("com.example.editor", "code.example", "Mira owns Atlas. The launch checklist requires a rollback review.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(context.Background(), source, at); err != nil {
		t.Fatal(err)
	}
	entity := knowledge.Entity{ID: "manual-atlas", Name: "Atlas", Kind: knowledge.Project, State: knowledge.Confirmed, Facts: []knowledge.Fact{
		{ID: "summary", Text: "Mira owns Atlas.", State: knowledge.Confirmed, Sources: []knowledge.Source{{ID: source.ContentID()}}},
		{ID: "candidate", Text: "The launch needs a rollback review.", State: knowledge.Proposed, Sources: []knowledge.Source{{ID: source.ContentID()}}},
	}}
	app := &libraryRefreshApp{posted: make(chan func(), 2)}
	d := &desktop{client: c, app: app}
	d.openLibrarySubject(entity, source.ContentID())
	publishLibraryCallback(t, app)
	text := mountedLibraryText(t, d.libraryView())
	for _, required := range []string{"Mira owns Atlas.", "The launch needs a rollback review.", "Confirmed details", "Inferred details to review", "Captured source", source.Text(), "code.example"} {
		if !strings.Contains(text, required) {
			t.Errorf("subject detail omits %q:\n%s", required, text)
		}
	}
	if strings.Contains(text, "Dismiss memory") || strings.Contains(text, "Correct") {
		t.Errorf("manual subject was given inferred-memory mutation controls:\n%s", text)
	}
}
