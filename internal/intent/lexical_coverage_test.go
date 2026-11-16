package intent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
)

// These cases distinguish extraction from authority validation. A catalog-only
// case is a proposed model evaluation item, never evidence of model accuracy.
type lexicalCoverageCase struct {
	ID       string `json:"id"`
	Family   string `json:"family"`
	Input    string `json:"input"`
	Layer    string `json:"layer"`
	Expected struct {
		Action  Action   `json:"action"`
		Outcome string   `json:"outcome"`
		Missing []string `json:"missing,omitempty"`
	} `json:"expected"`
	Context struct {
		Targets []Target `json:"targets,omitempty"`
		Pending *Pending `json:"pending,omitempty"`
	} `json:"context,omitempty"`
	Checks struct {
		Read *struct {
			Match     bool   `json:"match"`
			Generic   bool   `json:"generic,omitempty"`
			State     string `json:"state,omitempty"`
			Window    string `json:"window,omitempty"`
			Reminders bool   `json:"reminders,omitempty"`
			Count     bool   `json:"count,omitempty"`
		} `json:"read,omitempty"`
		Local *struct {
			Match bool `json:"match"`
		} `json:"local,omitempty"`
		Decode *struct {
			Proposal Action `json:"proposal"`
			Accept   bool   `json:"accept"`
		} `json:"decode,omitempty"`
	} `json:"checks,omitempty"`
	Notes string `json:"notes,omitempty"`
}

func readLexicalCoverage(t *testing.T) []lexicalCoverageCase {
	t.Helper()
	f, err := os.Open("testdata/lexical_coverage.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var cases []lexicalCoverageCase
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), 128<<10)
	for line := 1; scanner.Scan(); line++ {
		var c lexicalCoverageCase
		decoder := json.NewDecoder(strings.NewReader(scanner.Text()))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&c); err != nil {
			t.Fatalf("line %d: %v", line, err)
		}
		cases = append(cases, c)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return cases
}

// Removing a category, duplicating inputs to inflate the count, or silently
// turning model-dependent cases into passing parser cases breaks this contract.
func TestLexicalCoverageCatalog(t *testing.T) {
	cases := readLexicalCoverage(t)
	if len(cases) < 5000 {
		t.Fatalf("need at least 5000 labeled utterances, got %d", len(cases))
	}
	ids, inputs, families, layers, kinds := map[string]bool{}, map[string]string{}, map[string]int{}, map[string]int{}, map[Kind]int{}
	for _, c := range cases {
		if c.ID == "" || ids[c.ID] || c.Family == "" || strings.TrimSpace(c.Input) == "" {
			t.Fatalf("missing/duplicate identity or empty input: %+v", c)
		}
		ids[c.ID] = true
		key := strings.ToLower(strings.Join(strings.Fields(c.Input), " "))
		if earlier, ok := inputs[key]; ok {
			t.Fatalf("%s duplicates %s after case/space normalization", c.ID, earlier)
		}
		inputs[key] = c.ID
		families[c.Family]++
		layers[c.Layer]++
		kinds[c.Expected.Action.Kind]++
		switch c.Expected.Action.Kind {
		case Ask, Note, Task, Reminder, Fact, Rule, TaskEdit, TaskComplete, TaskCancel, TaskInspect, Clarify:
		default:
			t.Fatalf("%s: unknown expected intent kind %q", c.ID, c.Expected.Action.Kind)
		}
		if len(c.Input) > MaxInputBytes || strings.ContainsRune(c.Input, 0) {
			t.Fatalf("%s: catalog input exceeds the entry contract", c.ID)
		}
		switch c.Expected.Outcome {
		case "ready", "draft", "read", "clarify", "preserve", "not_supported":
		default:
			t.Fatalf("%s: unknown expected outcome %q", c.ID, c.Expected.Outcome)
		}
		switch c.Layer {
		case "local_read":
			if c.Checks.Read == nil || c.Checks.Local != nil || c.Checks.Decode != nil {
				t.Fatalf("%s: read case has incorrect checks", c.ID)
			}
		case "local_parser":
			if c.Checks.Local == nil {
				t.Fatalf("%s: local case has no parser assertion", c.ID)
			}
		case "validation":
			if c.Checks.Decode == nil || c.Checks.Local != nil {
				t.Fatalf("%s: validation case has incorrect checks", c.ID)
			}
		case "model_required":
			if c.Checks.Local != nil || c.Checks.Decode != nil || c.Notes == "" {
				t.Fatalf("%s: model catalog case must declare its limitation and have no synthetic inference assertion", c.ID)
			}
		default:
			t.Fatalf("%s: unknown evaluation layer %q", c.ID, c.Layer)
		}
		if len(c.Context.Targets) > 8 {
			t.Fatalf("%s: target context exceeds request bound", c.ID)
		}
		for i, target := range c.Context.Targets {
			if target.Ref != fmt.Sprintf("T%d", i+1) || target.Title == "" {
				t.Fatalf("%s: invalid bounded target", c.ID)
			}
		}
		if c.Expected.Outcome == "draft" && len(c.Expected.Missing) == 0 {
			t.Fatalf("%s: draft must explain the missing scheduling field", c.ID)
		}
	}
	for _, layer := range []string{"local_parser", "validation", "model_required"} {
		if layers[layer] == 0 {
			t.Fatalf("missing evaluation layer %s", layer)
		}
	}
	for _, kind := range []Kind{Ask, Note, Task, Reminder, Fact, Rule, TaskEdit, TaskComplete, TaskCancel, TaskInspect, Clarify} {
		if kinds[kind] == 0 {
			t.Fatalf("missing intent kind %s", kind)
		}
	}
	if len(families) < 40 {
		t.Fatalf("need distinct language/safety cohorts, got %d", len(families))
	}
	t.Logf("%d distinct utterances; layers=%v; families=%d; labels=%v (model_required is catalog coverage only)", len(cases), layers, len(families), kinds)
}

func TestLexicalCoverageTaskReads(t *testing.T) {
	count := 0
	var failures lexicalCoverageFailures
	for _, c := range readLexicalCoverage(t) {
		if c.Checks.Read == nil {
			continue
		}
		count++
		want := c.Checks.Read
		got, ok := TaskListRequest(c.Input)
		if ok != want.Match || ok && (got.Generic != want.Generic || want.State != "" && (got.State != want.State || got.Window != want.Window || got.RemindersOnly != want.Reminders || got.Count != want.Count)) {
			failures.add(c, fmt.Sprintf("got %+v match=%t, want %+v", got, ok, want))
		}
		if ok {
			for _, kind := range []Kind{Note, Task, Reminder} {
				content, err := json.Marshal(Action{Kind: kind, Text: c.Input})
				if err != nil {
					t.Fatal(err)
				}
				if action, err := Decode(string(content), Request{Input: c.Input}); err == nil {
					failures.add(c, fmt.Sprintf("read granted model mutation authority: %+v", action))
				}
			}
		}
	}
	if count < 5000 {
		t.Fatalf("expected thousands of read and boundary scenarios, got %d", count)
	}
	failures.report(t)
}

type lexicalCoverageFailures struct {
	counts  map[string]int
	samples map[string][]string
}

func (f *lexicalCoverageFailures) add(c lexicalCoverageCase, problem string) {
	if f.counts == nil {
		f.counts, f.samples = map[string]int{}, map[string][]string{}
	}
	f.counts[c.Family]++
	if len(f.samples[c.Family]) < 3 {
		f.samples[c.Family] = append(f.samples[c.Family], fmt.Sprintf("%s %q: %s", c.ID, c.Input, problem))
	}
}

func (f *lexicalCoverageFailures) report(t *testing.T) {
	t.Helper()
	var families []string
	for family := range f.counts {
		families = append(families, family)
	}
	sort.Strings(families)
	for _, family := range families {
		t.Errorf("%s: %d failure(s)\n%s", family, f.counts[family], strings.Join(f.samples[family], "\n"))
	}
}

// An overbroad schedule recognizer would save questions, observations, quoted
// instructions or negated commitments. A narrow one would lose valid tasks.
func TestLexicalCoverageLocalParser(t *testing.T) {
	var failures lexicalCoverageFailures
	checked := 0
	for _, c := range readLexicalCoverage(t) {
		if c.Checks.Local == nil {
			continue
		}
		checked++
		got, match := PersonalReminder(c.Input)
		if match != c.Checks.Local.Match || match && got != c.Expected.Action {
			failures.add(c, fmt.Sprintf("got %+v, match=%t; want %+v, match=%t", got, match, c.Expected.Action, c.Checks.Local.Match))
		}
	}
	failures.report(t)
	t.Logf("exercised PersonalReminder on %d utterances; no model calls", checked)
}

// Decode is the real post-model authority boundary. These assertions establish
// grounding and target bounds, not whether a model will infer the proposal.
func TestLexicalCoverageAuthority(t *testing.T) {
	var failures lexicalCoverageFailures
	checked := 0
	for _, c := range readLexicalCoverage(t) {
		if c.Checks.Decode == nil {
			continue
		}
		checked++
		proposal, err := json.Marshal(c.Checks.Decode.Proposal)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Decode(string(proposal), Request{Input: c.Input, Targets: c.Context.Targets, Pending: c.Context.Pending})
		if (err == nil) != c.Checks.Decode.Accept || err == nil && got != c.Expected.Action {
			failures.add(c, fmt.Sprintf("proposal=%s got=%+v err=%v; accept=%t want=%+v", proposal, got, err, c.Checks.Decode.Accept, c.Expected.Action))
		}
	}
	failures.report(t)
	t.Logf("exercised Decode on %d grounded/adversarial proposals; no model calls", checked)
}
