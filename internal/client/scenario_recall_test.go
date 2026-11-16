package client

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/intent"
	"github.com/hazcod/blur/internal/localapi"
	"github.com/hazcod/blur/internal/tasks"
)

type scenarioMode string

const (
	modeNoModel scenarioMode = "noModel"
	modeFakeGen scenarioMode = "fakeGen"
)

// scenarioGenerator is a deterministic stand-in for a model: it answers with a
// short sentence citing the first two packed source IDs.
type scenarioGenerator struct{}

func (scenarioGenerator) Generate(_ context.Context, packed assistant.PackedContext, _ int) (assistant.ModelAnswer, error) {
	if len(packed.SourceIDs) == 0 {
		return assistant.ModelAnswer{InsufficientEvidence: true}, nil
	}
	// Whole-document map and final prompts require every supplied label to be
	// kept (see wholeDocumentIntegrationGenerator); every other prompt cites
	// only the first two packed sources.
	if strings.Contains(packed.System, "BLUR WHOLE-DOCUMENT") {
		return assistant.ModelAnswer{Text: "The document covers running local LLMs.", SourceIDs: slices.Clone(packed.SourceIDs)}, nil
	}
	return assistant.ModelAnswer{Text: "Your saved memory covers this; see the cited sources.", SourceIDs: slices.Clone(packed.SourceIDs[:min(2, len(packed.SourceIDs))])}, nil
}

type scenarioOutcome struct {
	cited        []string
	insufficient bool
	diagnosis    string
	err          error
	notes        []string // extra failure reasons from custom checks
}

type recallScenario struct {
	name, question string
	// historyFrom names an earlier scenario whose answer becomes conversation history.
	historyFrom string
	// mustCite entries are labels or "prefix*" globs. minCite is how many distinct
	// matching labels must be cited; zero means every listed entry.
	mustCite     []string
	minCite      int
	mustNotCite  []string // labels, "prefix*" globs, or "any"
	insufficient bool
	// knownGap marks a confirmed, currently failing case. gapModes limits it to
	// specific modes; empty means every mode.
	knownGap string
	// gapProblem must appear in the joined problems of a gapped scenario, so a
	// gap cannot hide a different failure. gapProblemByMode overrides it per mode.
	gapProblem       string
	gapProblemByMode map[scenarioMode]string
	gapModes         []scenarioMode
	// custom replaces the Ask pipeline (entry flows for reminders and tasks).
	custom func(context.Context, *scenarioCorpus) scenarioOutcome
}

func (s recallScenario) expectedGapProblem(mode scenarioMode) string {
	if p, ok := s.gapProblemByMode[mode]; ok {
		return p
	}
	return s.gapProblem
}

func globMatch(pattern, label string) bool {
	if prefix, ok := strings.CutSuffix(pattern, "*"); ok {
		return strings.HasPrefix(label, prefix)
	}
	return pattern == label
}

func (c *scenarioCorpus) expand(patterns []string) []string {
	var out []string
	for label := range c.ids {
		for _, pattern := range patterns {
			if globMatch(pattern, label) {
				out = append(out, label)
				break
			}
		}
	}
	slices.Sort(out)
	return out
}

func (c *scenarioCorpus) service(mode scenarioMode) *assistant.Service {
	if mode == modeFakeGen {
		return assistant.NewService(c.client, scenarioGenerator{}, integrationTokenCounter{}, assistant.ContextTokenLimit)
	}
	return assistant.NewService(c.client, nil, nil, assistant.ContextTokenLimit)
}

func (c *scenarioCorpus) citedLabels(result assistant.Result) []string {
	set := map[string]bool{}
	for _, source := range result.Sources {
		if result.Answer.InsufficientEvidence {
			break
		}
		for _, label := range c.evidenceLabels(source.SourceID, source.Text, source.Layer == assistant.LayerHour) {
			set[label] = true
		}
	}
	for _, id := range result.Answer.SourceIDs {
		resolved := false
		// Recap citations carry a synthetic ID; resolve through the evidence list.
		for _, source := range result.Sources {
			if source.SourceID == id && source.Layer == assistant.LayerHour {
				for _, label := range c.evidenceLabels(id, source.Text, true) {
					set[label] = true
				}
				resolved = true
			}
		}
		if !resolved {
			for _, label := range c.evidenceLabels(id, "", false) {
				set[label] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for label := range set {
		out = append(out, label)
	}
	slices.Sort(out)
	return out
}

func (c *scenarioCorpus) diagnosis(ctx context.Context, result assistant.Result) string {
	parts := []string{
		fmt.Sprintf("kind=%d", result.Plan.Kind), fmt.Sprintf("direct=%t", result.Direct), fmt.Sprintf("general=%t", result.General),
		fmt.Sprintf("insufficient=%t", result.Answer.InsufficientEvidence), fmt.Sprintf("sources=%d", len(result.Sources)),
		fmt.Sprintf("sent=%d", result.SentItems), fmt.Sprintf("omitted=%d", result.Omitted), fmt.Sprintf("truncated=%d", result.Truncated),
		fmt.Sprintf("coverageLimited=%t", result.CoverageLimited), fmt.Sprintf("indexingPending=%t", result.IndexingPending),
	}
	if result.GenerationIssue != "" {
		parts = append(parts, "generationIssue="+result.GenerationIssue)
	}
	if !result.Plan.Scope.IsZero() {
		parts = append(parts, fmt.Sprintf("scope=%s[%s..%s]", result.Plan.Scope.Label, result.Plan.Scope.Start.Format("01-02 15:04"), result.Plan.Scope.End.Format("01-02 15:04")))
	}
	if len(result.Plan.AppIDs) > 0 {
		parts = append(parts, "apps="+strings.Join(result.Plan.AppIDs, ","))
	}
	if len(result.Plan.Terms) > 0 {
		parts = append(parts, "terms="+strings.Join(result.Plan.Terms, ","))
	}
	if retrieval, err := c.client.RetrievePlan(ctx, result.Plan); err == nil {
		d := retrieval.Diagnosis
		parts = append(parts, fmt.Sprintf("retrieved=%d sourcesInScope=%d appKnown=%t recapSampled=%t semantic=%q capturePaused=%t", len(retrieval.Evidence), d.SourcesInScope, d.AppKnown, d.RecapSampled, d.SemanticIssue, d.CapturePaused))
	}
	return strings.Join(parts, " ")
}

func (c *scenarioCorpus) ask(ctx context.Context, mode scenarioMode, question string, history []assistant.Turn) (assistant.Result, error) {
	return c.service(mode).AskWithHistoryMode(ctx, question, history, "")
}

func (c *scenarioCorpus) turnFor(question string, result assistant.Result) assistant.Turn {
	return assistant.Turn{Question: question, Answer: result.Answer.Text, Plan: result.Plan, General: result.General, SourceIDs: slices.Clone(result.Answer.SourceIDs), SourceDigests: assistant.EvidenceDigests(result.Sources)}
}

// entryFlow runs text through the same entry router the quick panel uses.
func (c *scenarioCorpus) entryFlow(ctx context.Context, mode scenarioMode, input string) (EntryResolution, *assistant.Result, error) {
	resolved, err := c.client.ResolveEntry(ctx, input, EntryState{}, "Europe/Brussels")
	if err != nil {
		return resolved, nil, err
	}
	if resolved.Answer != nil {
		return resolved, resolved.Answer, nil
	}
	if resolved.Action.Kind == intent.Ask {
		result, err := c.service(mode).AskWithHistoryMode(ctx, resolved.Action.Text, nil, resolved.Action.Mode)
		return resolved, &result, err
	}
	return resolved, nil, nil
}

func (s recallScenario) evaluate(c *scenarioCorpus, out scenarioOutcome) []string {
	problems := append([]string(nil), out.notes...)
	if out.err != nil {
		return append(problems, "error: "+out.err.Error())
	}
	if out.insufficient != s.insufficient {
		problems = append(problems, fmt.Sprintf("insufficient=%t, want %t", out.insufficient, s.insufficient))
	}
	for _, pattern := range s.mustNotCite {
		if pattern == "any" {
			if len(out.cited) > 0 {
				problems = append(problems, "cited sources "+strings.Join(out.cited, ",")+" but nothing may be cited")
			}
			continue
		}
		for _, label := range out.cited {
			if globMatch(pattern, label) {
				problems = append(problems, "cited forbidden "+label)
			}
		}
	}
	if len(s.mustCite) > 0 {
		want := c.expand(s.mustCite)
		need := s.minCite
		if need == 0 {
			need = len(want)
		}
		got := 0
		for _, label := range want {
			if slices.Contains(out.cited, label) {
				got++
			}
		}
		if got < need {
			problems = append(problems, fmt.Sprintf("cited %d of required %d from %s", got, need, strings.Join(want, ",")))
		}
	}
	return problems
}

func recallScenarios() []recallScenario {
	return []recallScenario{
		{name: "today-recap", question: "what did I do today", mustCite: []string{"orion-article", "notion-pentest-plan", "voice-youssef"}, minCite: 2, mustNotCite: []string{"blur-self"}},
		{name: "just-now", question: "what was I just working on", mustCite: []string{"notion-pentest-plan"}, mustNotCite: []string{"blur-self", "okta-login"}},
		{name: "venice-key", question: "where is the API key for venice stored again?", mustCite: []string{"terminal-venice"}},
		{name: "samira-recap", question: "recap my convo with Samira", mustCite: []string{"slack-samira-*"}, minCite: 2, mustNotCite: []string{"slack-dima-*", "okta-login"}},
		{name: "dima-retest", question: "what did Dima say about the pentest retest?", mustCite: []string{"slack-dima-retest"}, mustNotCite: []string{"okta-login", "blur-self"}, knownGap: "exact retrieval requires every term to match (search.go:188-206 termScore), so \"pentest\" excludes the Dima Slack capture that only says \"retest\" and the Notion planning page that has all three terms is cited instead", gapProblem: "cited 0 of required 1 from slack-dima-retest"},
		{name: "orion-article", question: "summarize that article I read in Orion about local LLMs", mustCite: []string{"orion-article"}},
		{name: "yesterday-afternoon", question: "what did I do yesterday afternoon?", mustCite: []string{"teams-idelux", "preview-programme", "slack-dima-retest"}, minCite: 1, mustNotCite: []string{"finder-downloads", "okta-login"}},
		{name: "last-week", question: "what did I do last week?", mustCite: []string{"sublime-sync-1", "terminal-sync-2", "slack-samira-1"}, minCite: 1, knownGap: "the model-facing context lists the newest hour recaps first and the fake generator cites the first two, which are Sunday's teams-idelux/slack-dima-retest recaps, so none of the earlier-week captures are cited", gapProblem: "cited 0 of required 1 from slack-samira-1", gapModes: []scenarioMode{modeFakeGen}},
		{name: "youssef-promise", question: "what did Youssef promise?", mustCite: []string{"voice-youssef"}, knownGap: "all planned terms must match (search.go:188-206), and the voice transcript says \"will send the SOW\" without the word \"promise\", so nothing is retrieved and semantic coverage reports limited", gapProblem: "cited 0 of required 1 from voice-youssef"},
		{name: "buildwise-url", question: "what's the buildwise evidence packet url?", mustCite: []string{"note-buildwise"}},
		{name: "who-is-dima", question: "who is Dima?", mustCite: []string{"slack-dima-*"}, minCite: 1, mustNotCite: []string{"okta-login"}},
		{name: "unknown-person", question: "what did I discuss with Marco?", mustNotCite: []string{"any"}, insufficient: true},
		{name: "followup-when", question: "when did he say that?", historyFrom: "dima-retest", mustCite: []string{"slack-dima-retest"}, knownGap: "the no-model \"when\" answer cites only what the previous answer cited, and the dima-retest turn still cites notion-pentest-plan (exact retrieval needs every term, see the dima-retest gap), so the follow-up dates that Notion capture instead of the Slack message; it passes once dima-retest ranks slack-dima-retest first", gapModes: []scenarioMode{modeNoModel}, gapProblem: "cited 0 of required 1 from slack-dima-retest"},
		{name: "reminder-create", question: "remind me tomorrow at 9 to send the AG report", custom: reminderCreateScenario},
		{name: "task-create-list", question: "add a task: follow up with Chloe on the consultant reporting, then: what's on my todo list?", custom: taskCreateListScenario},
	}
}

func reminderCreateScenario(ctx context.Context, c *scenarioCorpus) scenarioOutcome {
	var out scenarioOutcome
	resolved, err := c.client.ResolveEntry(ctx, "remind me tomorrow at 9 to send the AG report", EntryState{}, "Europe/Brussels")
	if err != nil {
		out.err = err
		return out
	}
	out.diagnosis = fmt.Sprintf("action=%v missing=%q", resolved.Action.Kind, func() string {
		if resolved.Draft != nil {
			return resolved.Draft.Missing
		}
		return "nodraft"
	}())
	if resolved.Action.Kind != intent.Reminder || resolved.Draft == nil || resolved.Draft.Missing != "" || resolved.Draft.Reminder == nil {
		out.notes = append(out.notes, "entry did not produce a complete reminder draft")
		return out
	}
	if _, err := c.client.CreateTask(ctx, resolved.Draft.Title, resolved.Draft.Reminder); err != nil {
		out.err = err
		return out
	}
	want := time.Date(2026, 11, 17, 9, 0, 0, 0, c.loc)
	page, err := c.client.TaskPage(ctx, "", tasks.Open)
	if err != nil {
		out.err = err
		return out
	}
	found := false
	for _, item := range page.Items {
		if item.Reminder != nil && item.Reminder.DueAt.Equal(want) && strings.Contains(strings.ToLower(item.Title), "ag report") {
			found = true
		}
	}
	if !found {
		out.notes = append(out.notes, fmt.Sprintf("no open task with a reminder at %s in the task listing (draft due %s)", want.Format(time.RFC3339), resolved.Draft.Reminder.DueAt.In(c.loc).Format(time.RFC3339)))
	}
	return out
}

func taskCreateListScenario(ctx context.Context, c *scenarioCorpus) scenarioOutcome {
	var out scenarioOutcome
	resolved, err := c.client.ResolveEntry(ctx, "add a task: follow up with Chloe on the consultant reporting", EntryState{}, "Europe/Brussels")
	if err != nil {
		out.err = err
		return out
	}
	title := resolved.Action.Text
	if resolved.Action.Kind != intent.Task || strings.TrimSpace(title) == "" {
		out.notes = append(out.notes, fmt.Sprintf("entry produced action=%v text=%q instead of a task", resolved.Action.Kind, title))
		// Keep exercising the listing step with the explicit title.
		title = "follow up with Chloe on the consultant reporting"
	}
	if _, err := c.client.CreateTask(ctx, title, nil); err != nil {
		out.err = err
		return out
	}
	listed, err := c.client.ResolveEntry(ctx, "what's on my todo list?", EntryState{}, "Europe/Brussels")
	if err != nil {
		out.err = err
		return out
	}
	if listed.Answer == nil {
		out.notes = append(out.notes, fmt.Sprintf("action=%v: todo question was not answered locally from the task list", listed.Action.Kind))
		return out
	}
	out.diagnosis = "listing=" + strings.ReplaceAll(listed.Answer.Answer.Text, "\n", " | ")
	if !strings.Contains(strings.ToLower(listed.Answer.Answer.Text), "chloe") {
		out.notes = append(out.notes, "todo list answer does not mention the new task")
	}
	return out
}

func TestRecallScenario(t *testing.T) {
	corpus := newScenarioCorpus(t)
	ctx := context.Background()
	scenarios := recallScenarios()
	byName := map[string]recallScenario{}
	for _, s := range scenarios {
		byName[s.name] = s
	}
	for _, mode := range []scenarioMode{modeNoModel, modeFakeGen} {
		for _, s := range scenarios {
			t.Run(string(mode)+"/"+s.name, func(t *testing.T) {
				var out scenarioOutcome
				switch {
				case s.custom != nil:
					out = s.custom(ctx, corpus)
					out.insufficient = s.insufficient
				default:
					var history []assistant.Turn
					if s.historyFrom != "" {
						prior := byName[s.historyFrom]
						first, err := corpus.ask(ctx, mode, prior.question, nil)
						if err != nil {
							out.err = err
							break
						}
						history = append(history, corpus.turnFor(prior.question, first))
					}
					result, err := corpus.ask(ctx, mode, s.question, history)
					out.err = err
					if err == nil {
						out.cited = corpus.citedLabels(result)
						out.insufficient = result.Answer.InsufficientEvidence
						out.diagnosis = corpus.diagnosis(ctx, result) + " answer=" + fmt.Sprintf("%q", firstBytes(result.Answer.Text, 160))
					}
				}
				problems := s.evaluate(corpus, out)
				gap := s.knownGap != "" && (len(s.gapModes) == 0 || slices.Contains(s.gapModes, mode))
				verdict := "PASS"
				gapMatches := gap && strings.Contains(strings.Join(problems, "; "), s.expectedGapProblem(mode))
				switch {
				case len(problems) > 0 && gapMatches:
					verdict = "KNOWN-GAP"
				case len(problems) > 0:
					verdict = "FAIL"
				}
				t.Logf("SCENARIO %s/%s: %s — cited=%s diagnosis=%s", s.name, mode, verdict, strings.Join(out.cited, ","), out.diagnosis)
				if len(problems) > 0 {
					t.Logf("SCENARIO %s/%s: problems: %s", s.name, mode, strings.Join(problems, "; "))
				}
				switch {
				case len(problems) > 0 && gapMatches:
				case len(problems) > 0 && gap:
					t.Errorf("scenario %s/%s is a knownGap but broke differently: expected problem containing %q, observed %q", s.name, mode, s.expectedGapProblem(mode), strings.Join(problems, "; "))
				case len(problems) > 0:
					t.Errorf("scenario %s/%s failed: %s", s.name, mode, strings.Join(problems, "; "))
				case len(problems) == 0 && gap:
					t.Errorf("scenario %s/%s now passes, remove knownGap (%s)", s.name, mode, s.knownGap)
				}
			})
		}
	}
}

func firstBytes(text string, n int) string {
	text = strings.ReplaceAll(text, "\n", " | ")
	if len(text) > n {
		return text[:n] + "..."
	}
	return text
}

func TestRecallScenarioMCPReadTools(t *testing.T) {
	corpus := newScenarioCorpus(t)
	ctx := context.Background()
	call := func(method, params string) json.RawMessage {
		t.Helper()
		wire, err := corpus.client.HandleMCPRead(ctx, method, json.RawMessage(params))
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		return wire
	}
	t.Run("search_memory", func(t *testing.T) {
		var out mcpSearchResponse
		if err := json.Unmarshal(call(localapi.RPCSearchMemory, `{"query":"Dima retest","limit":8}`), &out); err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, hit := range out.Results {
			got = append(got, corpus.label(hit.SourceID))
		}
		t.Logf("MCP search_memory results=%v", got)
		if len(got) == 0 || got[0] != "slack-dima-retest" {
			t.Errorf("slack-dima-retest is not ranked first: %v", got)
		}
		if slices.Contains(got, "blur-self") {
			t.Errorf("Blur's own screen was returned: %v", got)
		}
	})
	t.Run("get_working_memory", func(t *testing.T) {
		var out mcpWorkingMemoryResponse
		if err := json.Unmarshal(call(localapi.RPCGetWorkingMemory, `{"hours":6,"limit":8}`), &out); err != nil {
			t.Fatal(err)
		}
		var got []string
		var newest string
		var newestAt time.Time
		for _, item := range out.Items {
			label := corpus.label(item.SourceID)
			got = append(got, label)
			if item.CapturedAt.After(newestAt) {
				newest, newestAt = label, item.CapturedAt
			}
		}
		t.Logf("MCP get_working_memory items=%v newest=%s", got, newest)
		if newest != "notion-pentest-plan" {
			t.Errorf("newest working-memory item = %q, want notion-pentest-plan", newest)
		}
		if slices.Contains(got, "blur-self") {
			t.Errorf("Blur's own screen appeared in working memory: %v", got)
		}
	})
	t.Run("get_recent_activity", func(t *testing.T) {
		var out mcpActivityResponse
		if err := json.Unmarshal(call(localapi.RPCGetRecentActivity, `{"days_back":1,"limit":8}`), &out); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, segment := range out.Segments {
			for _, id := range segment.SourceIDs {
				if corpus.label(id) == "notion-pentest-plan" {
					found = true
				}
			}
		}
		t.Logf("MCP get_recent_activity segments=%d truncated=%t", len(out.Segments), out.Truncated)
		if !found {
			t.Error("recent activity omitted notion-pentest-plan")
		}
	})
	t.Run("get_snapshots paging", func(t *testing.T) {
		id := corpus.ids["orion-article"]
		offset, total, pages := 0, 0, 0
		var last mcpSnapshotItem
		for ; pages < 64; pages++ {
			params, _ := json.Marshal(map[string]any{"source_ids": []string{id}, "offsets": map[string]int{id: offset}, "max_bytes_per_source": 4096})
			var out mcpSnapshotResponse
			if err := json.Unmarshal(call(localapi.RPCGetSnapshots, string(params)), &out); err != nil {
				t.Fatal(err)
			}
			if len(out.Snapshots) != 1 {
				t.Fatalf("page %d returned %d snapshots", pages, len(out.Snapshots))
			}
			last = out.Snapshots[0]
			if last.OffsetBytes != offset || last.NextOffsetBytes < offset || (!last.Complete && last.NextOffsetBytes <= offset) {
				t.Fatalf("page %d did not advance: offset=%d next=%d", pages, last.OffsetBytes, last.NextOffsetBytes)
			}
			total += last.NextOffsetBytes - offset
			offset = last.NextOffsetBytes
			if last.Complete {
				break
			}
		}
		t.Logf("MCP get_snapshots pages=%d bytes=%d complete=%t", pages+1, total, last.Complete)
		if !last.Complete {
			t.Fatal("snapshot paging never completed")
		}
		if total < 60_000 {
			t.Errorf("paged only %d bytes, want the whole 60 KB article", total)
		}
		if !strings.Contains(last.Content, "the last paragraph mentions the always-on companion") {
			t.Errorf("final page lacks the tail sentence: %q", firstBytes(last.Content, 200))
		}
	})
}
