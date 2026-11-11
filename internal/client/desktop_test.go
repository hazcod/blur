package client

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-gui/goui/ui"
	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/knowledge/derive"
	"github.com/hazcod/blur/internal/model"
	"github.com/hazcod/blur/internal/power"
	"github.com/hazcod/blur/internal/today"
)

func TestFormatReportDuration(t *testing.T) {
	for _, tc := range []struct {
		duration time.Duration
		want     string
	}{{90 * time.Minute, "1h 30m"}, {45 * time.Minute, "45m"}, {30 * time.Second, "<1m"}, {0, "0m"}} {
		if got := formatReportDuration(int64(tc.duration)); got != tc.want {
			t.Errorf("formatReportDuration(%s) = %q, want %q", tc.duration, got, tc.want)
		}
	}
}

func TestCaptureIndicatorFadesForPausedUnknownAndDisallowedApps(t *testing.T) {
	for _, tc := range []struct {
		name            string
		running         bool
		known           bool
		appID           string
		excludedApps    []string
		defaultDisabled bool
		includedApps    []string
		wantActive      bool
	}{
		{name: "capture active", running: true, known: true, appID: "com.example.editor", wantActive: true},
		{name: "paused", running: false, known: true, appID: "com.example.editor"},
		{name: "unknown app", running: true},
		{name: "excluded app", running: true, known: true, appID: "com.example.secret", excludedApps: []string{"COM.EXAMPLE.SECRET"}},
		{name: "disabled by default", running: true, known: true, appID: "com.example.editor", defaultDisabled: true},
		{name: "explicitly included app", running: true, known: true, appID: "com.example.editor", defaultDisabled: true, includedApps: []string{"COM.EXAMPLE.EDITOR"}, wantActive: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := captureIndicatorPolicyActive(tc.running, tc.known, tc.appID, tc.excludedApps, tc.includedApps, tc.defaultDisabled)
			if got != tc.wantActive {
				t.Fatalf("capture indicator active = %v, want %v", got, tc.wantActive)
			}
		})
	}
}

func TestActivityReportRangeValidationMatchesClientLimits(t *testing.T) {
	for _, test := range []struct {
		name, start, end string
		want             bool
	}{
		{"single day", "2026-11-06", "2026-11-06", true},
		{"maximum span", "2026-10-07", "2026-11-06", true},
		{"reverse order", "2026-11-07", "2026-11-06", false},
		{"invalid calendar date", "2026-02-30", "2026-03-01", false},
		{"too many days", "2026-10-06", "2026-11-06", false},
		{"empty", "", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := validActivityReportRange(test.start, test.end); got != test.want {
				t.Fatalf("validActivityReportRange(%q, %q) = %t, want %t", test.start, test.end, got, test.want)
			}
		})
	}
}

func TestDesktopNavigationKeepsPrimaryDestinationsInStableOrder(t *testing.T) {
	want := []string{"Today", "Ask", "Search", "Library", "Time", "Settings"}
	got := desktopPages()
	if len(got) != len(want) {
		t.Fatalf("desktopPages() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("desktopPages()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestAskProcessingDisclosureIsLocalAndUserInitiated(t *testing.T) {
	copy := askProcessingDisclosure(LocalAISettings{Provider: ProviderOllama})
	for _, phrase := range []string{"when you ask", "matching local sources", "your question", "only your question when no sources match", "this device", "not sent to remote providers"} {
		if !strings.Contains(strings.ToLower(copy), strings.ToLower(phrase)) {
			t.Fatalf("Ask disclosure %q omits %q", copy, phrase)
		}
	}
	remote := askProcessingDisclosure(LocalAISettings{Provider: ProviderOpenAICompatible, RemoteAskEnabled: true})
	for _, phrase := range []string{"matching local sources", "your question", "only your question when no sources match", "configured OpenAI-compatible endpoint", "may retain requests"} {
		if !strings.Contains(strings.ToLower(remote), strings.ToLower(phrase)) {
			t.Fatalf("remote Ask disclosure %q omits %q", remote, phrase)
		}
	}
	remoteOff := askProcessingDisclosure(LocalAISettings{Provider: ProviderOpenAICompatible})
	if !strings.Contains(strings.ToLower(remoteOff), "stay on this device") || !strings.Contains(strings.ToLower(remoteOff), "unless you separately allow") {
		t.Fatalf("remote Ask off disclosure is unclear: %q", remoteOff)
	}
}

func TestRemoteAskConsentCopyMatchesSavedState(t *testing.T) {
	off := remoteAskConsentCopy(false)
	for _, phrase := range []string{"is off", "up to eight", "only when you ask", "may retain"} {
		if !strings.Contains(strings.ToLower(off), strings.ToLower(phrase)) {
			t.Fatalf("remote Ask off copy %q omits %q", off, phrase)
		}
	}
	on := remoteAskConsentCopy(true)
	if !strings.Contains(strings.ToLower(on), "is on") || !strings.Contains(strings.ToLower(on), "saved endpoint") {
		t.Fatalf("remote Ask on copy does not describe saved destination: %q", on)
	}
}

func TestLocalAISettingsDraftNoticeMakesActiveConsentClear(t *testing.T) {
	saved := LocalAISettings{Enabled: true, Provider: ProviderOpenAICompatible, Endpoint: "https://ai.example.test/v1", Model: "gpt", RemoteAskEnabled: true}
	if got := localAISettingsDraftNotice(saved, ProviderOpenAICompatible, saved.Endpoint, saved.Model, true); got != "" {
		t.Fatalf("unchanged model settings show draft warning: %q", got)
	}
	changed := localAISettingsDraftNotice(saved, ProviderOpenAICompatible, "https://other.example.test/v1", saved.Model, false)
	for _, phrase := range []string{"saved model remains active", "remote Ask remains active", "saving a different endpoint or model turns it off"} {
		if !strings.Contains(strings.ToLower(changed), strings.ToLower(phrase)) {
			t.Fatalf("changed endpoint notice %q omits %q", changed, phrase)
		}
	}
	disabled := localAISettingsDraftNotice(LocalAISettings{}, ProviderOllama, "http://127.0.0.1:11434", "qwen3:4b", false)
	if !strings.Contains(strings.ToLower(disabled), "no model requests start until you save and enable") {
		t.Fatalf("disabled-model draft notice is unclear: %q", disabled)
	}
}

func TestAskResultInvalidatedWhenItUsesDeletedSource(t *testing.T) {
	result := &assistant.Result{Sources: []assistant.Evidence{{SourceID: "source-1"}}}
	if !askResultUsesSource(result, "source-1") {
		t.Fatal("Ask result did not recognize its deleted source")
	}
	if askResultUsesSource(result, "source-2") || askResultUsesSource(nil, "source-1") {
		t.Fatal("Ask result matched a source it does not contain")
	}
}

func TestInvalidateAskCancelsRequestAndRejectsLateResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	d := &desktop{askLoading: true, askCancel: cancel, askRequestID: 7, askResult: &assistant.Result{Answer: assistant.Answer{Text: "stale"}}}
	d.invalidateAsk()
	if d.askLoading || d.askResult != nil || d.askCancel != nil || d.askRequestID == 7 {
		t.Fatalf("Ask state was not invalidated: %+v", d)
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("in-flight Ask context was not cancelled")
	}
	if askRequestCurrent(7, d.askRequestID) {
		t.Fatal("late result from invalidated Ask is still current")
	}
}

func TestClosingQuickPanelCollapsesAnswerButPreservesDraftAndSetupWatcher(t *testing.T) {
	stop := make(chan struct{})
	d := &desktop{quickInput: "draft task", expandedAnswer: true, askResult: &assistant.Result{Answer: assistant.Answer{Text: "expanded answer"}},
		permissionWatchStop: stop, permissionWatchActive: true}
	d.collapseTransientContent("quick")
	if d.askResult != nil || d.expandedAnswer {
		t.Fatal("closing Quick Capture retained its expanded answer")
	}
	if d.quickInput != "draft task" {
		t.Fatalf("closing Quick Capture discarded the draft: %q", d.quickInput)
	}
	if !d.permissionWatchActive {
		t.Fatal("closing Quick Capture stopped setup permission checks")
	}
	select {
	case <-stop:
		t.Fatal("closing Quick Capture closed the setup permission watcher")
	default:
	}
}

func TestClosingDashboardCollapsesExpandedSourceText(t *testing.T) {
	d := &desktop{expandedSourceID: "source-1", expandedChronicleID: "hour-1", chronicleSources: []SearchHit{{SourceID: "source-1"}},
		quickNoteExpanded: true, expandedAnswer: true, note: "unfinished note", askQuestion: "unfinished question", askResult: &assistant.Result{Answer: assistant.Answer{Text: "expanded answer"}}}
	d.collapseTransientContent("main")
	if d.expandedSourceID != "" || d.expandedChronicleID != "" || len(d.chronicleSources) != 0 {
		t.Fatalf("closing dashboard retained expanded source content: %+v", d)
	}
	if d.quickNoteExpanded || d.askResult != nil || d.expandedAnswer {
		t.Fatal("closing dashboard retained expanded editor or answer")
	}
	if d.note != "unfinished note" || d.askQuestion != "unfinished question" {
		t.Fatal("closing dashboard discarded a draft")
	}
}

func TestAskActionStaysDisabledWhileMemoryIsChanging(t *testing.T) {
	if askInputEnabled(false, true) {
		t.Fatal("Ask input remained enabled during a memory mutation")
	}
	if askSubmitEnabled(false, true, "What changed?") {
		t.Fatal("Ask submit remained enabled during a memory mutation")
	}
	if !askInputEnabled(false, false) || !askSubmitEnabled(false, false, "What changed?") {
		t.Fatal("Ask did not become available again after a mutation")
	}
}

func TestMemoryViewGenerationRejectsLateResults(t *testing.T) {
	if !currentMemoryRead(4, 4) {
		t.Fatal("current memory read was rejected")
	}
	if currentMemoryRead(3, 4) {
		t.Fatal("read started before a deletion was accepted after the memory view changed")
	}
}

func TestKnowledgeSuggestionResultRejectsStaleRecapAndMemoryGeneration(t *testing.T) {
	if !knowledgeSuggestionCurrent(8, 8, "hour:a", "hour:a", 3, 3) {
		t.Fatal("current recap suggestion result was rejected")
	}
	for _, test := range []struct {
		request, current              uint64
		recap, expanded               string
		generation, currentGeneration uint64
	}{
		{7, 8, "hour:a", "hour:a", 3, 3},
		{8, 8, "hour:a", "hour:b", 3, 3},
		{8, 8, "hour:a", "hour:a", 2, 3},
	} {
		if knowledgeSuggestionCurrent(test.request, test.current, test.recap, test.expanded, test.generation, test.currentGeneration) {
			t.Fatalf("stale suggestion published: %+v", test)
		}
	}
}

func TestDismissKnowledgeSuggestionRemovesOnlySelectedProposal(t *testing.T) {
	proposals := []derive.Proposal{{Name: "A"}, {Name: "B"}, {Name: "C"}}
	got := dismissKnowledgeSuggestion(proposals, 1)
	if len(got) != 2 || got[0].Name != "A" || got[1].Name != "C" || len(proposals) != 3 {
		t.Fatalf("dismissed proposals=%+v original=%+v", got, proposals)
	}
	if got := dismissKnowledgeSuggestion(proposals, -1); len(got) != len(proposals) {
		t.Fatalf("invalid index changed proposals: %+v", got)
	}
}

func TestKnowledgeSuggestionActionRequiresLocalModelAndStableMemory(t *testing.T) {
	for _, test := range []struct{ localModel, memoryChanging, loading, want bool }{
		{true, false, false, true},
		{false, false, false, false},
		{true, true, false, false},
		{true, false, true, false},
	} {
		if got := knowledgeSuggestionActionEnabled(test.localModel, test.memoryChanging, test.loading); got != test.want {
			t.Fatalf("knowledgeSuggestionActionEnabled(%t, %t, %t) = %t, want %t", test.localModel, test.memoryChanging, test.loading, got, test.want)
		}
	}
}

func TestMemoryInvalidationClearsVisibleEvidenceAndCancelsLoads(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	d := &desktop{
		memoryGeneration:        3,
		today:                   TodayView{State: "ready"},
		todayLoading:            true,
		todayRequestID:          12,
		searchHits:              []SearchHit{{SourceID: "source"}},
		searchComplete:          true,
		searchFailed:            true,
		library:                 knowledge.Page{Entities: []knowledge.Entity{{ID: "entity"}}},
		libraryLoading:          true,
		libraryLoaded:           true,
		reportReady:             true,
		askLoading:              true,
		askCancel:               cancel,
		askRequestID:            9,
		knowledgeProposals:      []derive.Proposal{{Name: "Stale"}},
		knowledgeProposalHourID: "hour:20261106T13Z",
		knowledgeSuggesting:     true,
	}
	d.invalidateMemoryViews()
	if d.memoryGeneration != 4 || d.todayLoading || d.today.State != "" || d.searchHits != nil || d.searchComplete || d.searchFailed || d.libraryLoading || d.libraryLoaded || d.library.Entities != nil || d.reportReady || d.askLoading || d.askCancel != nil || d.askRequestID == 9 || d.knowledgeProposals != nil || d.knowledgeProposalHourID != "" || d.knowledgeSuggesting {
		t.Fatalf("memory-derived UI state was not invalidated: %+v", d)
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("Ask was not cancelled during invalidation")
	}
	if currentMemoryRead(3, d.memoryGeneration) {
		t.Fatal("in-flight view load remains valid after invalidation")
	}
}

func TestAskErrorMessageGivesUsefulLocalRecovery(t *testing.T) {
	if got := askErrorMessage(assistant.ErrContext); got != "No matching local sources were found. Try a different question." {
		t.Fatalf("no-evidence guidance = %q", got)
	}
	if got := askErrorMessage(assistant.ErrTokenBudget); got != "That question and its matching evidence are too large. Try a shorter question." {
		t.Fatalf("budget guidance = %q", got)
	}
	if got := askErrorMessage(model.ErrOllamaUnavailable); got != "Ollama is unavailable. Make sure it’s running and the selected model is installed in Settings." {
		t.Fatalf("Ollama recovery guidance = %q", got)
	}
	if got := askErrorMessage(model.ErrOllamaResponse); got != "Ollama returned an answer Blur couldn’t verify. Try a different local model or update it." {
		t.Fatalf("invalid response guidance = %q", got)
	}
	if got := askErrorMessage(errors.New("transport details must stay private")); got != "Could not answer from local memory. Try a shorter question or check Ollama in Settings." {
		t.Fatalf("generic failure leaked or misreported: %q", got)
	}
}

func TestAskErrorMessageGivesProviderNeutralRemoteRecovery(t *testing.T) {
	if got := askErrorMessage(model.ErrCompatibleUnavailable, ProviderOpenAICompatible); got != "The configured AI endpoint is unavailable. Check its address and connection in Settings." {
		t.Fatalf("compatible endpoint recovery = %q", got)
	}
	if got := askErrorMessage(errors.New("private transport details"), ProviderOpenAICompatible); !strings.Contains(got, "configured provider in Settings") {
		t.Fatalf("compatible generic recovery is not actionable: %q", got)
	}
}

func TestLocalAIDataFlowCopySeparatesLocalAskFromRemoteNotes(t *testing.T) {
	local := localAIDataFlowCopy(true, ProviderOllama)
	for _, phrase := range []string{"explicitly ask", "captured sources", "loopback Ollama", "may retain request logs"} {
		if !strings.Contains(strings.ToLower(local), strings.ToLower(phrase)) {
			t.Fatalf("local AI disclosure %q omits %q", local, phrase)
		}
	}
	remote := localAIDataFlowCopy(true, ProviderOpenAICompatible)
	for _, phrase := range []string{"only sanitized notes", "never sent remotely", "unless you separately enable remote Ask"} {
		if !strings.Contains(strings.ToLower(remote), strings.ToLower(phrase)) {
			t.Fatalf("remote AI disclosure %q omits %q", remote, phrase)
		}
	}
	if strings.Contains(strings.ToLower(remote), "captured sources when you explicitly ask") {
		t.Fatalf("remote AI disclosure implies captured sources leave the device: %q", remote)
	}
	consented := localAIDataFlowCopy(true, ProviderOpenAICompatible, true)
	for _, phrase := range []string{"up to eight matching captured sources", "explicitly ask", "may retain requests"} {
		if !strings.Contains(strings.ToLower(consented), strings.ToLower(phrase)) {
			t.Fatalf("consented remote AI disclosure %q omits %q", consented, phrase)
		}
	}
}

func TestDeleteAllPrivacyActionRequiresSecondConfirmationAndStatesScope(t *testing.T) {
	if got := deleteAllActionLabel(false); got != "Delete all memory" {
		t.Fatalf("initial delete-all action = %q", got)
	}
	if got := deleteAllActionLabel(true); got != "Delete permanently" {
		t.Fatalf("confirmed delete-all action = %q", got)
	}
	copy := deleteAllConfirmationCopy()
	for _, required := range []string{"notes", "knowledge library entries", "captured sources", "derived recap items", "AI settings remain", "cannot be undone"} {
		if !strings.Contains(strings.ToLower(copy), strings.ToLower(required)) {
			t.Fatalf("delete-all confirmation copy %q omits %q", copy, required)
		}
	}
}

func TestRetentionControlsRequireConfirmationAndExplainDeletionScope(t *testing.T) {
	if got := retentionOptionLabel(90, true); got != "90 days" {
		t.Fatalf("selected retention option label = %q", got)
	}
	if got := retentionOptionLabel(0, false); got != "Off" {
		t.Fatalf("off retention option label = %q", got)
	}
	if got := retentionApplyLabel(30); got != "Apply and prune now" {
		t.Fatalf("enabled retention action = %q", got)
	}
	if got := retentionApplyLabel(0); got != "Turn off automatic pruning" {
		t.Fatalf("disabled retention action = %q", got)
	}
	for _, required := range []string{"captured sources", "derived recap items", "cannot be undone", "upgrade date"} {
		copy := strings.ToLower(retentionConfirmationCopy(30))
		if !strings.Contains(copy, strings.ToLower(required)) {
			t.Fatalf("retention confirmation %q omits %q", copy, required)
		}
	}
	if copy := retentionConfirmationCopy(0); !strings.Contains(copy, "Existing memory will remain") {
		t.Fatalf("disabling retention copy is ambiguous: %q", copy)
	}
}

func TestQuickNoteControlIsMultiline(t *testing.T) {
	input := quickNoteInput("first line\nsecond line", func(string) {})
	if _, ok := any(input).(*ui.TextViewView); !ok {
		t.Fatalf("quick note input has type %T, want native multiline editor", input)
	}
}

func TestTodayComposerStaysAheadOfRecapContent(t *testing.T) {
	dateControls := ui.Label("date controls")
	composer := ui.Label("quick note")
	recap := ui.Label("recap")
	got := todayContentViews(dateControls, composer, []ui.View{recap}, true)
	if len(got) != 3 || got[0] != dateControls || got[1] != composer || got[2] != recap {
		t.Fatalf("today content order = %#v, want date controls, composer, recap", got)
	}
	withoutComposer := todayContentViews(dateControls, composer, []ui.View{recap}, false)
	if len(withoutComposer) != 2 || withoutComposer[0] != dateControls || withoutComposer[1] != recap {
		t.Fatalf("today content without note permission = %#v, want date controls, recap", withoutComposer)
	}
}

func TestOpenSettingsSetsPageAndGroup(t *testing.T) {
	d := &desktop{page: "Today", settingsGroup: settingsGroupCapture, status: "old"}
	d.openSettings(settingsGroupAI)
	if d.page != "Settings" || d.settingsGroup != settingsGroupAI || d.status != "" {
		t.Fatalf("openSettings(AI) = page %q group %q status %q", d.page, d.settingsGroup, d.status)
	}
	d.openSettings("bogus")
	if d.page != "Settings" || d.settingsGroup != settingsGroupCapture {
		t.Fatalf("openSettings(bogus) = page %q group %q, want Settings/Capture", d.page, d.settingsGroup)
	}
}

func TestSettingsGroupsOrderAndOverviewLength(t *testing.T) {
	want := []string{"Capture", "AI", "Quick ask", "Connections", "Memory", "Privacy"}
	got := settingsGroups()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("settingsGroups() = %v, want %v", got, want)
	}
	if n := strings.Count(privacyOverviewCopy(), ". ") + 1; n > 3 {
		t.Fatalf("privacy overview has %d sentences, want at most 3", n)
	}
}

func TestPrivacyOverviewKeepsStorageAndReviewBoundariesVisible(t *testing.T) {
	copy := strings.ToLower(privacyOverviewCopy())
	for _, phrase := range []string{
		"encrypted locally before they enter the durable store",
		"tasks need confirmation",
		"without sending messages or scheduling notifications",
		"library entries are created only from details you enter",
	} {
		if !strings.Contains(copy, phrase) {
			t.Fatalf("privacy overview %q omits %q", copy, phrase)
		}
	}
}

func TestQuickNoteDisclosureNamesDraftRecovery(t *testing.T) {
	for _, test := range []struct {
		name      string
		expanded  bool
		hasDraft  bool
		wantLabel string
	}{
		{name: "collapsed empty composer", wantLabel: "Add a note"},
		{name: "expanded composer", expanded: true, wantLabel: "Hide editor"},
		{name: "collapsed unsent draft", hasDraft: true, wantLabel: "Continue draft"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := quickNoteDisclosureLabel(test.expanded, test.hasDraft); got != test.wantLabel {
				t.Fatalf("quickNoteDisclosureLabel(%t, %t) = %q, want %q", test.expanded, test.hasDraft, got, test.wantLabel)
			}
		})
	}
}

func TestKnowledgeDraftValidationAndSaveState(t *testing.T) {
	for _, test := range []struct {
		name, draft, note string
		kind              knowledge.Kind
		want              bool
	}{
		{"valid person", "Alex", "Works on the launch", knowledge.Person, true},
		{"optional note", "Project Aurora", "", knowledge.Project, true},
		{"trimmed input", "  Aurora  ", "  note  ", knowledge.Topic, true},
		{"empty name", "  ", "note", knowledge.Project, false},
		{"unknown type", "Aurora", "", knowledge.Kind("unknown"), false},
		{"oversized name", strings.Repeat("a", knowledge.MaxTextBytes+1), "", knowledge.Project, false},
		{"oversized note", "Aurora", strings.Repeat("a", knowledge.MaxTextBytes+1), knowledge.Project, false},
		{"nul byte", "Aurora\x00x", "", knowledge.Project, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := validKnowledgeDraft(test.kind, test.draft, test.note); got != test.want {
				t.Fatalf("validKnowledgeDraft() = %t, want %t", got, test.want)
			}
		})
	}
	if got := knowledgeSaveLabel(knowledge.Person, false); got != "Add person" {
		t.Fatalf("save action = %q", got)
	}
	if got := knowledgeSaveLabel(knowledge.Project, true); got != "Saving…" {
		t.Fatalf("saving action = %q", got)
	}
}

func TestKnowledgeRemovalConfirmationExplainsItsScope(t *testing.T) {
	copy := strings.ToLower(knowledgeDeleteConfirmationCopy())
	for _, required := range []string{"library entry", "notes", "captured sources remain", "cannot undo"} {
		if !strings.Contains(copy, required) {
			t.Fatalf("knowledge delete confirmation %q omits %q", copy, required)
		}
	}
	if got := knowledgeDeleteActionLabel(false, false); got != "Remove entry" {
		t.Fatalf("initial removal action=%q", got)
	}
	if got := knowledgeDeleteActionLabel(true, false); got != "Remove permanently" {
		t.Fatalf("confirmed removal action=%q", got)
	}
	if got := knowledgeDeleteActionLabel(false, true); got != "Removing…" {
		t.Fatalf("in-flight removal action=%q", got)
	}
}

func TestRemoveKnowledgeFromPageDropsOnlySelectedEntityAndPreservesCursor(t *testing.T) {
	page := knowledge.Page{Entities: []knowledge.Entity{{ID: "one", Name: "One"}, {ID: "two", Name: "Two"}}, Next: "two", HasMore: true}
	got, removed := removeKnowledgeFromPage(page, "one")
	if !removed || len(got.Entities) != 1 || got.Entities[0].ID != "two" || got.Next != "two" || !got.HasMore {
		t.Fatalf("removed page=%+v removed=%t", got, removed)
	}
	if _, removed := removeKnowledgeFromPage(got, "missing"); removed {
		t.Fatal("missing entity reported as removed")
	}
}

func TestKnowledgeEntryActionsRespectMergeState(t *testing.T) {
	merged := knowledge.Entity{ID: "source", State: knowledge.Merged, MergedInto: "target"}
	if canRemoveKnowledgeEntry(merged) || !canUnmergeKnowledgeEntry(merged) {
		t.Fatalf("merged source actions: remove=%t unmerge=%t", canRemoveKnowledgeEntry(merged), canUnmergeKnowledgeEntry(merged))
	}
	target := knowledge.Entity{ID: "target", State: knowledge.Confirmed, RelatedIDs: []string{"source"}}
	if canRemoveKnowledgeEntry(target) || canUnmergeKnowledgeEntry(target) {
		t.Fatalf("merge destination exposes invalid action: remove=%t unmerge=%t", canRemoveKnowledgeEntry(target), canUnmergeKnowledgeEntry(target))
	}
	ordinary := knowledge.Entity{ID: "ordinary", State: knowledge.Confirmed}
	if !canRemoveKnowledgeEntry(ordinary) || canUnmergeKnowledgeEntry(ordinary) {
		t.Fatalf("ordinary entity actions: remove=%t unmerge=%t", canRemoveKnowledgeEntry(ordinary), canUnmergeKnowledgeEntry(ordinary))
	}
	copy := strings.ToLower(knowledgeUnmergeConfirmationCopy())
	for _, required := range []string{"restore this entry", "destination", "details", "captured sources remain"} {
		if !strings.Contains(copy, required) {
			t.Fatalf("unmerge confirmation %q omits %q", copy, required)
		}
	}
}

func TestQuickNotePrivacyHintMatchesModelState(t *testing.T) {
	if got := quickNotePrivacyHint(false, ProviderOllama); got != "Private note. AI is off." {
		t.Fatalf("AI-off note hint = %q", got)
	}
	if got := quickNotePrivacyHint(true, ProviderOllama); got != "Private note. Suggestions stay yours to approve." {
		t.Fatalf("AI-on note hint = %q", got)
	}
	if got := quickNotePrivacyHint(true, ProviderOpenAICompatible); got != "Sent to the configured provider. Suggestions need your approval." {
		t.Fatalf("remote-provider note hint = %q", got)
	}
}

func TestSelectedDayRequiresCanonicalLocalDate(t *testing.T) {
	for _, test := range []struct {
		value string
		want  bool
	}{{"2026-11-05", true}, {"2026-02-30", false}, {"2026-1-05", false}, {" 2026-11-05 ", false}, {"", false}} {
		if got := validSelectedDay(test.value); got != test.want {
			t.Errorf("validSelectedDay(%q) = %t, want %t", test.value, got, test.want)
		}
	}
}

func TestSelectedDayLabelUsesHumanReadableDate(t *testing.T) {
	if got, want := selectedDayLabel("2026-11-06"), "Friday, November 6"; got != want {
		t.Fatalf("selectedDayLabel() = %q, want %q", got, want)
	}
	if got := selectedDayLabel("not-a-date"); got != "Choose a day" {
		t.Fatalf("invalid selectedDayLabel() = %q, want a safe prompt", got)
	}
}

func TestDesktopWithoutEncryptedStorageStatesCaptureAndSavingAreOff(t *testing.T) {
	d := newDesktop(nil, nil, nil)
	if d.client != nil || d.capture != nil {
		t.Fatal("unconfigured desktop retained a client or capture controller")
	}
	if d.status != "Encrypted local storage is unavailable. Blur has not started capture or saved anything." {
		t.Fatalf("setup status = %q", d.status)
	}
}

func TestCaptureDescriptionMatchesControllerState(t *testing.T) {
	if got := captureDescription(false, true); got != "Capture starts automatically when Blur opens. Accessibility access may be required; Blur never requests it automatically." {
		t.Fatalf("paused capture description = %q", got)
	}
	if got := captureDescription(false, false); got != "Capture is paused. Start it any time. Blur reads only the currently focused app and never navigates its history." {
		t.Fatalf("explicitly paused capture description = %q", got)
	}
	if got := captureDescription(true, true); got != "Capture is on for the currently focused app. Blur never navigates its history." {
		t.Fatalf("running capture description = %q", got)
	}
}

func TestCaptureActionLabelMatchesControllerState(t *testing.T) {
	if got := captureActionLabel(false); got != "Start capture" {
		t.Fatalf("paused capture action = %q, want Start capture", got)
	}
	if got := captureActionLabel(true); got != "Pause capture" {
		t.Fatalf("running capture action = %q, want Pause capture", got)
	}
}

func TestPowerSettingsCopyKeepsBatteryDistinctFromBatterySaver(t *testing.T) {
	if got := powerModeDescription(power.FollowSystem); !strings.Contains(got, "battery-saver mode") || !strings.Contains(got, "Balanced") {
		t.Fatalf("Follow System description = %q", got)
	}
	if got := powerStateSummary(power.Effective{RequestedMode: power.FollowSystem}, power.ErrSystemStateUnavailable); got != "System power state is unavailable; Follow System is using Balanced." {
		t.Fatalf("unknown system state = %q", got)
	}
	if got := powerStateSummary(power.Effective{}, power.ErrSystemStateUnavailable); got != "Power settings need repair; save a mode to restore the policy." {
		t.Fatalf("invalid saved settings = %q", got)
	}
	if got := powerStateSummary(power.Effective{RequestedMode: power.FollowSystem, System: power.SystemState{Known: true, SaverEnabled: true}, Mode: power.Saver}, nil); got != "System battery-saver mode is on; Blur Saver is active." {
		t.Fatalf("system saver state = %q", got)
	}
	if got := powerStateSummary(power.Effective{RequestedMode: power.Balanced, Mode: power.Balanced}, nil); got != "Manual mode: Balanced." {
		t.Fatalf("manual state = %q", got)
	}
}

func TestParsePowerBudgetEnforcesModeAndProductCaps(t *testing.T) {
	for _, test := range []struct {
		name      string
		mode      power.Mode
		jobs      string
		tokens    string
		seconds   string
		want      power.Config
		wantValid bool
	}{
		{"balanced custom", power.Balanced, "1", "512", "30", power.Config{Mode: power.Balanced, MaxBackgroundAIJobs: 1, MaxBackgroundTokens: 512, MaxBackgroundDuration: 30 * time.Second}, true},
		{"follow system custom", power.FollowSystem, "1", "1024", "45", power.Config{Mode: power.FollowSystem, MaxBackgroundAIJobs: 1, MaxBackgroundTokens: 1024, MaxBackgroundDuration: 45 * time.Second}, true},
		{"performance custom", power.Performance, "2", "4096", "120", power.Config{Mode: power.Performance, MaxBackgroundAIJobs: 2, MaxBackgroundTokens: 4096, MaxBackgroundDuration: 120 * time.Second}, true},
		{"balanced concurrency cannot exceed one", power.Balanced, "2", "512", "30", power.Config{}, false},
		{"performance cannot exceed product cap", power.Performance, "3", "4096", "120", power.Config{}, false},
		{"zero token budget rejected", power.Balanced, "1", "0", "30", power.Config{}, false},
		{"duration must be positive", power.Performance, "1", "1000", "0", power.Config{}, false},
		{"non numeric rejected", power.Balanced, "one", "512", "30", power.Config{}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, ok := parsePowerBudget(test.mode, test.jobs, test.tokens, test.seconds)
			if ok != test.wantValid || ok && got != test.want {
				t.Fatalf("parsePowerBudget(...) = (%+v, %t), want (%+v, %t)", got, ok, test.want, test.wantValid)
			}
		})
	}
}

func TestPowerConfigDirtyCheckDoesNotMarkDefaultBudgetChanged(t *testing.T) {
	defaults := power.DefaultConfig()
	jobs, tokens, seconds := powerBudgetDraftValues(defaults)
	draft, ok := parsePowerBudget(defaults.Mode, jobs, tokens, seconds)
	if !ok {
		t.Fatal("default power budget did not parse")
	}
	if powerConfigChanged(defaults, draft) {
		t.Fatal("opening default Follow System settings should not mark them dirty")
	}
	draft.MaxBackgroundTokens /= 2
	if !powerConfigChanged(defaults, draft) {
		t.Fatal("a customized token budget should be dirty")
	}
}

func TestBrowserCaptureDisclosureExplainsActivePageAndSafariExtension(t *testing.T) {
	got := browserCaptureDisclosure()
	for _, want := range []string{"Active browser pages", "address bars, tabs, and form fields", "private-window markers", "detection varies by browser and locale", "never navigates or reads history", "optional signed build"} {
		if !strings.Contains(got, want) {
			t.Fatalf("browser capture disclosure %q does not contain %q", got, want)
		}
	}
}

func TestEmptyTodayOffersCaptureSetupOnlyWhenAvailable(t *testing.T) {
	if !shouldOfferCaptureSetup("empty", true, false) {
		t.Fatal("empty day with an available capture controller should offer setup")
	}
	if shouldOfferCaptureSetup("ready", true, false) || shouldOfferCaptureSetup("empty", false, false) {
		t.Fatal("capture setup should only be offered for an empty day with an available controller")
	}
	if shouldOfferCaptureSetup("empty", true, true) {
		t.Fatal("an already-running capture controller should not keep showing setup")
	}
}

func TestTodayLoadResultMustMatchLatestRequestAndSelectedDate(t *testing.T) {
	for _, test := range []struct {
		name                        string
		requestID, latestRequestID  uint64
		requestedDate, selectedDate string
		want                        bool
	}{
		{"latest result", 3, 3, "2026-11-05", "2026-11-05", true},
		{"older result", 2, 3, "2026-11-04", "2026-11-05", false},
		{"selection changed", 3, 3, "2026-11-04", "2026-11-05", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := currentTodayLoad(test.requestID, test.latestRequestID, test.requestedDate, test.selectedDate); got != test.want {
				t.Fatalf("currentTodayLoad(...) = %t, want %t", got, test.want)
			}
		})
	}
}

func TestLiveCaptureRefreshIsQuietOutsideCurrentTodayView(t *testing.T) {
	for _, test := range []struct {
		name, page, selectedDate, todayDate string
		running, want                       bool
	}{
		{"current day while capturing", "Today", "2026-11-06", "2026-11-06", true, true},
		{"other page", "Search", "2026-11-06", "2026-11-06", true, false},
		{"historical date", "Today", "2026-11-05", "2026-11-06", true, false},
		{"capture paused", "Today", "2026-11-06", "2026-11-06", false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := shouldRefreshLiveToday(test.page, test.selectedDate, test.todayDate, test.running); got != test.want {
				t.Fatalf("shouldRefreshLiveToday(...) = %t, want %t", got, test.want)
			}
		})
	}
}

func TestLiveTodayRefreshCoalescesChangesDuringLoad(t *testing.T) {
	pending := false
	for range 3 {
		if start := queueLiveTodayRefresh(true, &pending); start {
			t.Fatal("capture change started a duplicate Today load")
		}
	}
	if !pending {
		t.Fatal("capture changes during a load were not remembered")
	}
	if !consumeLiveTodayRefresh(&pending) || pending {
		t.Fatal("pending refresh was not consumed exactly once")
	}
	if consumeLiveTodayRefresh(&pending) {
		t.Fatal("coalesced refresh was scheduled more than once")
	}
	if start := queueLiveTodayRefresh(false, &pending); !start {
		t.Fatal("idle Today refresh was incorrectly queued")
	}
}

func TestSearchEmptyStateDistinguishesInitialNoMatchesLoadingAndFailure(t *testing.T) {
	for _, test := range []struct {
		name      string
		loading   bool
		completed bool
		failed    bool
		results   int
		want      string
	}{
		{"initial", false, false, false, 0, "Search uses exact local terms and does not need AI."},
		{"no matches", false, true, false, 0, "No matches. Try a shorter phrase or an exact word."},
		{"loading", true, false, false, 0, ""},
		{"failed", false, true, true, 0, ""},
		{"has results", false, true, false, 1, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := searchEmptyMessage(test.loading, test.completed, test.failed, test.results); got != test.want {
				t.Fatalf("searchEmptyMessage(...) = %q, want %q", got, test.want)
			}
		})
	}
}

func TestSearchPreviewBoundsLongSourcesWithoutSplittingUnicode(t *testing.T) {
	short, truncated := searchPreview("short source")
	if truncated || short != "short source" {
		t.Fatalf("short preview = %q, truncated=%t", short, truncated)
	}
	long := strings.Repeat("a", searchPreviewRunes-1) + "🪼" + "tail"
	preview, truncated := searchPreview(long)
	if !truncated || !strings.HasSuffix(preview, "…") || strings.ContainsRune(preview, '\uFFFD') {
		t.Fatalf("long preview is not safely truncated: truncated=%t suffix=%q", truncated, preview[len(preview)-min(8, len(preview)):])
	}
	if got := len([]rune(strings.TrimSuffix(preview, "…"))); got != searchPreviewRunes {
		t.Fatalf("preview contains %d runes, want %d", got, searchPreviewRunes)
	}
}

func TestAnswerPreviewBoundsLongAnswersWithoutSplittingUnicode(t *testing.T) {
	short, truncated := answerPreview("short answer")
	if truncated || short != "short answer" {
		t.Fatalf("short preview = %q, truncated=%t", short, truncated)
	}
	long := strings.Repeat("a", answerPreviewRunes-1) + "🪼" + "tail"
	preview, truncated := answerPreview(long)
	if !truncated || !strings.HasSuffix(preview, "…") || strings.ContainsRune(preview, '\uFFFD') {
		t.Fatalf("long answer is not safely truncated: truncated=%t suffix=%q", truncated, preview[len(preview)-min(8, len(preview)):])
	}
	if got := len([]rune(strings.TrimSuffix(preview, "…"))); got != answerPreviewRunes {
		t.Fatalf("preview contains %d runes, want %d", got, answerPreviewRunes)
	}
}

func TestSearchResultAppliesOnlyToTheQueryThatStartedIt(t *testing.T) {
	if !currentSearchLoad("project recap", "project recap") {
		t.Fatal("result for the active query was rejected")
	}
	if currentSearchLoad("older query", "new query") {
		t.Fatal("result from an older query should be ignored")
	}
}

func TestLibraryRetryAppearsOnlyAfterAnInitialLoadFailure(t *testing.T) {
	for _, test := range []struct {
		name    string
		loading bool
		loaded  bool
		status  string
		want    bool
	}{
		{"failed", false, false, "Storage unavailable", true},
		{"loading", true, false, "Loading", false},
		{"not started", false, false, "", false},
		{"loaded", false, true, "Storage unavailable", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := canRetryLibraryLoad(test.loading, test.loaded, test.status); got != test.want {
				t.Fatalf("canRetryLibraryLoad(...) = %t, want %t", got, test.want)
			}
		})
	}
}

func TestOpenLoopLinesExposeOnlyNonEmptyRecapItems(t *testing.T) {
	got := openLoopLines([]today.OpenLoop{{Text: "Waiting for the launch date"}, {Text: ""}})
	if len(got) != 1 || got[0] != "Waiting for the launch date" {
		t.Fatalf("openLoopLines() = %#v", got)
	}
}

func TestInvalidateMemoryViewsAlwaysClearsLoading(t *testing.T) {
	d := &desktop{loading: true, status: ""}
	d.invalidateMemoryViews()
	if d.loading {
		t.Fatal("loading stayed set after invalidation")
	}
}

func TestCurrentChronicleSourcesLoad(t *testing.T) {
	if !currentChronicleSourcesLoad(3, 3, "h1", "h1") {
		t.Fatal("matching request rejected")
	}
	for _, tc := range []struct {
		name               string
		id, latest         uint64
		loaded, expandedID string
	}{
		{"superseded", 2, 3, "h1", "h1"},
		{"collapsed", 3, 3, "h1", ""},
		{"switched", 3, 3, "h1", "h2"},
		{"no id", 3, 3, "", ""},
	} {
		if currentChronicleSourcesLoad(tc.id, tc.latest, tc.loaded, tc.expandedID) {
			t.Errorf("%s: stale load accepted", tc.name)
		}
	}
}

func TestNoteAfterSaveKeepsTextTypedDuringSave(t *testing.T) {
	for _, tc := range []struct{ current, saved, want string }{
		{"hello", "hello", ""},
		{"  hello \n", "hello", ""},
		{"hello world", "hello", "hello world"},
		{"", "hello", ""},
	} {
		if got := noteAfterSave(tc.current, tc.saved); got != tc.want {
			t.Errorf("noteAfterSave(%q,%q) = %q, want %q", tc.current, tc.saved, got, tc.want)
		}
	}
}

func TestRetentionUnknownPolicyLabelAndGate(t *testing.T) {
	if got := retentionCurrentLabel(0, true); got != "Currently unknown" {
		t.Fatalf("unknown label = %q", got)
	}
	if got := retentionCurrentLabel(0, false); got != "Currently Off" {
		t.Fatalf("off label = %q", got)
	}
	if got := retentionCurrentLabel(90, false); got != "Currently 90 days" {
		t.Fatalf("90 label = %q", got)
	}
	if retentionChangeEnabled(false, true, 0) {
		t.Fatal("Off confirmation enabled while policy unknown")
	}
	if !retentionChangeEnabled(false, true, 30) || !retentionChangeEnabled(false, false, 0) || retentionChangeEnabled(true, false, 30) {
		t.Fatal("retention gate has wrong enabled state")
	}
}

func TestDeleteSourceFailureStatusMentionsPausedCapture(t *testing.T) {
	if got := deleteSourceFailureStatus(true); !strings.HasSuffix(got, "Capture remains paused.") {
		t.Fatalf("status = %q", got)
	}
	if got := deleteSourceFailureStatus(false); strings.Contains(got, "paused") {
		t.Fatalf("status = %q", got)
	}
}

func TestProviderSelectionChanges(t *testing.T) {
	if providerSelectionChanges(ProviderOllama, ProviderOllama) || providerSelectionChanges(ProviderOpenAICompatible, ProviderOpenAICompatible) {
		t.Fatal("reselecting the current provider must be a no-op")
	}
	if !providerSelectionChanges(ProviderOllama, ProviderOpenAICompatible) {
		t.Fatal("switching provider must change")
	}
}

func TestBackgroundSummariesPausedNote(t *testing.T) {
	if got := backgroundSummariesPausedNote(true, false); got != " Hourly recaps were paused." {
		t.Fatalf("flip note = %q", got)
	}
	if backgroundSummariesPausedNote(false, false) != "" || backgroundSummariesPausedNote(true, true) != "" || backgroundSummariesPausedNote(false, true) != "" {
		t.Fatal("note shown without a true-to-false flip")
	}
}

func TestBackgroundSummariesErrorStatus(t *testing.T) {
	wrapped := errors.Join(errLocalAIUnavailable, errors.New("x"))
	if got := backgroundSummariesErrorStatus(true, wrapped); !strings.Contains(got, "Enable local Ollama first") {
		t.Fatalf("enable unavailable = %q", got)
	}
	if got := backgroundSummariesErrorStatus(false, errLocalAIUnavailable); strings.Contains(got, "Enable local Ollama") || !strings.Contains(got, "paused") {
		t.Fatalf("disable unavailable = %q", got)
	}
	if got := backgroundSummariesErrorStatus(true, errors.New("disk")); strings.Contains(got, "Enable local Ollama") {
		t.Fatalf("other error = %q", got)
	}
}
