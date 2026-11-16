package enrich_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/chronicle"
	blurclient "github.com/hazcod/blur/internal/client"
	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/knowledge/derive"
	"github.com/hazcod/blur/internal/knowledge/enrich"
	"github.com/hazcod/blur/internal/model"
	"github.com/hazcod/blur/internal/power"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
)

// Report only closed outcomes for invented source transformations. A native
// refusal is an allowed provider outcome, never a reason to retry the prompt.
func TestAppleLibrarySyntheticTechnicalContent(t *testing.T) {
	if os.Getenv("BLUR_SYNTHETIC_APPLE_BACKGROUND") != "1" {
		t.Skip("explicit synthetic on-device model check only")
	}
	for _, fixture := range []struct{ name, text string }{
		{"source-code", "Project Atlas is a local document indexer. File parser.go: func DecodeRecord(raw []byte) (Record, error) { var r Record; err := json.Unmarshal(raw, &r); return r, err }. The DecodeRecord workflow parses a record and returns decoding errors."},
		{"build-log", "Project Borealis build review. 10:01 INFO build started; 10:02 ERROR compiler: expected string, received int; 10:03 INFO Theo corrected the configuration type; 10:04 INFO build passed. Borealis uses a typed configuration validation workflow."},
		{"security-review", "Project Harbor reviews expired credentials. An automated check rejects invalid signatures. The team uses local redaction to protect personal data. Mina owns the Harbor security review workflow."},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			generator, err := derive.NewAppleGenerator()
			if err != nil {
				t.Fatal(err)
			}
			defer generator.CloseIdleConnections()
			observation, err := capture.SanitizeObservation("editor.app", "synthetic.example", fixture.text, 4096)
			if err != nil {
				t.Fatal(err)
			}
			input := []derive.Evidence{{SourceID: observation.ContentID(), Text: observation.Text()}}
			output, err := generator.Generate(ctx, input)
			if errors.Is(err, model.ErrAppleRefused) {
				t.Log("closed provider outcome: model_refused; no retry")
				return
			}
			if err != nil {
				t.Fatalf("closed provider outcome: %T: %v", err, err)
			}
			if derive.ValidateSuggestions(input, output) != nil {
				t.Fatal("successful output violated strict source contract")
			}
			if len(output) == 0 {
				t.Fatal("technical source produced no usable Library fact")
			}
			var facts strings.Builder
			for _, proposal := range output {
				facts.WriteString(strings.ToLower(proposal.Fact))
				facts.WriteByte(' ')
			}
			summary := facts.String()
			switch fixture.name {
			case "source-code":
				if !strings.Contains(summary, "record") || !strings.Contains(summary, "json") || strings.Contains(summary, "func decoderrecord") || strings.Contains(summary, "[]byte") {
					t.Fatalf("source-code capture was not reduced to a useful high-level fact: %q", summary)
				}
			case "build-log":
				if !strings.Contains(summary, "configuration") {
					t.Fatalf("build-log capture lost its technical meaning: %q", summary)
				}
			case "security-review":
				if !strings.Contains(summary, "credential") || !strings.Contains(summary, "redaction") {
					t.Fatalf("security-review capture lost its technical meaning: %q", summary)
				}
			}
			if fixture.name == "security-review" {
				for _, proposal := range output {
					if strings.HasPrefix(proposal.Fact, knowledge.PossibleOpenItemPrefix) {
						t.Fatalf("completed security workflow was incorrectly marked as open work: %q", proposal.Fact)
					}
				}
			}
			t.Logf("closed provider outcome: validated; proposals=%d", len(output))
		})
	}
}

func TestAppleLibrarySyntheticOpenWorkProposal(t *testing.T) {
	if os.Getenv("BLUR_SYNTHETIC_APPLE_BACKGROUND") != "1" {
		t.Skip("explicit synthetic on-device model check only")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	generator, err := derive.NewAppleGenerator()
	if err != nil {
		t.Fatal(err)
	}
	defer generator.CloseIdleConnections()
	observation, err := capture.SanitizeObservation("editor.app", "synthetic.example", "Project CraftedSignal release readiness. We are still waiting for vendor verification before launch; the vendor has not replied yet. Mira will finish the release checklist after the verification arrives.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	input := []derive.Evidence{{SourceID: observation.ContentID(), Text: observation.Text()}}
	proposals, err := generator.Generate(ctx, input)
	if errors.Is(err, model.ErrAppleRefused) {
		t.Skip("Apple model declined this synthetic source")
	}
	if err != nil {
		t.Fatalf("Apple open-work derivation: %T: %v", err, err)
	}
	if derive.ValidateSuggestions(input, proposals) != nil {
		t.Fatal("Apple open-work proposal failed source validation")
	}
	for _, proposal := range proposals {
		t.Logf("synthetic proposal: kind=%s name=%q fact=%q", proposal.Kind, proposal.Name, proposal.Fact)
		if proposal.Kind == knowledge.Project && strings.Contains(strings.ToLower(proposal.Name), "craftedsignal") && strings.HasPrefix(proposal.Fact, knowledge.PossibleOpenItemPrefix) {
			return
		}
	}
	t.Fatal("Apple did not return a tentative, project-scoped open-work proposal")
}

func TestAppleLibraryOpenWorkFlowsIntoAskWithItsSource(t *testing.T) {
	if os.Getenv("BLUR_SYNTHETIC_APPLE_BACKGROUND") != "1" {
		t.Skip("explicit synthetic on-device model check only")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	when := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	observation, err := capture.SanitizeObservation("com.apple.mail", "", "Subject: CraftedSignal release readiness\nWe are still waiting for vendor verification before launch; the vendor has not replied yet. Mira will finish the release checklist after verification arrives.", 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationAt(ctx, observation, when); err != nil {
		t.Fatal(err)
	}
	generator, err := derive.NewAppleGenerator()
	if err != nil {
		t.Fatal(err)
	}
	worker, err := enrich.NewService(store, store, generator, enrich.Options{Policy: policy{mode: power.Balanced}})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	if err := worker.Backfill(ctx); err != nil {
		t.Fatalf("synthetic Library worker: %v; closed status=%s", err, worker.Status().ErrorCode)
	}
	page, err := store.ListInferredKnowledge(ctx, enrich.PageRequest{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	foundProposedOpenWork := false
	for _, entity := range page.Entities {
		for _, fact := range entity.Facts {
			if fact.State != "proposed" || !strings.HasPrefix(fact.Text, knowledge.PossibleOpenItemPrefix) {
				continue
			}
			for _, source := range fact.Sources {
				foundProposedOpenWork = foundProposedOpenWork || source.ID == observation.ContentID()
			}
		}
	}
	if !foundProposedOpenWork {
		t.Fatalf("Library did not persist a source-linked tentative follow-up: %+v; status=%+v", page.Entities, worker.Status())
	}
	client := blurclient.NewClient(store)
	defer client.Close()
	question := "What's outstanding for CraftedSignal?"
	plan, err := assistant.PlanQuestion(question, when, nil)
	if err != nil {
		t.Fatal(err)
	}
	retrieval, err := client.RetrievePlan(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range retrieval.Evidence {
		if item.SourceID == observation.ContentID() && strings.Contains(item.Text, knowledge.PossibleOpenItemPrefix) && strings.Contains(strings.ToLower(item.Text), "vendor verification") {
			return
		}
	}
	t.Fatalf("Ask did not retrieve the tentative fact with its original source: %+v", retrieval.Evidence)
}

// This opt-in smoke test uses only invented text, a temporary encrypted store
// and the helper beside the compiled test binary. Ordinary test runs never
// invoke an installed model. It does not open an app, Keychain or audio device.
func TestAppleBackgroundSyntheticIntegration(t *testing.T) {
	if os.Getenv("BLUR_SYNTHETIC_APPLE_BACKGROUND") != "1" {
		t.Skip("explicit synthetic on-device model check only")
	}
	for _, group := range []string{"library", "recaps"} {
		t.Run(group, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = store.Close() }()
			start := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
			observation, err := capture.SanitizeObservation("editor.app", "synthetic.example", "Project Atlas is a local writing tool. Mina is the Atlas project lead. The Atlas release review covered the offline export workflow.", 4096)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.PutObservationAt(ctx, observation, start.Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
			if group == "library" {
				generator, err := derive.NewAppleGenerator()
				if err != nil {
					t.Fatal(err)
				}
				worker, err := enrich.NewService(store, store, generator, enrich.Options{Policy: policy{mode: power.Balanced}})
				if err != nil {
					t.Fatal(err)
				}
				defer worker.Close()
				if err := worker.Backfill(ctx); err != nil {
					t.Fatalf("synthetic Library worker: %v; closed status=%s", err, worker.Status().ErrorCode)
				}
				if status := worker.Status(); status.ProcessedChunks != 1 || !status.Complete {
					t.Fatal("synthetic Library work did not persist completion")
				}
				page, err := store.ListInferredKnowledge(ctx, enrich.PageRequest{Limit: 20})
				if err != nil {
					t.Fatalf("read synthetic Library: %v", err)
				}
				for _, entity := range page.Entities {
					var facts []string
					for _, fact := range entity.Facts {
						facts = append(facts, fact.Text)
					}
					t.Logf("Apple Library learned entity=%q kind=%q facts=%q", entity.Name, entity.Kind, facts)
				}
				learnedProject, learnedOwner, learnedWorkflow := false, false, false
				for _, entity := range page.Entities {
					for _, fact := range entity.Facts {
						if fact.State == "proposed" || fact.State == "confirmed" || fact.State == "deterministic" {
							text := strings.ToLower(strings.TrimSpace(fact.Text))
							linked := false
							for _, source := range fact.Sources {
								linked = linked || source.ID == observation.ContentID()
							}
							if linked {
								learnedProject = learnedProject || strings.Contains(text, "atlas") && strings.Contains(text, "writing tool")
								learnedOwner = learnedOwner || strings.Contains(text, "mina") && (strings.Contains(text, "lead") || strings.Contains(text, "leader"))
								learnedWorkflow = learnedWorkflow || strings.Contains(text, "offline export")
							}
						}
					}
				}
				if !learnedProject || !learnedOwner || !learnedWorkflow {
					t.Fatalf("Apple Library did not retain all source-linked project, owner, and workflow details: project=%t owner=%t workflow=%t entities=%+v", learnedProject, learnedOwner, learnedWorkflow, page.Entities)
				}
				return
			}
			generator, err := chronicle.NewAppleGenerator()
			if err != nil {
				t.Fatal(err)
			}
			worker := chronicle.NewWorker(store, policy{mode: power.Balanced}, generator, chronicle.WorkerConfig{Enabled: true, Provider: power.LocalProvider})
			defer worker.Close()
			hour := chronicle.Hour{Start: start, End: start.Add(time.Hour), Evidence: []chronicle.Evidence{{Observation: observation, At: start.Add(time.Minute), Salience: 10}}}
			if err := worker.Process(ctx, hour); err != nil {
				t.Fatalf("synthetic recap worker: %v; closed status=%s", err, worker.Status().LastErrorCode)
			}
			id, err := hour.ID()
			if err != nil {
				t.Fatal(err)
			}
			if _, found, err := store.GetChronicle(ctx, id); err != nil || !found {
				t.Fatal("synthetic recap did not persist", err)
			}
		})
	}
}

func TestAppleBackgroundSyntheticDenseLibrary(t *testing.T) {
	if os.Getenv("BLUR_SYNTHETIC_APPLE_BACKGROUND") != "1" {
		t.Skip("explicit synthetic on-device model check only")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	var text strings.Builder
	for i := 1; i <= 18; i++ {
		fmt.Fprintf(&text, "Project Atlas-%d is an offline desktop writing tool with encrypted local storage and accessible keyboard navigation. The Atlas-%d project lead is Mina-%d. The project workflow requires an editor review before export.\n", i, i, i)
	}
	source(t, store, text.String())
	generator, err := derive.NewAppleGenerator()
	if err != nil {
		t.Fatal(err)
	}
	worker, err := enrich.NewService(store, store, generator, enrich.Options{Policy: policy{mode: power.Balanced}})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	for attempt := 0; attempt < 8; attempt++ {
		if err := worker.Backfill(ctx); err == nil {
			return
		} else if !errors.Is(err, enrich.ErrMoreWork) {
			t.Fatalf("dense synthetic Library worker: %v; closed status=%s", err, worker.Status().ErrorCode)
		}
	}
	t.Fatal("dense synthetic Library exceeded bounded completion quanta")
}

func TestAppleBackgroundSyntheticMultipleSources(t *testing.T) {
	if os.Getenv("BLUR_SYNTHETIC_APPLE_BACKGROUND") != "1" {
		t.Skip("explicit synthetic on-device model check only")
	}
	for _, group := range []string{"library", "recaps"} {
		t.Run(group, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			store, err := durable.OpenDurable(filepath.Join(t.TempDir(), "memory.db"), securestore.OfflineFixtureKey(), durable.CreateNew)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = store.Close() }()
			start := time.Date(2026, 11, 13, 11, 0, 0, 0, time.UTC)
			var observations []capture.SanitizedObservation
			for _, text := range []string{
				"Project Atlas is a local writing tool. Mina leads Project Atlas. Atlas release review covered offline export.",
				"Project Borealis is an encrypted calendar application. Theo leads Borealis. The review covered timezone support.",
				"Project Cedar is a desktop accessibility tool. Alice leads Cedar. Cedar review covered keyboard navigation.",
			} {
				observation, err := capture.SanitizeObservation("editor.app", "synthetic.example", text, 4096)
				if err != nil {
					t.Fatal(err)
				}
				observations = append(observations, observation)
			}
			// Time order and hash order are independent in a real captured hour.
			sort.Slice(observations, func(i, j int) bool { return observations[i].ContentID() > observations[j].ContentID() })
			hour := chronicle.Hour{Start: start, End: start.Add(time.Hour)}
			for i, observation := range observations {
				at := start.Add(time.Duration(i+1) * time.Minute)
				if err := store.PutObservationAt(ctx, observation, at); err != nil {
					t.Fatal(err)
				}
				hour.Evidence = append(hour.Evidence, chronicle.Evidence{Observation: observation, At: at, Salience: 10})
			}
			if group == "library" {
				generator, err := derive.NewAppleGenerator()
				if err != nil {
					t.Fatal(err)
				}
				worker, err := enrich.NewService(store, store, generator, enrich.Options{Policy: policy{mode: power.Balanced}})
				if err != nil {
					t.Fatal(err)
				}
				defer worker.Close()
				if err := worker.Backfill(ctx); err != nil {
					t.Fatalf("multi-source synthetic Library worker: %v; closed status=%s", err, worker.Status().ErrorCode)
				}
				return
			}
			generator, err := chronicle.NewAppleGenerator()
			if err != nil {
				t.Fatal(err)
			}
			worker := chronicle.NewWorker(store, policy{mode: power.Balanced}, generator, chronicle.WorkerConfig{Enabled: true, Provider: power.LocalProvider})
			defer worker.Close()
			if err := worker.Process(ctx, hour); err != nil {
				t.Fatalf("multi-source synthetic recap worker: %v; closed status=%s", err, worker.Status().LastErrorCode)
			}
		})
	}
}
