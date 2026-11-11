package client

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/golang-gui/goui/core/geometry"
	"github.com/golang-gui/goui/gui"
	"github.com/golang-gui/goui/layout"
	"github.com/golang-gui/goui/style"
	"github.com/golang-gui/goui/theme/modern"
	"github.com/golang-gui/goui/ui"
	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/appidentity"
	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/chronicle"
	"github.com/hazcod/blur/internal/ingest"
	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/knowledge/derive"
	"github.com/hazcod/blur/internal/model"
	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/platform"
	"github.com/hazcod/blur/internal/power"
	"github.com/hazcod/blur/internal/preferences"
	"github.com/hazcod/blur/internal/today"
)

// RunDesktop starts Blur's native-window Go shell. A nil or incomplete client
// opens an honest setup state and never falls back to unencrypted storage.
func RunDesktop(client *Client, captureControllers ...*ingest.Controller) error {
	return RunDesktopWithSetupIssue(client, nil, captureControllers...)
}

// RunDesktopWithSetupIssue opens the desktop with a safe explanation when
// secure local storage could not be initialized.
func RunDesktopWithSetupIssue(client *Client, setupIssue error, captureControllers ...*ingest.Controller) error {
	endBackgroundActivity, err := keepDesktopBackgroundAlive()
	if err != nil {
		slog.Warn("background app lifetime protection unavailable", "result", "failed")
	} else {
		defer endBackgroundActivity()
	}
	done := make(chan struct{})
	defer close(done)
	var d *desktop
	defer func() { d.cancelPauseTimer() }()
	var menuOnce sync.Once
	defer unregisterQuickAskHotkey()
	return ui.Run(appidentity.BundleID, func(app ui.App) ui.RootView {
		menuOnce.Do(func() {
			if err := installDesktopMainMenu(); err != nil {
				// Keep the usable window available; the window-level shortcut remains
				// as a fallback if AppKit could not install its standard app menu.
				slog.Error("native application menu unavailable", "result", "failed")
			}
		})
		if d == nil {
			var controller *ingest.Controller
			if len(captureControllers) > 0 {
				controller = captureControllers[0]
			}
			d = newDesktop(app, client, controller)
			d.captureSetupDone = done
			d.watchQuickPanelFocus()
			if client == nil && setupIssue != nil {
				d.startupIssue = SetupIssueMessage(setupIssue)
			}
			if client != nil {
				client.SetMemoryInvalidator(func() {
					d.app.Sync(func() {
						d.invalidateMemoryViews()
						d.app.RequestUpdate()
					})
				})
			}
			d.watchCaptureChanges(done)
			d.watchDesktopReactivation(done)
			d.startUISnapshotIfRequested()
			d.updateMenuBarCaptureState()
			d.applyQuickAskHotkey()
			installDesktopStatusItem(app, d.ensurePauseScheduler(), d.preferences.MenuBarItemEnabled, d.openQuickPanel, d.openSettingsFromMenu, d.toggleCaptureFromMenu, d.onNotchHover)
			if d.preferences.CaptureEnabled {
				d.startCaptureOnLaunch()
			}
		}
		return d.view()
	})
}

func (d *desktop) watchCaptureChanges(done <-chan struct{}) {
	if d == nil || d.client == nil || done == nil {
		return
	}
	changes := d.client.CaptureChanges()
	captureStateChanges := d.client.CaptureStateChanges()
	memoryChanges := d.client.MemoryChanges()
	safariChanges := d.client.SafariCaptureChanges()
	controllerChanges := (<-chan struct{})(nil)
	if d.capture != nil {
		controllerChanges = d.capture.Changes()
	}
	if changes == nil && captureStateChanges == nil && memoryChanges == nil && safariChanges == nil && controllerChanges == nil {
		return
	}
	go func() {
		for {
			select {
			case <-done:
				return
			case _, ok := <-safariChanges:
				if !ok {
					safariChanges = nil
					continue
				}
				d.app.Post(d.app.RequestUpdate)
			case _, ok := <-controllerChanges:
				if !ok {
					controllerChanges = nil
					continue
				}
				d.app.Post(func() {
					if d.capture != nil && d.capture.UnexpectedExit() && !d.capture.Running() && d.preferences.CaptureEnabled && !d.loading && !d.captureStarting {
						d.captureState = "unavailable"
						d.status = "Capture paused unexpectedly; Blur will retry in the background."
						d.scheduleCaptureRecovery()
						d.updateMenuBarCaptureState()
					}
					d.updateMenuBarCaptureState()
					d.app.RequestUpdate()
				})
			case _, ok := <-captureStateChanges:
				if !ok {
					captureStateChanges = nil
					continue
				}
				d.app.Post(d.updateMenuBarCaptureState)
			case _, ok := <-changes:
				if !ok {
					changes = nil
					continue
				}
				d.app.Post(func() {
					todayDate := time.Now().In(time.Local).Format("2006-01-02")
					running := d.capture != nil && d.capture.Running()
					if shouldRefreshLiveToday(d.page, d.date, todayDate, running) && queueLiveTodayRefresh(d.todayLoading, &d.todayRefreshPending) {
						d.loadToday()
					}
					d.updateMenuBarCaptureState()
				})
			case _, ok := <-memoryChanges:
				if !ok {
					memoryChanges = nil
					continue
				}
				d.app.Post(func() {
					d.invalidateMemoryViews()
					if !d.client.MemoryIsChanging() {
						d.refreshAfterMemoryChange()
					}
					d.app.RequestUpdate()
				})
			}
		}
	}()
}

func captureMenuIconActive(running, appKnown bool, appID string, excludedApps []string) bool {
	if !running || !appKnown || strings.TrimSpace(appID) == "" {
		return false
	}
	appID = strings.ToLower(strings.TrimSpace(appID))
	for _, excluded := range excludedApps {
		if appID == strings.ToLower(strings.TrimSpace(excluded)) {
			return false
		}
	}
	return true
}

func captureAppAllowed(appID string, known bool, includedApps []string) bool {
	if !known || strings.TrimSpace(appID) == "" {
		return false
	}
	appID = strings.ToLower(strings.TrimSpace(appID))
	for _, included := range includedApps {
		if appID == strings.ToLower(strings.TrimSpace(included)) {
			return true
		}
	}
	return false
}

func captureIndicatorPolicyActive(running, appKnown bool, appID string, excludedApps, includedApps []string, defaultDisabled bool) bool {
	if !captureMenuIconActive(running, appKnown, appID, excludedApps) {
		return false
	}
	return !defaultDisabled || captureAppAllowed(appID, appKnown, includedApps)
}

func (d *desktop) updateMenuBarCaptureState() {
	if d == nil {
		return
	}
	appID, known := d.capture.ActiveApplicationID()
	running := d.capture != nil && d.capture.Running()
	active := captureIndicatorPolicyActive(running, known, appID, d.preferences.ExcludedApps, d.preferences.IncludedApps, d.preferences.CaptureDefaultDisabled)
	setDesktopMenuBarCapture(d.pause, running, active)
}

func shouldRefreshLiveToday(page, selectedDate, todayDate string, captureRunning bool) bool {
	return page == "Today" && selectedDate == todayDate && captureRunning
}

func queueLiveTodayRefresh(loading bool, pending *bool) bool {
	if loading {
		if pending != nil {
			*pending = true
		}
		return false
	}
	return true
}

func consumeLiveTodayRefresh(pending *bool) bool {
	if pending == nil || !*pending {
		return false
	}
	*pending = false
	return true
}

type desktop struct {
	pause                         *pauseScheduler
	sync                          *syncSettingsState
	app                           ui.App
	client                        *Client
	page                          string
	dashboardOpen                 bool
	quickPanelMounted             bool
	date                          string
	memoryGeneration              uint64
	dateDraft                     string
	tokens                        palette
	lightDrawn                    captureLightState
	reportStart                   string
	reportEnd                     string
	reportZone                    string
	reportCustom                  bool
	privacyDetails                map[string]bool
	settingsGroup                 string
	report                        activity.ReportResult
	reportReady                   bool
	note                          string
	quickInput                    string
	quickHistory                  quickEntryHistory
	quickNotePending              bool
	quickAskPending               bool
	quickPreview                  bool
	quickPanelPinned              bool
	quickFocusWatching            bool
	quickPanelPointerArmed        bool
	quickPanelPointerWidget       gui.Widget
	quickPanelPointerController   *gui.MotionEventController
	quickHoverEpoch               uint64
	quickUI                       quickPanelExtras
	quickConversation             []quickConversationTurn
	quickNoteExpanded             bool
	query                         string
	modelEndpoint                 string
	modelName                     string
	modelProvider                 string
	modelCredential               string
	remoteAskEnabled              bool
	status                        string
	loading                       bool
	libraryLoading                bool
	libraryLoaded                 bool
	library                       knowledge.Page
	addingKnowledge               bool
	knowledgeSaving               bool
	knowledgeDeletingID           string
	pendingDeleteKnowledge        string
	pendingUnmergeKnowledge       string
	knowledgeKind                 knowledge.Kind
	knowledgeName                 string
	knowledgeNote                 string
	justAdded                     *knowledge.Entity
	today                         TodayView
	todayLoading                  bool
	todayRefreshPending           bool
	todayRequestID                uint64
	hourlySummaries               []chronicle.Summary
	hourlyLoading                 bool
	hourlyError                   bool
	chronicleSourcesRequestID     uint64
	hourlyRequestID               uint64
	expandedChronicleID           string
	chronicleSources              []SearchHit
	chronicleSourcesNext          string
	chronicleSourcesLoading       bool
	knowledgeProposals            []derive.Proposal
	knowledgeProposalHourID       string
	knowledgeSuggesting           bool
	knowledgeSuggestRequest       uint64
	knowledgeSuggestCancel        context.CancelFunc
	knowledgeSuggestStatus        string
	knowledgeAccepting            bool
	searchHits                    []SearchHit
	expandedSourceID              string
	expandedAnswer                bool
	searchComplete                bool
	searchFailed                  bool
	askQuestion                   string
	askResult                     *assistant.Result
	askLoading                    bool
	askRequestID                  uint64
	askCancel                     context.CancelFunc
	answers                       map[string]string
	pendingDelete                 string
	pendingDeleteAll              bool
	retentionDays                 int
	retentionDraft                int
	pendingRetention              bool
	retentionError                bool
	retentionUnknown              bool
	powerConfig                   power.Config
	powerDraftMode                power.Mode
	powerStatus                   string
	powerChecking                 bool
	powerSaving                   bool
	powerNeedsRepair              bool
	powerJobsDraft                string
	powerTokensDraft              string
	powerSecondsDraft             string
	capture                       *ingest.Controller
	captureState                  string
	localAIEnabled                bool
	backgroundSummaries           bool
	aiNeedsRepair                 bool
	preferences                   preferences.Config
	captureDefaultDisabledDraft   bool
	preferencesNeedsRepair        bool
	mcpStatus                     string
	excludedAppsDraft             string
	excludedDomainsDraft          string
	includedAppsDraft             string
	includedDomainsDraft          string
	exclusionValidation           string
	captureStarting               bool
	captureStartedAt              time.Time
	captureRecoveryAttempts       uint8
	captureRecoveryScheduled      bool
	setupRequired                 bool
	permissionWatchStop           chan struct{}
	permissionWatchActive         bool
	permissionActivationAvailable bool
	captureSetupDone              <-chan struct{}
	startupIssue                  string
}

func newDesktop(app ui.App, client *Client, captureController *ingest.Controller) *desktop {
	localDate := time.Now().In(time.Local).Format("2006-01-02")
	prefs := preferences.DefaultConfig()
	if client == nil {
		prefs.CaptureEnabled = false
	}
	d := &desktop{app: app, client: client, capture: captureController, preferences: prefs, page: "Today", settingsGroup: settingsGroupCapture, date: localDate, dateDraft: localDate, reportStart: localDate, reportEnd: localDate, reportZone: localZoneName(), answers: make(map[string]string), knowledgeKind: knowledge.Project}
	if client != nil && captureController != nil {
		client.SetCaptureStateReader(func() (bool, time.Time) { return captureController.Running(), time.Time{} })
	}
	if client == nil || client.data == nil {
		d.status = "Encrypted local storage is unavailable. Blur has not started capture or saved anything."
		return d
	}
	if saved, err := client.ApplicationPreferences(context.Background()); err != nil {
		d.preferences.CaptureEnabled = false
		d.preferencesNeedsRepair = true
	} else {
		d.preferences = saved
	}
	if d.preferences.ClaudeMCPEnabled {
		if err := client.RestoreClaudeMCP(context.Background(), true); err != nil {
			d.mcpStatus = "Claude integration is enabled, but the local bridge could not start. Check Keychain access and try again."
		} else {
			d.mcpStatus = "Claude can search Blur memory through the read-only local bridge."
		}
	}
	d.excludedAppsDraft = strings.Join(d.preferences.ExcludedApps, "\n")
	d.excludedDomainsDraft = strings.Join(d.preferences.ExcludedDomains, "\n")
	d.includedAppsDraft = strings.Join(d.preferences.IncludedApps, "\n")
	d.includedDomainsDraft = strings.Join(d.preferences.IncludedDomains, "\n")
	d.captureDefaultDisabledDraft = d.preferences.CaptureDefaultDisabled
	settings := client.LocalAISettings()
	d.modelEndpoint, d.modelName = settings.Endpoint, settings.Model
	d.modelProvider = effectiveProvider(settings)
	d.remoteAskEnabled = settings.RemoteAskEnabled
	if d.modelEndpoint == "" {
		d.modelEndpoint = "http://127.0.0.1:11434"
	}
	if d.modelProvider == ProviderOpenAICompatible && settings.Endpoint == "" {
		d.modelEndpoint = "https://api.openai.com/v1"
	}
	d.localAIEnabled, d.aiNeedsRepair = client.LocalAIEnabled(), client.LocalAISettingsNeedRepair()
	d.backgroundSummaries = settings.BackgroundSummaries
	d.powerConfig = power.DefaultConfig()
	if client.CanConfigurePower() {
		if config, err := client.PowerConfig(context.Background()); err == nil {
			d.powerConfig = config
		} else {
			d.powerNeedsRepair = true
		}
	}
	d.powerDraftMode = d.powerConfig.Mode
	d.powerJobsDraft, d.powerTokensDraft, d.powerSecondsDraft = powerBudgetDraftValues(d.powerConfig)
	d.retentionError = client.retentionStartupFailed
	if client.CanConfigureRetention() {
		policy, err := client.RetentionPolicy(context.Background())
		if err != nil {
			d.retentionError, d.retentionUnknown = true, true
		} else {
			d.retentionDays, d.retentionDraft = policy.Days, policy.Days
		}
	}
	d.status = "Loading your local day…"
	d.loadToday()
	return d
}

func (d *desktop) view() ui.RootView {
	settings := d.app.Settings()
	options := modern.Options{Dark: settings.ColorScheme() == ui.ColorSchemeDark, AccentColor: settings.AccentColor(), FontFamily: settings.FontFamily(), FontSize: settings.FontSize()}
	d.tokens = themePalette(options.Dark)
	rules := themeRules(options)
	mainWindow := ui.Window("main").Title("Blur").Size(1040, 740).MinSize(800, 560).Center(true).Chrome(ui.WindowChromeNative).Content(
		ui.HBox(d.sidebar(), d.contentColumn()).CrossAlign(layout.CrossStretch),
	)
	mainWindow = addDesktopBackgroundClose(mainWindow, d.app, "main", func() { d.collapseTransientContent("main") })
	mainWindow = addDesktopQuitShortcut(mainWindow, d.app)
	quickWindow := d.quickWindowView()
	var windows []ui.WindowView
	for _, id := range quickPanelWindowIDs(d.dashboardOpen, d.quickPanelMounted) {
		switch id {
		case "quick":
			windows = append(windows, quickWindow)
		case "main":
			windows = append(windows, mainWindow)
		}
	}
	return ui.Root().StyleSheet(style.Sheet(rules...)).Windows(windows...)
}

// contentColumn is the page column: left aligned, never wider than a
// comfortable text measure. The header row carries the measure as its minimum
// width so the column hugs it instead of collapsing to its text.
func (d *desktop) contentColumn() ui.View {
	return ui.VBox(d.header(), d.content()).Spacing(24).
		Paddings(geometry.Insets{Left: contentPadding, Top: contentPadding, Right: contentPadding, Bottom: contentPadding - 8}).
		CrossAlign(layout.CrossStretch).MaxWidth(contentMaxText + 2*contentPadding)
}

func desktopPages() []string {
	return []string{"Today", "Ask", "Search", "Library", "Time", "Settings"}
}

func (d *desktop) captureLightState() captureLightState {
	switch {
	case d.capture != nil && d.capture.Running():
		return captureLightOn
	case d.captureStarting || d.preferences.CaptureEnabled:
		return captureLightPending
	}
	return captureLightPaused
}

func (d *desktop) sidebar() ui.View {
	rows := []ui.View{
		ui.VBox(wrapLabel("Blur").Style(styleBrand)).Paddings(geometry.Insets{Left: navMarkerWidth + navTextInset, Bottom: 22}),
	}
	for _, name := range desktopPages() {
		page := name
		marker, textStyle, buttonStyle := styleNavMarker, styleNavText, styleNav
		if page == d.page {
			marker, textStyle, buttonStyle = styleNavMarkerOn, styleNavTextCurrent, styleNavSelected
		}
		button := ui.Button().Content(wrapLabel(name).Style(textStyle).MinWidth(railWidth - navMarkerWidth - navTextInset - navTextTrailing)).
			Style(buttonStyle).
			Paddings(geometry.Insets{Left: navTextInset, Right: navTextTrailing}).
			MinHeight(navRowHeight).MainWeight(1).
			OnClick(func() {
				d.page, d.status = page, ""
				if page == "Settings" && d.powerStatus == "" && !d.powerChecking {
					d.refreshPowerState()
				}
				if page == "Library" && !d.libraryLoaded && !d.libraryLoading {
					d.loadLibrary()
				}
				d.app.RequestUpdate()
			})
		rows = append(rows, ui.HBox(ui.VBox().Style(marker).MinSize(navMarkerWidth, navRowHeight).MaxSize(navMarkerWidth, navRowHeight), button).CrossAlign(layout.CrossStretch))
	}
	rows = append(rows, wrapLabel("").MainWeight(1))
	if d.capture == nil {
		rows = append(rows, ui.VBox(wrapLabel("Storage unavailable").Style(modern.MutedText)).Paddings(geometry.Insets{Left: navMarkerWidth + navTextInset, Right: 16, Bottom: 4}))
	} else {
		state := d.captureLightState()
		rows = append(rows, ui.VBox(
			ui.HBox(
				captureLight(d.tokens, state, &d.lightDrawn),
				wrapLabel(captureLightLabel(state, d.captureStarting)),
			).Spacing(10).CrossAlign(layout.CrossCenter),
			flatButton(captureActionLabel(d.capture.Running())).Enabled(!d.loading).OnClick(d.toggleCapture),
		).Spacing(8).CrossAlign(layout.CrossStart).Paddings(geometry.Insets{Left: navMarkerWidth + navTextInset - 12, Right: 16}))
	}
	return ui.VBox(rows...).Spacing(2).Paddings(geometry.Insets{Top: 28, Bottom: 22}).CrossAlign(layout.CrossStretch).
		Style(styleRail).MinWidth(railWidth).MaxWidth(railWidth)
}

func captureActionLabel(running bool) string {
	if running {
		return "Pause capture"
	}
	return "Start capture"
}

func (d *desktop) header() ui.View {
	var title ui.View = wrapLabel(d.page).Style(stylePageTitle)
	actions := []ui.View{}
	if d.page == "Today" {
		browse := d.client != nil && !d.loading
		title = ui.HBox(
			arrowButton("‹").Enabled(browse).OnClick(func() { d.shiftDay(-1) }).MinSize(controlHeight, controlHeight),
			wrapLabel(selectedDayLabel(d.date)).Style(styleDayTitle),
			arrowButton("›").Enabled(browse && !d.showingToday()).OnClick(func() { d.shiftDay(1) }).MinSize(controlHeight, controlHeight),
		).Spacing(6).CrossAlign(layout.CrossCenter)
		if !d.showingToday() {
			title = ui.HBox(title, flatButton("Today").Enabled(browse).OnClick(d.goToToday)).Spacing(6).CrossAlign(layout.CrossCenter)
		}
	}
	actions = append(actions, flatButton("Quick ask").OnClick(d.openQuickPanel))
	return ui.HBox(
		ui.VBox(title, wrapLabel(d.subtitle()).Style(styleMeta)).Spacing(6).CrossAlign(layout.CrossStart).MainWeight(1),
		ui.HBox(actions...).Spacing(4).CrossAlign(layout.CrossCenter),
	).CrossAlign(layout.CrossStart).MinWidth(contentMaxText)
}

func (d *desktop) subtitle() string {
	switch d.page {
	case "Today":
		return "A private recap, with next steps you can review."
	case "Search":
		return "Find an exact match in your local memory."
	case "Ask":
		return "Ask a question about what you have chosen to remember."
	case "Library":
		return "People, projects, and the evidence behind them."
	case "Time":
		return reportZoneLine(d.reportZone)
	case "Settings":
		return "Capture, AI, connections, and memory."
	default:
		return ""
	}
}

func (d *desktop) content() ui.View {
	switch d.page {
	case "Today":
		return d.todayView()
	case "Ask":
		return d.askView()
	case "Search":
		return d.searchView()
	case "Library":
		return d.libraryView()
	case "Time":
		return d.timeReportView()
	default:
		return d.settingsView()
	}
}

func askProcessingDisclosure(settings LocalAISettings) string {
	if effectiveProvider(settings) == ProviderOpenAICompatible {
		if settings.RemoteAskEnabled {
			return "When you ask, Blur searches matching local sources and sends your question plus up to eight bounded matches to the configured OpenAI-compatible endpoint. Blur sends only your question when no sources match. The provider may retain requests."
		}
		return "Ask searches matching local sources. Your question and any matches stay on this device unless you separately allow sending them to the configured remote endpoint."
	}
	return "When you ask, Blur searches matching local sources and sends your question plus any bounded matches to Ollama on this device. Blur sends only your question when no sources match. Captured text is not sent to remote providers."
}

// askModelLastCopy states the retrieval principle: local first, model last.
const askModelLastCopy = "Blur reads your memory locally first. Only the few items it picks for a question go to the model you configured, never your whole memory."

func (d *desktop) askView() ui.View {
	settings := LocalAISettings{}
	if d.client != nil {
		settings = d.client.LocalAISettings()
	}
	rows := []ui.View{wrapLabel(askProcessingDisclosure(settings)).Style(modern.MutedText), wrapLabel(askModelLastCopy).Style(modern.MutedText)}
	if d.client == nil || !d.client.CanAskMemory() {
		rows = append(rows, surfaceBox(12, 0, ui.HBox(
			ui.TextInput().Placeholder("Configure Ollama in Settings to ask").MainWeight(1).Enabled(false),
			primaryButton("Ask").Enabled(false),
		).Spacing(10).CrossAlign(layout.CrossCenter)))
		message := "To ask about local memory, configure Ollama on this device in Settings."
		if settings.Enabled && effectiveProvider(settings) == ProviderOpenAICompatible {
			if settings.RemoteAskEnabled {
				message = "The configured provider is not ready for Ask. Review its endpoint and credentials in Settings."
			} else {
				message = "Remote Ask is off. Allow it in Settings to send matching captured sources to the configured endpoint, or switch to local Ollama."
			}
		} else if d.client != nil && d.client.LocalAISettingsNeedRepair() {
			message = "The local model settings need attention. Review AI settings to reconnect Ollama."
		}
		rows = append(rows,
			wrapLabel(message).Style(modern.MutedText),
			flatButton("Open AI settings").OnClick(func() { d.openSettings(settingsGroupAI) }),
		)
		return pageScroll(ui.VBox(hugButtons(rows)...).Spacing(16).CrossAlign(layout.CrossStretch))
	}
	memoryChanging := d.client.MemoryIsChanging()
	rows = append(rows, surfaceBox(12, 0, ui.HBox(
		ui.TextInput().Text(d.askQuestion).MainWeight(1).Placeholder("What did we decide about the launch?").Enabled(askInputEnabled(d.askLoading || d.loading, memoryChanging)).OnText(func(value string) {
			d.askQuestion = value
			d.askResult = nil
			d.expandedAnswer = false
			d.status = ""
			d.app.RequestUpdate()
		}).OnSubmit(d.askMemory),
		primaryButton("Ask").Enabled(askSubmitEnabled(d.askLoading || d.loading, memoryChanging, d.askQuestion)).OnClick(d.askMemory),
	).Spacing(10).CrossAlign(layout.CrossCenter)))
	if memoryChanging {
		rows = append(rows, wrapLabel("Memory pruning… Ask will return when it’s done.").Style(modern.MutedText))
	}
	if d.askLoading {
		rows = append(rows,
			wrapLabel(askProgressLabel(d.client.LocalAISettings(), d.askQuestion)).Style(modern.MutedText),
			flatButton("Cancel Ask").OnClick(d.cancelAsk),
		)
	}
	if d.status != "" {
		rows = append(rows, wrapLabel(d.status).Style(modern.MutedText))
	}
	if result := d.askResult; result != nil {
		if result.Answer.InsufficientEvidence {
			rows = append(rows, wrapLabel("There isn’t enough matching evidence to answer that.").Style(modern.MutedText))
		} else {
			if result.General {
				rows = append(rows, wrapLabel("General answer. No saved memory was sent.").Style(modern.MutedText))
			}
			rows = append(rows, surfaceBox(16, 10, answerViews(result.Answer.Text, d.expandedAnswer, func() {
				d.expandedAnswer = !d.expandedAnswer
				d.app.RequestUpdate()
			}, "")...))
			if line := answerSentLine(result.General, result.Direct, result.SentItems, len(result.Sources)); line != "" && !result.General {
				rows = append(rows, wrapLabel(line).Style(modern.MutedText))
			}
		}
		var sourceBlocks []ui.View
		for _, source := range result.Sources {
			preview, truncated := searchPreview(source.Text)
			content := preview
			if d.expandedSourceID == source.SourceID {
				content = source.Text
			}
			sourceRows := []ui.View{
				wrapLabel(content),
				wrapLabel(strings.Trim(strings.Join([]string{source.AppID, source.Domain}, ", "), ", ")).Style(styleMeta),
			}
			if truncated {
				label := "Show full source"
				if d.expandedSourceID == source.SourceID {
					label = "Show preview"
				}
				id := source.SourceID
				sourceRows = append(sourceRows, flatButton(label).OnClick(func() {
					if d.expandedSourceID == id {
						d.expandedSourceID = ""
					} else {
						d.expandedSourceID = id
					}
					d.app.RequestUpdate()
				}))
			}
			sourceBlocks = append(sourceBlocks, ui.VBox(sourceRows...).Spacing(6).CrossAlign(layout.CrossStretch))
		}
		if len(sourceBlocks) > 0 {
			rows = append(rows, hairline(), section("Sources", sourceBlocks...))
		}
	}
	if d.askResult == nil && !d.askLoading && d.status == "" {
		rows = append(rows, wrapLabel("Questions are processed only after you ask. Answers include the local sources they used.").Style(modern.MutedText))
		rows = append(rows, wrapLabel("Questions about your day, meetings, pages, or notes search your memory. Other questions get a general answer.").Style(modern.MutedText))
	}
	return pageScroll(ui.VBox(hugButtons(rows)...).Spacing(14).CrossAlign(layout.CrossStretch))
}

func askProgressLabel(settings LocalAISettings, question string) string {
	remote := effectiveProvider(settings) == ProviderOpenAICompatible && settings.RemoteAskEnabled
	searches := assistant.QuestionSeeksPersonalMemory(question)
	switch {
	case remote && searches:
		return "Searching local memory and asking the configured provider…"
	case remote:
		return "Asking the configured provider…"
	case searches:
		return "Searching local memory and asking Ollama…"
	}
	return "Asking Ollama…"
}

func askInputEnabled(loading, memoryChanging bool) bool {
	return !loading && !memoryChanging
}

func askSubmitEnabled(loading, memoryChanging bool, question string) bool {
	return askInputEnabled(loading, memoryChanging) && strings.TrimSpace(question) != ""
}

func (d *desktop) askMemory() {
	if d == nil || d.loading || d.askLoading || d.client == nil || d.client.MemoryIsChanging() || !d.client.CanAskMemory() {
		return
	}
	d.askRequestID++
	requestID := d.askRequestID
	question := d.askQuestion
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	d.askCancel = cancel
	d.askLoading, d.status, d.askResult = true, "", nil
	d.expandedSourceID = ""
	d.expandedAnswer = false
	d.resizeQuickPanel(false)
	d.app.RequestUpdate()
	go func() {
		var history []assistant.Turn
		if d.quickAskPending {
			history = quickHistoryTurns(d.quickConversation)
		}
		result, err := d.client.AskMemoryWithHistory(ctx, question, history)
		cancel()
		d.app.Post(func() {
			if !askRequestCurrent(requestID, d.askRequestID) {
				return
			}
			d.askCancel = nil
			d.askLoading = false
			if err != nil {
				d.status = askErrorMessage(err, effectiveProvider(d.client.LocalAISettings()))
				if d.quickAskPending {
					d.quickInput = d.askQuestion
					d.quickAskPending = false
				}
				d.resizeQuickPanel(d.quickPanelExpanded())
			} else {
				d.askResult = &result
				if d.quickAskPending {
					d.quickConversation = appendQuickConversation(d.quickConversation, d.askQuestion, result)
					d.quickAskPending = false
					d.quickInput = ""
				}
				d.resizeQuickPanel(d.quickPanelExpanded())
			}
			d.app.RequestUpdate()
		})
	}()
}

func askRequestCurrent(requestID, latestRequestID uint64) bool {
	return requestID != 0 && requestID == latestRequestID
}

func (d *desktop) invalidateAsk() {
	if d == nil {
		return
	}
	d.askRequestID++
	if d.askCancel != nil {
		d.askCancel()
		d.askCancel = nil
	}
	if d.quickAskPending {
		d.quickInput = d.askQuestion
		d.quickAskPending = false
	}
	d.askLoading = false
	d.askResult = nil
	d.expandedSourceID = ""
	d.expandedAnswer = false
}

func (d *desktop) cancelAsk() {
	if d == nil || !d.askLoading {
		return
	}
	d.invalidateAsk()
	d.status = "Ask cancelled."
	d.resizeQuickPanel(false)
	d.app.RequestUpdate()
}

func askErrorMessage(err error, configuredProvider ...string) string {
	remote := len(configuredProvider) > 0 && configuredProvider[0] == ProviderOpenAICompatible
	switch {
	case errors.Is(err, ErrMemoryChanging):
		return "Memory is being pruned. Try your question again in a moment."
	case errors.Is(err, assistant.ErrContext):
		return "No matching local sources were found. Try a different question."
	case errors.Is(err, assistant.ErrTokenBudget):
		return "That question and its matching evidence are too large. Try a shorter question."
	case errors.Is(err, model.ErrOllamaUnavailable):
		return "Ollama is unavailable. Make sure it’s running and the selected model is installed in Settings."
	case errors.Is(err, model.ErrOllamaResponse), errors.Is(err, model.ErrCompatibleResponse), errors.Is(err, assistant.ErrGeneratorResponse), errors.Is(err, assistant.ErrAnswer):
		if remote {
			return "The configured provider returned an answer Blur couldn’t verify. Try again or choose another model."
		}
		return "Ollama returned an answer Blur couldn’t verify. Try a different local model or update it."
	case errors.Is(err, model.ErrCompatibleUnavailable):
		return "The configured AI endpoint is unavailable. Check its address and connection in Settings."
	case errors.Is(err, context.DeadlineExceeded):
		if remote {
			return "The configured provider took too long to answer. Check the endpoint in Settings, then try again."
		}
		return "Ollama took too long to answer. Check that the selected model is loaded, then try again."
	default:
		if remote {
			return "Could not answer from local memory. Check the configured provider in Settings or try a shorter question."
		}
		return "Could not answer from local memory. Try a shorter question or check Ollama in Settings."
	}
}

func askResultUsesSource(result *assistant.Result, sourceID string) bool {
	if result == nil || sourceID == "" {
		return false
	}
	for _, source := range result.Sources {
		if source.SourceID == sourceID {
			return true
		}
	}
	return false
}

func (d *desktop) timeReportView() ui.View {
	rangeValid := validActivityReportRange(d.reportStart, d.reportEnd)
	children := []ui.View{d.reportPresetRow(time.Now())}
	if d.reportCustom {
		children = append(children, ui.HBox(
			ui.TextInput().Text(d.reportStart).Placeholder("From (YYYY-MM-DD)").MainWeight(1).Enabled(!d.loading).OnText(func(value string) {
				d.reportStart, d.status, d.reportReady = value, "", false
				d.report = activity.ReportResult{}
				d.app.RequestUpdate()
			}),
			ui.TextInput().Text(d.reportEnd).Placeholder("To (YYYY-MM-DD)").MainWeight(1).Enabled(!d.loading).OnText(func(value string) {
				d.reportEnd, d.status, d.reportReady = value, "", false
				d.report = activity.ReportResult{}
				d.app.RequestUpdate()
			}),
			primaryButton("Show report").Enabled(!d.loading && rangeValid).OnClick(d.loadActivityReport),
		).Spacing(10).CrossAlign(layout.CrossCenter))
	}
	if d.reportCustom && !rangeValid {
		children = append(children, wrapLabel(activityReportRangeHint(d.reportStart, d.reportEnd)).Style(modern.MutedText))
	}
	if d.loading && d.status != "" {
		children = append(children, wrapLabel(d.status).Style(modern.MutedText))
	}
	if d.reportReady {
		children = append(children, hairline(),
			ui.VBox(wrapLabel(formatReportDuration(d.report.Eligible)).Style(styleDayTitle), wrapLabel("tracked across the selected dates").Style(modern.MutedText)).Spacing(2).CrossAlign(layout.CrossStart),
		)
		var byApp []ui.View
		if len(d.report.Apps) == 0 {
			byApp = append(byApp, wrapLabel("No activity was recorded in this date range.").Style(modern.MutedText))
		}
		var largest int64
		for _, bucket := range d.report.Apps {
			largest = max(largest, bucket.Duration)
		}
		for _, bucket := range d.report.Apps {
			name := bucket.Key
			if name == "" {
				name = "Unclassified app"
			}
			byApp = append(byApp, d.reportAppRow(name, bucket.Duration, largest))
		}
		children = append(children, hairline(), section("By app", byApp...))
	}
	if !d.reportReady && !d.loading && d.status == "" && (!d.reportCustom || rangeValid) {
		children = append(children, recapSectionTitle("Choose a range to see where your time went."))
	}
	if !d.loading && d.status != "" {
		children = append(children, wrapLabel(d.status).Style(modern.MutedText))
	}
	return pageScroll(ui.VBox(hugButtons(children)...).Spacing(20).CrossAlign(layout.CrossStretch))
}

func validActivityReportRange(startDate, endDate string) bool {
	if !validDate(startDate) || !validDate(endDate) {
		return false
	}
	start, _ := time.Parse("2006-01-02", startDate)
	end, _ := time.Parse("2006-01-02", endDate)
	days := int(end.Sub(start)/(24*time.Hour)) + 1
	return days > 0 && days <= MaxActivityReportDays
}

func activityReportRangeHint(startDate, endDate string) string {
	if !validDate(startDate) || !validDate(endDate) {
		return "Enter real dates in YYYY-MM-DD format."
	}
	start, _ := time.Parse("2006-01-02", startDate)
	end, _ := time.Parse("2006-01-02", endDate)
	if end.Before(start) {
		return "The end date must be on or after the start date."
	}
	return fmt.Sprintf("Choose a range of %d days or less.", MaxActivityReportDays)
}

func (d *desktop) loadActivityReport() {
	if d.loading {
		return
	}
	if !validActivityReportRange(d.reportStart, d.reportEnd) {
		d.reportReady = false
		d.report = activity.ReportResult{}
		d.status = activityReportRangeHint(d.reportStart, d.reportEnd)
		d.app.RequestUpdate()
		return
	}
	if d.client == nil || d.client.data == nil {
		d.status = "Encrypted local storage is not set up."
		d.app.RequestUpdate()
		return
	}
	d.loading, d.status = true, "Reading the selected dates locally…"
	d.reportReady = false
	d.app.RequestUpdate()
	start, end, zone := d.reportStart, d.reportEnd, d.reportZone
	memoryGeneration := d.memoryGeneration
	memoryEpoch, stable := d.client.BeginMemoryRead()
	if !stable {
		d.loading, d.status = false, ""
		return
	}
	go func() {
		report, err := d.client.ActivityReport(start, end, zone)
		d.app.Post(func() {
			if !currentMemoryRead(memoryGeneration, d.memoryGeneration) {
				d.loading = false
				d.app.RequestUpdate()
				return
			}
			if !d.client.PublishMemoryRead(memoryEpoch, func() {
				d.loading = false
				if err != nil {
					d.status = friendlyError(err)
				} else {
					d.report, d.reportReady, d.status = report, true, ""
				}
			}) {
				d.loading = false
				d.app.RequestUpdate()
				return
			}
			d.app.RequestUpdate()
		})
	}()
}

func formatReportDuration(nanoseconds int64) string {
	if nanoseconds <= 0 {
		return "0m"
	}
	minutes := int64(time.Duration(nanoseconds) / time.Minute)
	if minutes == 0 {
		return "<1m"
	}
	if minutes < 60 {
		return fmt.Sprintf("%dm", minutes)
	}
	return fmt.Sprintf("%dh %02dm", minutes/60, minutes%60)
}

func (d *desktop) todayView() ui.View {
	showQuickNote := d.client != nil && d.client.CanAddNote()
	var quickNote ui.View
	if showQuickNote {
		quickNote = d.quickNoteCard()
	}
	children := todayContentViews(nil, quickNote, nil, showQuickNote)
	if hourView := d.hourSoFarView(); hourSoFarHasLines(d) {
		children = append(children, hourView)
	}
	var blocks []ui.View
	if d.todayLoading {
		blocks = append(blocks, wrapLabel("Loading this day…").Style(modern.MutedText))
	} else if d.today.State == "ready" {
		if todos := d.today.Snapshot.ConfirmedTodos; len(todos) > 0 {
			rows := make([]ui.View, 0, len(todos))
			for _, task := range todos {
				rows = append(rows, wrapLabel(task.Text))
			}
			blocks = append(blocks, section("On your list", rows...))
		}
		if candidates := d.today.Snapshot.TodoCandidates; len(candidates) > 0 {
			rows := make([]ui.View, 0, len(candidates))
			for _, task := range candidates {
				candidate := task
				rows = append(rows, ui.HBox(
					wrapLabel(candidate.Text).MainWeight(1),
					flatButton("Add to Today").Enabled(!d.loading).OnClick(func() { d.confirm(candidate.ID) }),
				).Spacing(10).CrossAlign(layout.CrossCenter))
			}
			blocks = append(blocks, section("Suggested tasks", rows...))
		}
		if loopLines := openLoopLines(d.today.Snapshot.OpenLoops); len(loopLines) > 0 {
			rows := make([]ui.View, 0, len(loopLines))
			for _, line := range loopLines {
				rows = append(rows, wrapLabel(line))
			}
			blocks = append(blocks, section("Open loops", rows...))
		}
		for _, question := range d.today.Snapshot.Questions {
			if question.Resolved {
				continue
			}
			blocks = append(blocks, d.questionBlock(question))
		}
		if !todayHasUsefulItems(d.today.Snapshot) && len(d.hourlySummaries) == 0 {
			blocks = append(blocks, wrapLabel("No open items or useful recap for this day yet.").Style(modern.MutedText))
		}
	} else if d.client == nil || d.client.data == nil {
		blocks = append(blocks, wrapLabel("Encrypted local storage is unavailable. Capture and notes stay disabled until storage is ready.").Style(modern.MutedText))
	} else {
		captureRunning := d.capture != nil && d.capture.Running()
		if shouldOfferCaptureSetup(d.today.State, d.capture != nil, captureRunning) {
			blocks = append(blocks, section("Your recap starts here",
				wrapLabel("Start capture when you’re ready. Blur only reads the focused app, and you can pause anytime.").Style(modern.MutedText),
				hug(primaryButton("Set up capture").OnClick(func() { d.openSettings(settingsGroupCapture) })),
			))
		} else {
			blocks = append(blocks, wrapLabel("No recap for "+selectedDayLabel(d.date)+" yet. It appears as approved activity is captured.").Style(modern.MutedText))
		}
	}
	if d.client != nil && d.client.CanViewChronicles() {
		blocks = append(blocks, d.hourlyChroniclesView())
	}
	if d.loading || d.status != "" {
		blocks = append(blocks, wrapLabel(d.status).Style(modern.MutedText))
	}
	children = append(children, blocks...)
	return pageScroll(sections(children...))
}

func todayHasUsefulItems(snapshot today.Snapshot) bool {
	for _, question := range snapshot.Questions {
		if !question.Resolved {
			return true
		}
	}
	return len(snapshot.ConfirmedTodos)+len(snapshot.TodoCandidates)+len(snapshot.OpenLoops) > 0
}

func todayContentViews(dateControls, quickNote ui.View, recap []ui.View, showQuickNote bool) []ui.View {
	children := make([]ui.View, 0, 2+len(recap))
	if dateControls != nil {
		children = append(children, dateControls)
	}
	if showQuickNote {
		children = append(children, quickNote)
	}
	return append(children, recap...)
}

func (d *desktop) quickNoteCard() ui.View {
	hasDraft := strings.TrimSpace(d.note) != ""
	if !d.quickNoteExpanded {
		label := "Quick note"
		if hasDraft {
			label = "Unsaved note"
		}
		return ui.HBox(wrapLabel(label).Style(styleSectionTitle).MainWeight(1), flatButton(quickNoteDisclosureLabel(false, hasDraft)).OnClick(func() {
			d.quickNoteExpanded = true
			d.app.RequestUpdate()
		})).CrossAlign(layout.CrossCenter)
	}
	return surfaceBox(16, 10,
		ui.HBox(
			wrapLabel("Quick note").Style(styleSectionTitle).MainWeight(1),
			flatButton(quickNoteDisclosureLabel(true, hasDraft)).OnClick(func() {
				d.quickNoteExpanded = false
				d.app.RequestUpdate()
			}),
		).CrossAlign(layout.CrossCenter),
		quickNoteInput(d.note, func(value string) { d.note = value; d.app.RequestUpdate() }),
		ui.HBox(
			wrapLabel(quickNotePrivacyHint(d.localAIEnabled, d.modelProvider)).Style(styleMeta).MainWeight(1),
			primaryButton("Save note").Enabled(d.client != nil && d.client.CanAddNote() && !d.loading && hasDraft).OnClick(d.addNote),
		).Spacing(10).CrossAlign(layout.CrossCenter),
	)
}

func quickNoteDisclosureLabel(expanded, hasDraft bool) string {
	if expanded {
		return "Hide editor"
	}
	if hasDraft {
		return "Continue draft"
	}
	return "Add a note"
}

func quickNotePrivacyHint(modelEnabled bool, provider string) string {
	if !modelEnabled {
		return "Private note. AI is off."
	}
	if provider == ProviderOpenAICompatible {
		return "Sent to the configured provider. Suggestions need your approval."
	}
	return "Private note. Suggestions stay yours to approve."
}

func recapSectionTitle(text string) ui.View { return wrapLabel(text).Style(styleSectionTitle) }

func quickNoteInput(text string, onText func(string)) ui.View {
	return ui.TextView().Text(text).Placeholder("Add a thought, task, or meeting note…").OnText(onText).
		WrapMode(ui.WrapWordChar).MinHeight(76).MaxHeight(128)
}

func openLoopLines(loops []today.OpenLoop) []string {
	lines := make([]string, 0, len(loops))
	for _, loop := range loops {
		if strings.TrimSpace(loop.Text) != "" {
			lines = append(lines, loop.Text)
		}
	}
	return lines
}

func validSelectedDay(value string) bool { return validDate(value) }

func selectedDayLabel(value string) string {
	date, err := time.Parse("2006-01-02", value)
	if err != nil {
		return "Choose a day"
	}
	return date.Format("Monday, January 2")
}

// shiftedDay returns the calendar date delta days from value.
func shiftedDay(value string, delta int) (string, bool) {
	date, err := time.Parse("2006-01-02", value)
	if err != nil {
		return "", false
	}
	return date.AddDate(0, 0, delta).Format("2006-01-02"), true
}

func localToday() string { return time.Now().In(time.Local).Format("2006-01-02") }

// showingToday reports whether the visible day is the current local day.
func (d *desktop) showingToday() bool { return d.date == localToday() }

// shiftDay steps the visible day by delta days and never past today.
func (d *desktop) shiftDay(delta int) {
	if d == nil || d.loading {
		return
	}
	next, ok := shiftedDay(d.date, delta)
	if !ok || next > localToday() {
		return
	}
	d.dateDraft = next
	d.showSelectedDay()
}

func (d *desktop) goToToday() {
	if d == nil || d.loading {
		return
	}
	d.dateDraft = localToday()
	d.showSelectedDay()
}

func (d *desktop) showSelectedDay() {
	if d == nil || d.loading {
		return
	}
	if !validSelectedDay(d.dateDraft) {
		d.status = "Enter a real calendar date in YYYY-MM-DD format."
		d.app.RequestUpdate()
		return
	}
	previousDate := d.date
	d.date = d.dateDraft
	d.status = "Loading selected day…"
	if !d.loadToday() {
		d.date, d.dateDraft = previousDate, previousDate
		d.status = ""
		d.app.RequestUpdate()
	}
}

// noteAfterSave keeps text typed while a save was in flight; it only clears the
// draft when it still matches what was saved.
func noteAfterSave(current, saved string) string {
	if strings.TrimSpace(current) == saved {
		return ""
	}
	return current
}

func (d *desktop) questionBlock(question today.Question) ui.View {
	return section("Possible follow-up",
		wrapLabel(question.Text),
		wrapLabel(question.Reason).Style(modern.MutedText),
		ui.HBox(
			ui.TextInput().Text(d.answers[question.ID]).Placeholder("Add your answer").OnText(func(value string) { d.answers[question.ID] = value; d.app.RequestUpdate() }).MainWeight(1),
			primaryButton("Answer").Enabled(!d.loading && strings.TrimSpace(d.answers[question.ID]) != "").OnClick(func() { d.answerQuestion(question.ID) }),
			flatButton("Dismiss").Enabled(!d.loading).OnClick(func() { d.dismissQuestion(question.ID) }),
		).Spacing(8).CrossAlign(layout.CrossCenter),
	)
}

func (d *desktop) addNote() {
	text := strings.TrimSpace(d.note)
	if text == "" || d.loading || d.client == nil || !d.client.CanAddNote() {
		return
	}
	isQuickNote := d.quickNotePending
	d.loading, d.status = true, "Saving the note locally…"
	if d.quickNotePending {
		d.resizeQuickPanel(d.askResult != nil)
	}
	d.app.RequestUpdate()
	go func() {
		result, err := d.client.AddNote(text)
		d.app.Post(func() {
			d.loading = false
			if err != nil {
				d.quickNotePending = false
				d.status = friendlyError(err)
			} else {
				d.note = noteAfterSave(d.note, text)
				if isQuickNote {
					d.quickHistory.Record(text)
				}
				d.quickPanelNoteSaved()
				switch result.Processing {
				case notes.ProcessingUnavailable:
					d.status = "Saved. The local model is unavailable; explicit TODOs still work."
				case notes.ProcessingBudget:
					d.status = "Saved. This note was too large for the local model budget and was not sent."
				case notes.ProcessingInvalidOutput:
					d.status = "Saved. Blur ignored an invalid model suggestion."
				case notes.ProcessingModel:
					d.status = "Saved. Review the task and follow-up suggestions below."
				default:
					d.status = "Saved. Add a local model to process ordinary prose."
				}
				d.loadToday()
			}
			if isQuickNote {
				if quickNoteShouldReopenAfterFailure(isQuickNote, err != nil) {
					d.openQuickPanel()
				} else {
					d.dismissQuickPanel()
				}
			}
			d.app.RequestUpdate()
		})
	}()
}

func (d *desktop) confirm(id string) {
	if d.loading || d.client == nil {
		return
	}
	d.loading, d.status = true, "Adding to Today…"
	d.app.RequestUpdate()
	date := d.today.Date
	go func() {
		err := d.client.ConfirmProposal(date, id)
		d.app.Post(func() {
			d.loading = false
			if err != nil {
				d.status = friendlyError(err)
			} else {
				d.status = "Added to Today. No notification was created."
				d.loadToday()
			}
			d.app.RequestUpdate()
		})
	}()
}

func (d *desktop) answerQuestion(id string) {
	if d.loading || d.client == nil {
		return
	}
	answer := strings.TrimSpace(d.answers[id])
	if answer == "" {
		return
	}
	d.loading, d.status = true, "Saving your answer…"
	d.app.RequestUpdate()
	date := d.today.Date
	go func() {
		err := d.client.AnswerQuestion(date, id, answer)
		d.app.Post(func() {
			d.loading = false
			if err != nil {
				d.status = friendlyError(err)
			} else {
				delete(d.answers, id)
				d.status = "Answer saved as a private note."
				d.loadToday()
			}
			d.app.RequestUpdate()
		})
	}()
}

func (d *desktop) dismissQuestion(id string) {
	if d.loading || d.client == nil {
		return
	}
	d.loading, d.status = true, "Dismissing question…"
	d.app.RequestUpdate()
	date := d.today.Date
	go func() {
		err := d.client.DismissQuestion(date, id)
		d.app.Post(func() {
			d.loading = false
			if err != nil {
				d.status = friendlyError(err)
			} else {
				d.status = "Question dismissed."
				d.loadToday()
			}
			d.app.RequestUpdate()
		})
	}()
}

// loadToday starts a read of the selected day and reports whether one began.
func (d *desktop) loadToday() bool {
	if d.client == nil {
		return false
	}
	date := d.date
	memoryGeneration := d.memoryGeneration
	memoryEpoch, stable := d.client.BeginMemoryRead()
	if !stable {
		return false
	}
	d.loadChronicles(date)
	d.todayRequestID++
	requestID := d.todayRequestID
	d.todayLoading = true
	go func() {
		view, err := d.client.Today(date)
		d.app.Post(func() {
			if !currentMemoryRead(memoryGeneration, d.memoryGeneration) || !currentTodayLoad(requestID, d.todayRequestID, date, d.date) || !d.client.PublishMemoryRead(memoryEpoch, func() {
				d.todayLoading = false
				if err != nil {
					d.status = "Today could not be read from encrypted storage."
				} else {
					d.today = view
				}
				if d.status == "Loading your local day…" || d.status == "Loading selected day…" {
					d.status = ""
				}
			}) {
				return
			}
			if consumeLiveTodayRefresh(&d.todayRefreshPending) {
				todayDate := time.Now().In(time.Local).Format("2006-01-02")
				if shouldRefreshLiveToday(d.page, d.date, todayDate, d.capture != nil && d.capture.Running()) {
					d.loadToday()
				}
			}
			d.app.RequestUpdate()
		})
	}()
	return true
}

func (d *desktop) loadChronicles(date string) {
	if d.client == nil || !d.client.CanViewChronicles() {
		d.hourlySummaries = nil
		return
	}
	d.hourlyRequestID++
	requestID := d.hourlyRequestID
	generation := d.memoryGeneration
	epoch, stable := d.client.BeginMemoryRead()
	if !stable {
		return
	}
	d.hourlyLoading, d.hourlyError = true, false
	go func() {
		rows, err := d.client.ChroniclesForDate(context.Background(), date)
		d.app.Post(func() {
			if requestID != d.hourlyRequestID || date != d.date || generation != d.memoryGeneration {
				return
			}
			if !d.client.PublishMemoryRead(epoch, func() {
				d.hourlyLoading = false
				if err != nil {
					d.hourlySummaries, d.hourlyError = nil, true
				} else {
					d.hourlySummaries, d.hourlyError = rows, false
				}
			}) {
				return
			}
			d.app.RequestUpdate()
		})
	}()
}

func (d *desktop) hourlyChroniclesView() ui.View {
	rows := []ui.View{wrapLabel("Hourly recaps").Style(styleSectionTitle)}
	if d.hourlyLoading {
		rows = append(rows, wrapLabel("Checking completed hours…").Style(modern.MutedText))
	}
	if d.hourlyError && !d.hourlyLoading {
		rows = append(rows, wrapLabel("Hourly recaps could not be read. Try again.").Style(modern.MutedText))
	} else if len(d.hourlySummaries) == 0 && !d.hourlyLoading {
		rows = append(rows, wrapLabel("Completed-hour recaps appear here when local summaries are enabled.").Style(modern.MutedText))
	}
	for _, summary := range d.hourlySummaries {
		id := summary.HourID
		card := []ui.View{
			wrapLabel(summary.Start.In(time.Local).Format("3:04 PM") + " – " + summary.End.In(time.Local).Format("3:04 PM")).Style(styleMeta),
			wrapLabel(compactPreviewText(firstRecapSentence(summary.Text), 220)),
			hug(flatButton(chronicleSourcesLabel(len(summary.SourceIDs), d.expandedChronicleID == id, d.chronicleSourcesLoading)).Enabled(!d.chronicleSourcesLoading).OnClick(func() { d.toggleChronicleSources(id) })),
		}
		if d.expandedChronicleID == id {
			if d.chronicleSourcesLoading {
				card = append(card, wrapLabel("Loading cited sources…").Style(modern.MutedText))
			} else {
				card = append(card, d.chronicleSourceList())
			}
			card = append(card, d.chronicleKnowledgeSuggestionsView(id))
		}
		rows = append(rows, ui.VBox(card...).Spacing(4).CrossAlign(layout.CrossStretch))
	}
	return ui.VBox(rows...).Spacing(12).CrossAlign(layout.CrossStretch)
}

func chronicleSourcesLabel(count int, expanded, loading bool) string {
	if loading {
		return "Loading sources…"
	}
	if expanded {
		return "Hide sources"
	}
	return fmt.Sprintf("Review %d sources", count)
}

func (d *desktop) toggleChronicleSources(id string) {
	if d.expandedChronicleID == id {
		d.clearKnowledgeSuggestions()
		d.expandedChronicleID = ""
		d.chronicleSources = nil
		d.chronicleSourcesRequestID++
		d.chronicleSourcesLoading = false
		d.app.RequestUpdate()
		return
	}
	d.clearKnowledgeSuggestions()
	d.chronicleSourcesRequestID++
	d.chronicleSourcesLoading = false
	d.expandedChronicleID = id
	d.chronicleSources = nil
	d.app.RequestUpdate()
	d.chronicleSourcesNext = ""
	d.loadChronicleSourcesPage(id, "", false)
}

func (d *desktop) chronicleKnowledgeSuggestionsView(hourID string) ui.View {
	rows := []ui.View{}
	if d.client != nil && d.client.CanSuggestKnowledge() {
		rows = append(rows,
			wrapLabel("Suggestions use only sources cited by this recap and stay on this device.").Style(modern.MutedText),
			hug(flatButton("Suggest Library facts").Enabled(knowledgeSuggestionActionEnabled(d.client.CanSuggestKnowledge(), d.client.MemoryIsChanging(), d.knowledgeSuggesting || d.knowledgeAccepting)).OnClick(func() { d.suggestKnowledgeFromChronicle(hourID) })),
		)
	} else {
		rows = append(rows, wrapLabel("Library suggestions require local Ollama; recap content is never sent to a remote model.").Style(modern.MutedText))
	}
	if d.knowledgeSuggestStatus != "" {
		rows = append(rows, wrapLabel(d.knowledgeSuggestStatus).Style(modern.MutedText))
	}
	if d.knowledgeSuggesting {
		rows = append(rows, wrapLabel("Reviewing this recap with local Ollama…").Style(modern.MutedText))
	}
	if d.knowledgeProposalHourID == hourID {
		for i, proposal := range d.knowledgeProposals {
			index, item := i, proposal
			sourceLabel := "Cited source"
			for _, source := range d.chronicleSources {
				if source.SourceID == item.SourceID {
					sourceLabel = strings.Trim(strings.Join([]string{source.AppID, source.Domain}, ", "), ", ")
					if sourceLabel == "" {
						sourceLabel = "Cited source"
					}
					break
				}
			}
			rows = append(rows, ui.VBox(
				wrapLabel(item.Name+" ("+string(item.Kind)+")").Style(styleSectionTitle),
				wrapLabel(item.Fact),
				wrapLabel("Source: "+sourceLabel).Style(modern.MutedText),
				ui.HBox(
					primaryButton("Add to Library").Enabled(!d.knowledgeAccepting && !d.client.MemoryIsChanging()).OnClick(func() { d.acceptKnowledgeSuggestion(hourID, index) }),
					flatButton("Dismiss").Enabled(!d.knowledgeAccepting).OnClick(func() { d.dismissKnowledgeSuggestion(index) }),
				).Spacing(8),
			).Spacing(6).Padding(12).Style(modern.Card).CrossAlign(layout.CrossStretch))
		}
	}
	return ui.VBox(rows...).Spacing(8).CrossAlign(layout.CrossStretch)
}

func knowledgeSuggestionCurrent(requestID, currentRequestID uint64, hourID, expandedHourID string, generation, currentGeneration uint64) bool {
	return requestID == currentRequestID && hourID == expandedHourID && generation == currentGeneration
}

func dismissKnowledgeSuggestion(proposals []derive.Proposal, index int) []derive.Proposal {
	if index < 0 || index >= len(proposals) {
		return append([]derive.Proposal(nil), proposals...)
	}
	result := make([]derive.Proposal, 0, len(proposals)-1)
	result = append(result, proposals[:index]...)
	return append(result, proposals[index+1:]...)
}

func knowledgeSuggestionActionEnabled(localModel, memoryChanging, loading bool) bool {
	return localModel && !memoryChanging && !loading
}

func (d *desktop) suggestKnowledgeFromChronicle(hourID string) {
	if d == nil || d.client == nil || d.client.MemoryIsChanging() || !d.client.CanSuggestKnowledge() || d.expandedChronicleID != hourID || d.knowledgeSuggesting {
		return
	}
	d.clearKnowledgeSuggestions()
	d.knowledgeSuggesting = true
	d.knowledgeProposalHourID = hourID
	d.knowledgeSuggestStatus = ""
	d.knowledgeSuggestRequest++
	requestID, generation := d.knowledgeSuggestRequest, d.memoryGeneration
	epoch, stable := d.client.BeginMemoryRead()
	if !stable {
		d.knowledgeSuggesting = false
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	d.knowledgeSuggestCancel = cancel
	d.app.RequestUpdate()
	go func() {
		proposals, err := d.client.SuggestKnowledgeFromChronicle(ctx, hourID)
		cancel()
		d.app.Post(func() {
			if !knowledgeSuggestionCurrent(requestID, d.knowledgeSuggestRequest, hourID, d.expandedChronicleID, generation, d.memoryGeneration) {
				return
			}
			if !d.client.PublishMemoryRead(epoch, func() {
				d.knowledgeSuggestCancel = nil
				d.knowledgeSuggesting = false
				if err != nil {
					d.knowledgeSuggestStatus = "No suggestions were saved. Check local Ollama and try again."
				} else if len(proposals) == 0 {
					d.knowledgeSuggestStatus = "No durable Library facts stood out in this recap."
				} else {
					d.knowledgeProposals = proposals
					d.knowledgeSuggestStatus = "Nothing is added until you accept a suggestion."
				}
			}) {
				return
			}
			d.app.RequestUpdate()
		})
	}()
}

func (d *desktop) dismissKnowledgeSuggestion(index int) {
	if d == nil || d.knowledgeAccepting {
		return
	}
	d.knowledgeProposals = dismissKnowledgeSuggestion(d.knowledgeProposals, index)
	if len(d.knowledgeProposals) == 0 {
		d.knowledgeProposalHourID = ""
	}
	d.app.RequestUpdate()
}

func (d *desktop) acceptKnowledgeSuggestion(hourID string, index int) {
	if d == nil || d.client == nil || d.knowledgeAccepting || d.client.MemoryIsChanging() || hourID != d.expandedChronicleID || d.knowledgeProposalHourID != hourID || index < 0 || index >= len(d.knowledgeProposals) {
		return
	}
	proposal, generation := d.knowledgeProposals[index], d.memoryGeneration
	d.knowledgeAccepting = true
	d.knowledgeSuggestStatus = "Saving your reviewed fact…"
	d.app.RequestUpdate()
	go func() {
		_, err := d.client.AcceptKnowledgeSuggestion(context.Background(), hourID, proposal)
		d.app.Post(func() {
			if generation != d.memoryGeneration || d.expandedChronicleID != hourID || d.knowledgeProposalHourID != hourID {
				return
			}
			d.knowledgeAccepting = false
			if err != nil {
				d.knowledgeSuggestStatus = "This fact could not be added. The existing Library entry, if any, was left unchanged."
			} else {
				d.knowledgeProposals = dismissKnowledgeSuggestion(d.knowledgeProposals, index)
				d.knowledgeSuggestStatus = "Added to your Library with its source link."
				d.libraryLoaded = false
				if d.page == "Library" {
					d.loadLibrary()
				}
				if len(d.knowledgeProposals) == 0 {
					d.knowledgeProposalHourID = ""
				}
			}
			d.app.RequestUpdate()
		})
	}()
}

func (d *desktop) clearKnowledgeSuggestions() {
	if d == nil {
		return
	}
	if d.knowledgeSuggestCancel != nil {
		d.knowledgeSuggestCancel()
		d.knowledgeSuggestCancel = nil
	}
	d.knowledgeSuggestRequest++
	d.knowledgeSuggesting = false
	d.knowledgeProposals = nil
	d.knowledgeProposalHourID = ""
	d.knowledgeSuggestStatus = ""
	d.knowledgeAccepting = false
}

func (d *desktop) loadChronicleSourcesPage(id, after string, appendPage bool) {
	if d == nil || d.client == nil {
		return
	}
	generation := d.memoryGeneration
	epoch, stable := d.client.BeginMemoryRead()
	if !stable {
		d.chronicleSourcesLoading = false
		return
	}
	d.chronicleSourcesRequestID++
	requestID := d.chronicleSourcesRequestID
	d.chronicleSourcesLoading = true
	d.app.RequestUpdate()
	go func() {
		sources, next, err := d.client.ChronicleSourcesPage(context.Background(), id, after, 8)
		d.app.Post(func() {
			if generation != d.memoryGeneration || !currentChronicleSourcesLoad(requestID, d.chronicleSourcesRequestID, id, d.expandedChronicleID) {
				return
			}
			if !d.client.PublishMemoryRead(epoch, func() {
				d.chronicleSourcesLoading = false
				if err != nil {
					d.status = "Sources for this recap could not be read from encrypted storage."
					return
				}
				if appendPage {
					d.chronicleSources = append(d.chronicleSources, sources...)
				} else {
					d.chronicleSources = sources
				}
				d.chronicleSourcesNext = next
			}) {
				return
			}
			d.app.RequestUpdate()
		})
	}()
}

func (d *desktop) chronicleSourceList() ui.View {
	rows := make([]ui.View, 0, len(d.chronicleSources))
	for _, source := range d.chronicleSources {
		preview, truncated := searchPreview(source.Text)
		content := preview
		if d.expandedSourceID == source.SourceID {
			content = source.Text
		}
		item := []ui.View{wrapLabel(content), wrapLabel(strings.Trim(strings.Join([]string{source.AppID, source.Domain}, ", "), ", ")).Style(modern.MutedText)}
		if truncated || source.Truncated {
			id := source.SourceID
			label := "Show full source"
			if source.Truncated {
				label = "Load full source"
			} else if d.expandedSourceID == id {
				label = "Show preview"
			}
			item = append(item, flatButton(label).OnClick(func() {
				if source.Truncated {
					d.loadFullChronicleSource(id)
				} else if d.expandedSourceID == id {
					d.expandedSourceID = ""
				} else {
					d.expandedSourceID = id
				}
				d.app.RequestUpdate()
			}))
		}
		if d.client.CanDeleteSource() {
			id := source.SourceID
			if d.pendingDelete == id {
				item = append(item, wrapLabel("Deleting this source also removes the recap.").Style(modern.MutedText), ui.HBox(flatButton("Cancel").OnClick(func() { d.pendingDelete = ""; d.app.RequestUpdate() }), dangerButton("Delete permanently").OnClick(func() { d.deleteSource(id) })).Spacing(8))
			} else {
				item = append(item, flatButton("Delete source").OnClick(func() { d.pendingDelete = id; d.app.RequestUpdate() }))
			}
		}
		rows = append(rows, ui.VBox(item...).Spacing(6).Padding(8).CrossAlign(layout.CrossStretch))
	}
	if d.chronicleSourcesNext != "" {
		id := d.expandedChronicleID
		after := d.chronicleSourcesNext
		rows = append(rows, flatButton("Load more sources").Enabled(!d.chronicleSourcesLoading).OnClick(func() { d.loadChronicleSourcesPage(id, after, true) }))
	}
	if len(rows) == 0 {
		return wrapLabel("No source details are available.").Style(modern.MutedText)
	}
	return ui.VBox(rows...).Spacing(6).CrossAlign(layout.CrossStretch)
}

func (d *desktop) loadFullChronicleSource(id string) {
	generation := d.memoryGeneration
	epoch, stable := d.client.BeginMemoryRead()
	if !stable {
		return
	}
	d.chronicleSourcesRequestID++
	requestID := d.chronicleSourcesRequestID
	expandedID := d.expandedChronicleID
	d.chronicleSourcesLoading = true
	d.app.RequestUpdate()
	go func() {
		source, err := d.client.ChronicleSource(context.Background(), id)
		d.app.Post(func() {
			if generation != d.memoryGeneration || expandedID == "" || !currentChronicleSourcesLoad(requestID, d.chronicleSourcesRequestID, expandedID, d.expandedChronicleID) {
				return
			}
			if !d.client.PublishMemoryRead(epoch, func() {
				d.chronicleSourcesLoading = false
				if err != nil {
					d.status = "Source could not be read from encrypted storage."
					return
				}
				for i := range d.chronicleSources {
					if d.chronicleSources[i].SourceID == id {
						d.chronicleSources[i] = source
						break
					}
				}
				d.expandedSourceID = id
			}) {
				return
			}
			d.app.RequestUpdate()
		})
	}()
}

// currentChronicleSourcesLoad reports whether a source read still matches the
// latest request and the recap that is still expanded.
func currentChronicleSourcesLoad(requestID, latestRequestID uint64, loadedID, expandedID string) bool {
	return requestID == latestRequestID && loadedID != "" && loadedID == expandedID
}

func currentTodayLoad(requestID, latestRequestID uint64, requestedDate, selectedDate string) bool {
	return requestID == latestRequestID && requestedDate == selectedDate
}

func currentMemoryRead(requestGeneration, currentGeneration uint64) bool {
	return requestGeneration == currentGeneration
}

func (d *desktop) searchView() ui.View {
	children := []ui.View{
		surfaceBox(12, 0, ui.HBox(ui.TextInput().Text(d.query).Placeholder("Search your local memory").MainWeight(1).OnText(func(value string) {
			d.query, d.status = value, ""
			d.searchHits, d.searchComplete, d.searchFailed = nil, false, false
			d.expandedSourceID = ""
			d.app.RequestUpdate()
		}).OnSubmit(d.runSearch), primaryButton("Search").Enabled(!d.loading).OnClick(d.runSearch)).Spacing(10).CrossAlign(layout.CrossCenter)),
	}
	if d.status != "" {
		children = append(children, wrapLabel(d.status).Style(modern.MutedText))
	}
	for _, hit := range d.searchHits {
		preview, truncated := searchPreview(hit.Text)
		content := preview
		if d.expandedSourceID == hit.SourceID {
			content = hit.Text
		}
		row := []ui.View{wrapLabel(content), wrapLabel(strings.Trim(strings.Join([]string{hit.AppID, hit.Domain}, ", "), ", ")).Style(styleMeta)}
		if truncated {
			label := "Show full source"
			if d.expandedSourceID == hit.SourceID {
				label = "Show preview"
			}
			id := hit.SourceID
			row = append(row, hug(flatButton(label).OnClick(func() {
				if d.expandedSourceID == id {
					d.expandedSourceID = ""
				} else {
					d.expandedSourceID = id
				}
				d.app.RequestUpdate()
			})))
		}
		if d.client != nil && d.client.CanDeleteSource() {
			id := hit.SourceID
			if d.pendingDelete == id {
				row = append(row,
					wrapLabel("Delete this source and its derived recap items? This cannot be undone.").Style(modern.MutedText),
					ui.HBox(
						flatButton("Cancel").Enabled(!d.loading).OnClick(func() { d.pendingDelete = ""; d.app.RequestUpdate() }),
						dangerButton("Delete permanently").Enabled(!d.loading).OnClick(func() { d.deleteSource(id) }),
					).Spacing(8),
				)
			} else {
				row = append(row, hug(flatButton("Delete from memory").Enabled(!d.loading).OnClick(func() { d.pendingDelete = id; d.app.RequestUpdate() })))
			}
		}
		children = append(children, hairline(), ui.VBox(row...).Spacing(6).CrossAlign(layout.CrossStretch))
	}
	if message := searchEmptyMessage(d.loading, d.searchComplete, d.searchFailed, len(d.searchHits)); message != "" {
		children = append(children, recapSectionTitle(message))
	}
	return pageScroll(ui.VBox(hugButtons(children)...).Spacing(16).CrossAlign(layout.CrossStretch))
}

func searchEmptyMessage(loading, completed, failed bool, resultCount int) string {
	if loading || failed || resultCount > 0 {
		return ""
	}
	if completed {
		return "No matches. Try a shorter phrase or an exact word."
	}
	return "Search uses exact local terms and does not need AI."
}

func currentSearchLoad(requestedQuery, selectedQuery string) bool {
	return requestedQuery == selectedQuery
}

const searchPreviewRunes = 360

const answerPreviewRunes = searchPreviewRunes

// answerPreview keeps model output compact by default while allowing deliberate
// inspection of the complete answer. It operates on UTF-8 rune boundaries.
func answerPreview(text string) (string, bool) {
	return boundedPreview(text, answerPreviewRunes)
}

// searchPreview keeps result lists compact while preserving a Unicode-safe
// path to reveal the complete locally retrieved source on demand.
func searchPreview(text string) (string, bool) {
	return boundedPreview(text, searchPreviewRunes)
}

func boundedPreview(text string, maxRunes int) (string, bool) {
	count := 0
	for offset := range text {
		if count == maxRunes {
			return text[:offset] + "…", true
		}
		count++
	}
	if !utf8.ValidString(text) {
		return strings.ToValidUTF8(text, "�"), false
	}
	return text, false
}

func answerViews(text string, expanded bool, onToggle func(), textStyle string) []ui.View {
	preview, truncated := answerPreview(text)
	display := preview
	if expanded {
		display = text
	}
	label := wrapLabel(display)
	if textStyle != "" {
		label = label.Style(textStyle)
	}
	views := []ui.View{label}
	if truncated {
		button := "Show full answer"
		if expanded {
			button = "Show less"
		}
		views = append(views, flatButton(button).OnClick(onToggle))
	}
	return views
}

func appendAnswerViews(rows []ui.View, text string, expanded bool, onToggle func(), textStyle string) []ui.View {
	return append(rows, answerViews(text, expanded, onToggle, textStyle)...)
}

// deleteSourceFailureStatus is the failure text once capture was successfully
// paused for a deletion that then could not be confirmed.
func deleteSourceFailureStatus(capturePaused bool) string {
	status := "Deletion could not be confirmed. Persistent data was not removed; retry after storage is available."
	if capturePaused {
		status += " Capture remains paused."
	}
	return status
}

// markCapturePaused keeps the in-memory preference consistent with a pause made
// for a destructive operation, so the sidebar does not claim capture needs access.
func (d *desktop) markCapturePaused(paused bool) {
	if d != nil && paused && d.capture != nil && !d.capture.Running() {
		d.preferences.CaptureEnabled = false
		d.captureState = "paused"
		d.updateMenuBarCaptureState()
	}
}

func (d *desktop) deleteSource(id string) {
	if d.loading || d.client == nil || !d.client.CanDeleteSource() || d.pendingDelete != id {
		return
	}
	d.invalidateAsk()
	d.loading, d.status = true, "Deleting this source and its derived items…"
	wasCapturing := d.capture != nil && d.capture.Running()
	d.app.RequestUpdate()
	go func() {
		var err error
		if wasCapturing {
			err = d.capture.Pause(context.Background())
		}
		if err == nil {
			_, err = d.client.DeleteSource(id)
		}
		d.app.Post(func() {
			d.loading = false
			d.pendingDelete = ""
			if err != nil {
				if wasCapturing && d.capture.Running() {
					d.status = "Capture could not be paused; nothing was deleted. Pause capture, then retry."
				} else {
					d.status = deleteSourceFailureStatus(wasCapturing)
					d.markCapturePaused(wasCapturing)
				}
			} else {
				d.markCapturePaused(wasCapturing)
				d.status = "Source and its durable derived items were deleted."
				if wasCapturing {
					d.status += " Capture remains paused; restart it when ready."
				}
				filtered := d.searchHits[:0]
				for _, hit := range d.searchHits {
					if hit.SourceID != id {
						filtered = append(filtered, hit)
					}
				}
				for i := len(filtered); i < len(d.searchHits); i++ {
					d.searchHits[i] = SearchHit{}
				}
				d.searchHits = filtered
				if askResultUsesSource(d.askResult, id) {
					d.askResult = nil
					d.expandedAnswer = false
				}
				if d.expandedSourceID == id {
					d.expandedSourceID = ""
				}
				d.libraryLoaded = false
				d.library = knowledge.Page{}
				d.loadToday()
				if d.page == "Library" {
					d.loadLibrary()
				}
			}
			d.app.RequestUpdate()
		})
	}()
}

func (d *desktop) runSearch() {
	if d.loading {
		return
	}
	if d.client == nil || d.client.data == nil {
		d.status = "Encrypted local storage is not set up."
		d.searchFailed = true
		d.app.RequestUpdate()
		return
	}
	d.loading, d.status = true, "Searching locally…"
	d.searchComplete, d.searchFailed = false, false
	d.expandedSourceID = ""
	d.app.RequestUpdate()
	query := d.query
	memoryGeneration := d.memoryGeneration
	memoryEpoch, stable := d.client.BeginMemoryRead()
	if !stable {
		d.loading = false
		d.status = ""
		return
	}
	go func() {
		hits, err := d.client.SearchMemory(query)
		d.app.Post(func() {
			if !currentMemoryRead(memoryGeneration, d.memoryGeneration) {
				d.loading = false
				d.app.RequestUpdate()
				return
			}
			if !currentSearchLoad(query, d.query) {
				d.loading = false
				d.app.RequestUpdate()
				return
			}
			if !d.client.PublishMemoryRead(memoryEpoch, func() {
				d.loading = false
				d.searchHits = hits
				d.searchComplete, d.searchFailed = true, err != nil
				if err != nil {
					d.status = friendlyError(err)
				} else {
					d.status = fmt.Sprintf("%d result(s)", len(hits))
				}
			}) {
				d.loading = false
				d.app.RequestUpdate()
				return
			}
			d.app.RequestUpdate()
		})
	}()
}

func (d *desktop) libraryView() ui.View {
	if d.client == nil || d.client.data == nil {
		return wrapLabel("Encrypted local storage is not set up.").Style(modern.MutedText)
	}
	if d.libraryLoading && !d.libraryLoaded {
		return pageScroll(ui.VBox(d.knowledgeControls(), wrapLabel("Loading your local library…").Style(modern.MutedText)).Spacing(14).CrossAlign(layout.CrossStretch))
	}
	if !d.libraryLoaded {
		if canRetryLibraryLoad(d.libraryLoading, d.libraryLoaded, d.status) {
			return ui.VBox(
				d.knowledgeControls(),
				wrapLabel(d.status).Style(modern.MutedText),
				hug(flatButton("Retry").Enabled(!d.loading).OnClick(d.loadLibrary)),
			).Spacing(8).CrossAlign(layout.CrossStart)
		}
		return ui.VBox(d.knowledgeControls(), wrapLabel(d.status).Style(modern.MutedText)).Spacing(12).CrossAlign(layout.CrossStretch)
	}
	rows := make([]ui.View, 0, len(d.library.Entities))
	rows = append(rows, d.knowledgeControls())
	if d.status != "" {
		rows = append(rows, wrapLabel(d.status).Style(modern.MutedText))
	}
	if d.justAdded != nil {
		rows = append(rows, recapSectionTitle("Just added"), d.knowledgeEntityCard(*d.justAdded))
	}
	if len(d.library.Entities) == 0 && !d.library.HasMore && d.justAdded == nil {
		rows = append(rows, recapSectionTitle("Your library is empty. Add someone or something you want to remember."))
	}
	for _, entity := range d.library.Entities {
		if d.justAdded != nil && entity.ID == d.justAdded.ID {
			continue
		}
		rows = append(rows, d.knowledgeEntityCard(entity))
	}
	if d.library.HasMore {
		rows = append(rows, hug(flatButton("Load more").Enabled(!d.libraryLoading && !d.loading).OnClick(func() { d.loadLibraryAfter(d.library.Next) })))
	} else if d.libraryLoading {
		rows = append(rows, wrapLabel("Loading more…").Style(modern.MutedText))
	}
	return pageScroll(ui.VBox(hugButtons(rows)...).Spacing(12).CrossAlign(layout.CrossStretch))
}

func (d *desktop) knowledgeEntityCard(entity knowledge.Entity) ui.View {
	parts := []ui.View{wrapLabel(entity.Name).Style(styleSectionTitle), wrapLabel(string(entity.Kind) + ", " + string(entity.State)).Style(styleMeta)}
	for _, fact := range entity.Facts {
		parts = append(parts, wrapLabel(fact.Text))
	}
	for _, loop := range entity.OpenLoops {
		parts = append(parts, wrapLabel("Open: "+loop.Text).Style(modern.MutedText))
	}
	if d != nil && d.client != nil && d.client.CanDeleteKnowledge() {
		if canUnmergeKnowledgeEntry(entity) {
			parts = append(parts, wrapLabel("Merged entry. Unmerge it to restore both library entries.").Style(modern.MutedText))
			if d.client.CanUnmergeKnowledge() {
				if d.pendingUnmergeKnowledge == entity.ID {
					parts = append(parts,
						wrapLabel(knowledgeUnmergeConfirmationCopy()).Style(modern.MutedText),
						ui.HBox(
							flatButton("Cancel").Enabled(!d.loading && !d.libraryLoading && d.knowledgeDeletingID == "").OnClick(func() {
								d.pendingUnmergeKnowledge = ""
								d.app.RequestUpdate()
							}),
							primaryButton("Unmerge entries").Enabled(!d.loading && !d.libraryLoading && !d.knowledgeSaving && d.knowledgeDeletingID == "").OnClick(func() { d.unmergeKnowledge(entity.ID, entity.MergedInto) }),
						).Spacing(8),
					)
				} else {
					id := entity.ID
					parts = append(parts, hug(flatButton("Unmerge…").Enabled(!d.loading && !d.libraryLoading && d.knowledgeDeletingID == "").OnClick(func() {
						d.pendingUnmergeKnowledge = id
						d.pendingDeleteKnowledge = ""
						d.app.RequestUpdate()
					})))
				}
			}
		} else if len(entity.RelatedIDs) > 0 {
			parts = append(parts, wrapLabel("Unmerge linked entries first. Their “Merged” cards have an Unmerge action.").Style(modern.MutedText))
		} else if d.pendingDeleteKnowledge == entity.ID {
			parts = append(parts,
				wrapLabel(knowledgeDeleteConfirmationCopy()).Style(modern.MutedText),
				ui.HBox(
					flatButton("Cancel").Enabled(!d.loading && !d.libraryLoading && d.knowledgeDeletingID == "").OnClick(func() {
						d.pendingDeleteKnowledge = ""
						d.app.RequestUpdate()
					}),
					dangerButton(knowledgeDeleteActionLabel(true, d.knowledgeDeletingID == entity.ID)).Enabled(!d.loading && !d.libraryLoading && !d.knowledgeSaving && d.knowledgeDeletingID == "").OnClick(func() { d.deleteKnowledge(entity.ID) }),
				).Spacing(8),
			)
		} else if canRemoveKnowledgeEntry(entity) {
			id := entity.ID
			parts = append(parts, hug(flatButton("Remove entry").Enabled(!d.loading && !d.libraryLoading && !d.knowledgeSaving && d.knowledgeDeletingID == "").OnClick(func() {
				d.pendingDeleteKnowledge = id
				d.pendingUnmergeKnowledge = ""
				d.status = ""
				d.app.RequestUpdate()
			})))
		}
	}
	return surfaceBox(14, 6, hugButtons(parts)...)
}

func knowledgeDeleteActionLabel(confirming, deleting bool) string {
	if deleting {
		return "Removing…"
	}
	if confirming {
		return "Remove permanently"
	}
	return "Remove entry"
}

func knowledgeDeleteConfirmationCopy() string {
	return "Remove this library entry and its notes? Captured sources remain saved. You cannot undo this."
}

func canRemoveKnowledgeEntry(entity knowledge.Entity) bool {
	return entity.State != knowledge.Merged && len(entity.RelatedIDs) == 0
}

func canUnmergeKnowledgeEntry(entity knowledge.Entity) bool {
	return entity.State == knowledge.Merged && entity.MergedInto != ""
}

func knowledgeUnmergeConfirmationCopy() string {
	return "Restore this entry and its destination’s previous details? Captured sources remain saved."
}

func (d *desktop) knowledgeControls() ui.View {
	if d.client == nil || !d.client.CanAddKnowledge() {
		return ui.VBox()
	}
	if !d.addingKnowledge {
		return ui.HBox(primaryButton("Add to library").Enabled(!d.loading && !d.knowledgeSaving).OnClick(func() {
			d.addingKnowledge = true
			d.status = ""
			d.app.RequestUpdate()
		})).CrossAlign(layout.CrossCenter)
	}
	kinds := []struct {
		kind  knowledge.Kind
		label string
	}{{knowledge.Person, "Person"}, {knowledge.Project, "Project"}, {knowledge.Topic, "Topic"}, {knowledge.Place, "Place"}, {knowledge.Workflow, "Workflow"}}
	kindButtons := make([]ui.View, 0, len(kinds))
	for _, option := range kinds {
		option := option
		kindButtons = append(kindButtons, choiceButton(option.label, d.knowledgeKind == option.kind).Enabled(!d.loading && !d.knowledgeSaving).OnClick(func() {
			d.knowledgeKind = option.kind
			d.app.RequestUpdate()
		}))
	}
	valid := validKnowledgeDraft(d.knowledgeKind, d.knowledgeName, d.knowledgeNote)
	return ui.VBox(
		wrapLabel("Add to your library").Style(styleSectionTitle),
		wrapLabel("Only the details you enter here are saved. Captured text is not used.").Style(modern.MutedText),
		ui.HBox(kindButtons...).Spacing(6).CrossAlign(layout.CrossCenter),
		ui.TextInput().Text(d.knowledgeName).Placeholder("Name").OnText(func(value string) { d.knowledgeName = value; d.app.RequestUpdate() }),
		ui.TextView().Text(d.knowledgeNote).Placeholder("Optional note, kept as your own source").OnText(func(value string) { d.knowledgeNote = value; d.app.RequestUpdate() }).WrapMode(ui.WrapWordChar).MinHeight(64).MaxHeight(112),
		ui.HBox(
			flatButton("Cancel").Enabled(!d.loading && !d.knowledgeSaving).OnClick(func() { d.cancelKnowledgeEntry() }),
			primaryButton(knowledgeSaveLabel(d.knowledgeKind, d.knowledgeSaving)).Enabled(valid && !d.loading && !d.knowledgeSaving).OnClick(d.addKnowledge),
		).Spacing(8).CrossAlign(layout.CrossCenter),
	).Spacing(9).Padding(16).Style(modern.Card).CrossAlign(layout.CrossStretch)
}

func knowledgeSaveLabel(kind knowledge.Kind, saving bool) string {
	if saving {
		return "Saving…"
	}
	return "Add " + string(kind)
}

func validKnowledgeDraft(kind knowledge.Kind, name, note string) bool {
	if kind != knowledge.Person && kind != knowledge.Project && kind != knowledge.Topic && kind != knowledge.Place && kind != knowledge.Workflow {
		return false
	}
	name, note = strings.TrimSpace(name), strings.TrimSpace(note)
	return name != "" && len(name) <= knowledge.MaxTextBytes && utf8.ValidString(name) && !strings.ContainsRune(name, 0) && len(note) <= knowledge.MaxTextBytes && utf8.ValidString(note) && !strings.ContainsRune(note, 0)
}

func (d *desktop) cancelKnowledgeEntry() {
	d.addingKnowledge = false
	d.knowledgeName, d.knowledgeNote = "", ""
	d.knowledgeKind = knowledge.Project
	d.status = ""
	d.app.RequestUpdate()
}

func (d *desktop) addKnowledge() {
	if d.loading || d.knowledgeSaving || d.client == nil || !validKnowledgeDraft(d.knowledgeKind, d.knowledgeName, d.knowledgeNote) {
		return
	}
	d.knowledgeSaving = true
	d.status = ""
	d.app.RequestUpdate()
	kind, name, note := d.knowledgeKind, d.knowledgeName, d.knowledgeNote
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		entity, err := d.client.AddKnowledge(ctx, kind, name, note)
		cancel()
		d.app.Post(func() {
			d.knowledgeSaving = false
			if err != nil {
				d.status = friendlyError(err)
			} else {
				d.addingKnowledge = false
				d.knowledgeName, d.knowledgeNote = "", ""
				d.knowledgeKind = knowledge.Project
				d.justAdded = &entity
				d.status = "Added to your library."
				if !d.libraryLoaded {
					d.loadLibrary()
				}
			}
			d.app.RequestUpdate()
		})
	}()
}

func removeKnowledgeFromPage(page knowledge.Page, id string) (knowledge.Page, bool) {
	for i := range page.Entities {
		if page.Entities[i].ID != id {
			continue
		}
		copy(page.Entities[i:], page.Entities[i+1:])
		page.Entities[len(page.Entities)-1] = knowledge.Entity{}
		page.Entities = page.Entities[:len(page.Entities)-1]
		return page, true
	}
	return page, false
}

func (d *desktop) deleteKnowledge(id string) {
	if d == nil || d.loading || d.knowledgeSaving || d.knowledgeDeletingID != "" || d.pendingDeleteKnowledge != id || d.client == nil || !d.client.CanDeleteKnowledge() {
		return
	}
	d.loading = true
	d.knowledgeDeletingID = id
	d.status = "Removing this library entry…"
	d.app.RequestUpdate()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := d.client.DeleteKnowledge(ctx, id)
		cancel()
		d.app.Post(func() {
			d.loading = false
			d.knowledgeDeletingID = ""
			d.pendingDeleteKnowledge = ""
			if err != nil {
				d.status = "Could not remove this entry. It may be linked to a merged entry; otherwise check storage and retry."
			} else {
				d.library, _ = removeKnowledgeFromPage(d.library, id)
				if d.justAdded != nil && d.justAdded.ID == id {
					d.justAdded = nil
				}
				d.status = "Removed from your library. Captured sources remain saved."
			}
			d.app.RequestUpdate()
		})
	}()
}

func (d *desktop) unmergeKnowledge(sourceID, targetID string) {
	if d == nil || d.loading || d.libraryLoading || d.knowledgeSaving || d.knowledgeDeletingID != "" || d.pendingUnmergeKnowledge != sourceID || d.client == nil || !d.client.CanUnmergeKnowledge() {
		return
	}
	d.loading = true
	d.knowledgeDeletingID = sourceID
	d.status = "Restoring the separate library entries…"
	d.app.RequestUpdate()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := d.client.UnmergeKnowledge(ctx, sourceID, targetID)
		cancel()
		d.app.Post(func() {
			d.loading = false
			d.knowledgeDeletingID = ""
			d.pendingUnmergeKnowledge = ""
			if err != nil {
				d.status = "Could not unmerge these entries. The destination may have changed; reload the Library and retry."
			} else {
				d.library = knowledge.Page{}
				d.libraryLoaded = false
				d.status = "Entries restored as separate library items. Captured sources remain saved."
				d.loadLibrary()
			}
			d.app.RequestUpdate()
		})
	}()
}

func canRetryLibraryLoad(loading, loaded bool, status string) bool {
	return !loading && !loaded && strings.TrimSpace(status) != ""
}

func (d *desktop) loadLibrary() {
	if d.libraryLoaded {
		return
	}
	d.loadLibraryAfter("")
}

func (d *desktop) loadLibraryAfter(after string) {
	if d.client == nil || d.client.data == nil || d.libraryLoading {
		return
	}
	if after != "" && (!d.libraryLoaded || !d.library.HasMore || d.library.Next != after) {
		return
	}
	d.libraryLoading = true
	d.app.RequestUpdate()
	current := d.library
	memoryGeneration := d.memoryGeneration
	memoryEpoch, stable := d.client.BeginMemoryRead()
	if !stable {
		d.libraryLoading = false
		return
	}
	if after == "" {
		current = knowledge.Page{}
	}
	go func() {
		page, err := d.client.Knowledge(after)
		if err == nil {
			page, err = appendKnowledgePage(current, page, after)
		}
		d.app.Post(func() {
			if !currentMemoryRead(memoryGeneration, d.memoryGeneration) {
				return
			}
			if !d.client.PublishMemoryRead(memoryEpoch, func() {
				d.libraryLoading = false
				if err != nil {
					d.status = friendlyError(err)
				} else {
					d.library = page
					d.libraryLoaded = true
					d.status = ""
				}
			}) {
				return
			}
			d.app.RequestUpdate()
		})
	}()
}

const (
	settingsGroupCapture     = "Capture"
	settingsGroupAI          = "AI"
	settingsGroupQuickAsk    = "Quick ask"
	settingsGroupConnections = "Connections"
	settingsGroupMemory      = "Memory"
	settingsGroupPrivacy     = "Privacy"
)

// settingsGroups lists the Settings page's sub-navigation in display order.
func settingsGroups() []string {
	return []string{settingsGroupCapture, settingsGroupAI, settingsGroupQuickAsk, settingsGroupConnections, settingsGroupMemory, settingsGroupPrivacy}
}

// normalSettingsGroup maps unknown names to the default group.
func normalSettingsGroup(group string) string {
	for _, name := range settingsGroups() {
		if name == group {
			return group
		}
	}
	return settingsGroupCapture
}

// openSettings navigates to the Settings page on the given group. Every deep
// link into Settings goes through it so the page and group never disagree.
func (d *desktop) openSettings(group string) {
	if d == nil {
		return
	}
	d.page, d.settingsGroup, d.status = "Settings", normalSettingsGroup(group), ""
	if d.app == nil {
		return
	}
	if d.powerStatus == "" && !d.powerChecking {
		d.refreshPowerState()
	}
	d.app.RequestUpdate()
}

func (d *desktop) settingsChips() ui.View {
	selected := normalSettingsGroup(d.settingsGroup)
	buttons := make([]ui.View, 0, len(settingsGroups()))
	for _, name := range settingsGroups() {
		group := name
		buttons = append(buttons, choiceButton(group, selected == group).OnClick(func() {
			d.settingsGroup, d.status = group, ""
			d.app.RequestUpdate()
		}))
	}
	return ui.HBox(buttons...).Spacing(4).CrossAlign(layout.CrossCenter)
}

func (d *desktop) settingsView() ui.View {
	group := normalSettingsGroup(d.settingsGroup)
	rows := []ui.View{d.settingsChips()}
	rows = append(rows, d.settingsGroupViews(group)...)
	if d.status != "" {
		rows = append(rows, wrapLabel(d.status).Style(modern.MutedText))
	}
	return pageScroll(ui.VBox(hugButtons(rows)...).Spacing(16).CrossAlign(layout.CrossStretch))
}

func (d *desktop) settingsGroupViews(group string) []ui.View {
	var views []ui.View
	switch group {
	case settingsGroupAI:
		views = append(views, d.localAISettingsView(), d.chronicleSettingsView())
		if d.client != nil && d.client.CanConfigurePower() {
			views = append(views, d.powerSettingsView())
		}
	case settingsGroupQuickAsk:
		if quickAsk := d.quickAskSettingsView(); quickAsk != nil {
			views = append(views, quickAsk)
		} else {
			views = append(views, surfaceBox(14, 5,
				wrapLabel("Quick ask").Style(styleSectionTitle),
				wrapLabel("A global shortcut for Quick ask is not available on this platform.").Style(modern.MutedText),
			))
		}
	case settingsGroupConnections:
		views = append(views, d.claudeMCPSettingsView())
		views = append(views, d.syncSettingsViews()...)
	case settingsGroupMemory:
		if d.client != nil && d.client.CanConfigureRetention() {
			views = append(views, d.retentionView())
		}
		if d.client != nil && d.client.CanDeleteAll() {
			views = append(views, d.deleteAllView())
		}
	case settingsGroupPrivacy:
		views = append(views, d.privacyOverviewView())
	default:
		if capture := d.captureSettingsView(); capture != nil {
			views = append(views, capture)
		}
		views = append(views, d.captureExclusionsSettingsView())
	}
	return views
}

func (d *desktop) deleteAllView() ui.View {
	if d.pendingDeleteAll {
		return ui.VBox(
			wrapLabel("Delete all memory").Style(styleSectionTitle),
			wrapLabel(deleteAllConfirmationCopy()).Style(modern.MutedText),
			ui.HBox(
				flatButton("Cancel").Enabled(!d.loading).OnClick(func() { d.pendingDeleteAll = false; d.app.RequestUpdate() }),
				dangerButton(deleteAllActionLabel(true)).Enabled(!d.loading && !d.knowledgeSaving).OnClick(d.deleteAllMemory),
			).Spacing(8),
		).Spacing(8).Padding(14).Style(modern.Card).CrossAlign(layout.CrossStretch)
	}
	return ui.VBox(
		wrapLabel("Memory controls").Style(styleSectionTitle),
		wrapLabel("Remove saved notes, knowledge library entries, captured sources, and their derived recap data.").Style(modern.MutedText),
		flatButton(deleteAllActionLabel(false)).Enabled(!d.loading && !d.knowledgeSaving).OnClick(func() { d.pendingDeleteAll = true; d.app.RequestUpdate() }),
	).Spacing(8).Padding(14).Style(modern.Card).CrossAlign(layout.CrossStretch)
}

// privacyOverviewView is the Privacy group: what Blur keeps, plus links to
// the groups that hold the controls.
func (d *desktop) privacyOverviewView() ui.View {
	first, rest := splitFirstSentence(privacyOverviewCopy())
	rows := []ui.View{wrapLabel("Your data and control").Style(styleSectionTitle)}
	rows = append(rows, d.groupCopy("Your data and control", first, rest)...)
	for _, link := range []struct{ label, group string }{
		{"Capture settings", settingsGroupCapture},
		{"AI settings", settingsGroupAI},
		{"Memory settings", settingsGroupMemory},
	} {
		group := link.group
		rows = append(rows, flushButton(link.label).OnClick(func() { d.openSettings(group) }))
	}
	return surfaceBox(14, 5, rows...)
}

func (d *desktop) claudeMCPSettingsView() ui.View {
	state := "Off. Blur does not create the local Claude socket or Keychain grant."
	label := "Allow Claude to search Blur memory"
	if d.preferences.ClaudeMCPEnabled {
		label = "Disable Claude access"
		if d.client != nil && d.client.ClaudeMCPRunning() {
			state = "On. Claude has read-only access to bounded local search results."
		} else {
			state = "Enabled, but the bridge is not running. Check Keychain access and try again."
		}
	}
	if d.mcpStatus != "" {
		state = d.mcpStatus
	}
	rows := []ui.View{wrapLabel("Claude integration").Style(styleSectionTitle)}
	rows = append(rows, d.groupCopy("Claude integration", state, "Optional: let Claude search saved Blur memory over a private, user-only local socket. Claude receives the matching text; if its model provider is hosted, that provider may receive the results. Blur does not send them itself.")...)
	if d.client == nil || !d.client.ClaudeMCPAvailable() {
		rows = append(rows, wrapLabel("The local Claude bridge is unavailable on this build.").Style(modern.MutedText))
	} else {
		rows = append(rows, flatButton(label).Enabled(!d.loading && !d.preferencesNeedsRepair).OnClick(d.toggleClaudeMCP))
	}
	return surfaceBox(14, 8, hugButtons(rows)...)
}

func (d *desktop) toggleClaudeMCP() {
	if d == nil || d.client == nil || d.app == nil || d.loading || d.preferencesNeedsRepair || !d.client.ClaudeMCPAvailable() {
		return
	}
	previous := d.preferences
	next := previous
	next.ClaudeMCPEnabled = !previous.ClaudeMCPEnabled
	d.loading = true
	d.mcpStatus = "Updating Claude access…"
	d.app.RequestUpdate()
	go func() {
		err := d.client.ConfigureApplicationPreferences(context.Background(), next)
		d.app.Post(func() {
			d.loading = false
			if err != nil {
				d.preferences = previous
				d.mcpStatus = "Claude access could not be changed. The previous setting remains active."
			} else {
				d.preferences = next
				if next.ClaudeMCPEnabled {
					d.mcpStatus = "Claude can search Blur memory through the read-only local bridge."
				} else {
					d.mcpStatus = "Claude access is off; the local grant was revoked and removed."
				}
			}
			d.app.RequestUpdate()
		})
	}()
}

func privacyOverviewCopy() string {
	return "Notes and captured evidence are encrypted locally before they enter the durable store. Tasks need confirmation, and follow-up questions can be answered or dismissed without sending messages or scheduling notifications. Library entries are created only from details you enter."
}

func (d *desktop) captureSettingsView() ui.View {
	if d.capture == nil {
		return ui.VBox(
			wrapLabel("Capture has not started").Style(styleSectionTitle),
			wrapLabel("Secure local storage is unavailable, so Blur has not started the capture controller.").Style(modern.MutedText),
			wrapLabel(browserCaptureDisclosure()).Style(modern.MutedText),
		).Spacing(5).Padding(14).Style(modern.Card).CrossAlign(layout.CrossStretch)
	}
	label := captureActionLabel(d.capture.Running())
	captureFirst, captureRest := splitFirstSentence(captureDescription(d.capture.Running(), d.preferences.CaptureEnabled))
	rows := []ui.View{wrapLabel("Capture").Style(styleSectionTitle)}
	rows = append(rows, d.groupCopy("Capture", captureFirst, captureRest, browserCaptureDisclosure())...)
	rows = append(rows, primaryButton(label).Enabled(!d.loading).OnClick(d.toggleCapture))
	if supportsSafariPageCaptureSetting() {
		safariStatus := "Safari page capture is not configured in this build. It requires a signed build with Safari App Group and Keychain entitlements."
		if safariPageCaptureBuildConfigured() {
			safariStatus = "Optional: capture text from the active Safari tab only. Safari may request permission for individual websites. Private windows and browsing history are excluded."
			if d.preferences.SafariPageCaptureEnabled {
				if !d.preferences.CaptureEnabled {
					safariStatus = "Safari page capture is paused with global capture."
				} else {
					safariStatus = d.client.SafariCaptureStatus()
					if safariStatus == "" || safariStatus == safariCaptureOffStatus {
						safariStatus = safariCaptureStartingStatus
					}
				}
			}
		}
		safariLabel := "Enable Safari page capture"
		if d.preferences.SafariPageCaptureEnabled {
			safariLabel = "Disable Safari page capture"
		}
		rows = append(rows,
			wrapLabel(safariStatus).Style(modern.MutedText),
			flushButton(safariLabel).Enabled(!d.loading && safariPageCaptureToggleAllowed(d.preferences.SafariPageCaptureEnabled, safariPageCaptureBuildConfigured())).OnClick(d.toggleSafariPageCapture),
		)
	}
	if d.captureStarting {
		rows = append(rows, wrapLabel("Starting capture…").Style(modern.MutedText))
	}
	if supportsAccessibilitySettingsShortcut() && d.captureState == "permission_required" {
		rows = append(rows, secondaryButton("Open Accessibility Settings").Enabled(!d.loading).OnClick(d.openAccessibilitySettings))
	}
	if d.preferencesNeedsRepair {
		rows = append(rows,
			wrapLabel("Local preferences could not be read. Capture is paused until settings are repaired.").Style(modern.MutedText),
			secondaryButton("Restore default preferences").Enabled(!d.loading).OnClick(d.restoreDefaultPreferences),
		)
	}
	if menu := menuBarSettingsView(d); menu != nil {
		rows = append(rows, menu)
	}
	return surfaceBox(14, 9, hugButtons(rows)...)
}

func (d *desktop) captureExclusionsSettingsView() ui.View {
	scopeFirst, scopeRest := splitFirstSentence("Choose what Blur captures. Never-capture rules always win; existing saved history is unchanged.")
	rows := []ui.View{wrapLabel("Capture scope").Style(styleSectionTitle)}
	rows = append(rows, d.groupCopy("Capture scope", scopeFirst, scopeRest)...)
	rows = append(rows,
		ui.HBox(
			choiceButton("Capture all by default", !d.captureDefaultDisabledDraft).Enabled(!d.loading).OnClick(func() { d.captureDefaultDisabledDraft = false; d.app.RequestUpdate() }),
			choiceButton("Capture none by default", d.captureDefaultDisabledDraft).Enabled(!d.loading).OnClick(func() { d.captureDefaultDisabledDraft = true; d.app.RequestUpdate() }),
		).Spacing(8),
		wrapLabel("Allow these apps (only needed when default is disabled)"),
	)
	rows = append(rows, d.captureAppList(&d.includedAppsDraft, "No apps allowed yet.")...)
	rows = append(rows,
		wrapLabel("Allow these website domains"),
		ui.TextView().Text(d.includedDomainsDraft).Placeholder("work.example").Enabled(!d.loading).
			OnText(func(value string) { d.includedDomainsDraft = value; d.exclusionValidation = ""; d.app.RequestUpdate() }).
			WrapMode(ui.WrapWordChar).MinHeight(56).MaxHeight(92),
		wrapLabel("Never capture these apps (blocklist)"),
	)
	rows = append(rows, d.captureAppList(&d.excludedAppsDraft, "No apps are blocked.")...)
	rows = append(rows,
		wrapLabel("Never capture these website domains (blocklist)"),
		ui.TextView().Text(d.excludedDomainsDraft).Placeholder("private.example").Enabled(!d.loading).
			OnText(func(value string) { d.excludedDomainsDraft = value; d.exclusionValidation = ""; d.app.RequestUpdate() }).
			WrapMode(ui.WrapWordChar).MinHeight(68).MaxHeight(112),
		primaryButton("Save capture rules").Enabled(!d.loading && d.capture != nil && d.client != nil && !d.preferencesNeedsRepair).OnClick(d.saveCaptureExclusions),
	)
	if d.exclusionValidation != "" {
		rows = append(rows, wrapLabel(d.exclusionValidation).Style(modern.MutedText))
	}
	return surfaceBox(14, 8, hugButtons(rows)...)
}

func (d *desktop) saveCaptureExclusions() {
	if d == nil || d.client == nil || d.capture == nil || d.loading || d.preferencesNeedsRepair {
		return
	}
	exclusions, ok := parseCaptureExclusionDrafts(d.excludedAppsDraft, d.excludedDomainsDraft)
	if !ok {
		d.exclusionValidation = "Check each entry: use app identifiers or executable names and plain domain names (up to 32 of each)."
		d.app.RequestUpdate()
		return
	}
	allowed, ok := parseCaptureExclusionDrafts(d.includedAppsDraft, d.includedDomainsDraft)
	if !ok {
		d.exclusionValidation = "Check each allow entry: use app identifiers or plain domain names (up to 32 of each)."
		d.app.RequestUpdate()
		return
	}
	previous := d.preferences
	next := previous
	next.ExcludedApps = append([]string(nil), exclusions.Apps...)
	next.ExcludedDomains = append([]string(nil), exclusions.Domains...)
	next.IncludedApps = append([]string(nil), allowed.Apps...)
	next.IncludedDomains = append([]string(nil), allowed.Domains...)
	next.CaptureDefaultDisabled = d.captureDefaultDisabledDraft
	wasRunning := d.capture.Running()
	d.loading = true
	d.exclusionValidation = ""
	d.status = "Applying ignore list…"
	d.app.RequestUpdate()
	go func() {
		ctx := context.Background()
		var err error
		if wasRunning {
			err = d.capture.Pause(ctx)
		}
		if err == nil {
			err = d.client.ConfigureApplicationPreferences(ctx, next)
		}
		if err != nil {
			if wasRunning && !d.capture.Running() {
				d.startCaptureNoted(context.Background())
			}
			d.app.Post(func() {
				d.loading = false
				d.preferences = previous
				d.status = "Ignore list could not be saved; the previous settings remain active."
				d.app.RequestUpdate()
			})
			return
		}
		var resumeErr error
		if previous.CaptureEnabled && !d.capture.Running() {
			resumeErr = d.capture.Start(ctx)
		}
		d.app.Post(func() {
			if previous.CaptureEnabled && resumeErr == nil && d.capture.Running() {
				d.noteCaptureStarted()
			}
			d.loading = false
			d.preferences = next
			d.excludedAppsDraft = strings.Join(next.ExcludedApps, "\n")
			d.excludedDomainsDraft = strings.Join(next.ExcludedDomains, "\n")
			d.includedAppsDraft = strings.Join(next.IncludedApps, "\n")
			d.includedDomainsDraft = strings.Join(next.IncludedDomains, "\n")
			d.captureDefaultDisabledDraft = next.CaptureDefaultDisabled
			if resumeErr != nil {
				d.status = "Ignore list saved. Capture is paused because it could not restart; resume it in Settings."
			} else if wasRunning {
				d.status = "Ignore list saved and applied to new captures."
			} else {
				d.status = "Ignore list saved; it will apply when capture resumes."
			}
			d.updateMenuBarCaptureState()
			d.app.RequestUpdate()
		})
	}()
}

// startCaptureNoted restarts capture after a rolled-back change and, only on
// success, records the start on the UI thread for crash-recovery backoff.
func (d *desktop) startCaptureNoted(ctx context.Context) {
	if d.capture.Start(ctx) == nil {
		d.app.Post(d.noteCaptureStarted)
	}
}

func (d *desktop) powerSettingsView() ui.View {
	modes := []power.Mode{power.FollowSystem, power.Balanced, power.Saver, power.Performance}
	first := make([]ui.View, 0, 2)
	second := make([]ui.View, 0, 2)
	for i, mode := range modes {
		selected := d.powerDraftMode == mode
		button := choiceButton(powerModeLabel(mode), selected).Enabled(!d.loading && !d.powerSaving).OnClick(func() {
			d.powerDraftMode = mode
			d.app.RequestUpdate()
		})
		if i < 2 {
			first = append(first, button)
		} else {
			second = append(second, button)
		}
	}
	powerFirst, powerRest := splitFirstSentence("This policy controls optional local hourly recaps when you enable them in Settings. Follow System uses the OS battery-saver signal and falls back to Balanced when unavailable. Battery power alone never pauses Blur; capture, retention, and AI you start remain available.")
	rows := []ui.View{wrapLabel("Background work").Style(styleSectionTitle)}
	rows = append(rows, d.groupCopy("Background work", powerFirst, powerRest)...)
	rows = append(rows,
		ui.HBox(first...).Spacing(8),
		ui.HBox(second...).Spacing(8),
		wrapLabel(powerModeDescription(d.powerDraftMode)).Style(modern.MutedText),
	)
	if d.powerDraftMode != power.Saver {
		rows = append(rows,
			ui.HBox(
				ui.VBox(wrapLabel("Max jobs"), ui.TextInput().Text(d.powerJobsDraft).Placeholder("1").Enabled(!d.loading && !d.powerSaving).OnText(func(value string) { d.powerJobsDraft = value; d.app.RequestUpdate() })).Spacing(4).MainWeight(1),
				ui.VBox(wrapLabel("Max tokens"), ui.TextInput().Text(d.powerTokensDraft).Placeholder("2048").Enabled(!d.loading && !d.powerSaving).OnText(func(value string) { d.powerTokensDraft = value; d.app.RequestUpdate() })).Spacing(4).MainWeight(1),
				ui.VBox(wrapLabel("Max seconds"), ui.TextInput().Text(d.powerSecondsDraft).Placeholder("60").Enabled(!d.loading && !d.powerSaving).OnText(func(value string) { d.powerSecondsDraft = value; d.app.RequestUpdate() })).Spacing(4).MainWeight(1),
			).Spacing(8),
			wrapLabel("Limits can only be reduced below the selected mode’s defaults and product-wide caps.").Style(modern.MutedText),
		)
	}
	if d.powerNeedsRepair {
		rows = append(rows, wrapLabel("Power settings could not be read. Saving a mode will replace the invalid preference.").Style(modern.MutedText))
	}
	draftConfig, validBudget := parsePowerBudget(d.powerDraftMode, d.powerJobsDraft, d.powerTokensDraft, d.powerSecondsDraft)
	if !validBudget {
		rows = append(rows, wrapLabel(powerBudgetHint(d.powerDraftMode)).Style(modern.MutedText))
	}
	rows = append(rows, primaryButton("Save power settings").Enabled(!d.loading && !d.powerSaving && validBudget && (d.powerNeedsRepair || powerConfigChanged(d.powerConfig, draftConfig))).OnClick(d.savePowerMode))
	if d.powerStatus != "" {
		rows = append(rows, wrapLabel(d.powerStatus).Style(modern.MutedText))
	}
	rows = append(rows, secondaryButton("Check current system mode").Enabled(!d.loading && !d.powerChecking && !d.powerSaving).OnClick(d.refreshPowerState))
	return surfaceBox(14, 8, hugButtons(rows)...)
}

func powerModeLabel(mode power.Mode) string {
	switch mode {
	case power.FollowSystem:
		return "Follow System"
	case power.Balanced:
		return "Balanced"
	case power.Saver:
		return "Blur Saver"
	case power.Performance:
		return "Performance"
	default:
		return "Unknown"
	}
}

func powerModeDescription(mode power.Mode) string {
	switch mode {
	case power.FollowSystem:
		return "Uses Blur Saver only when the operating system reports battery-saver mode; otherwise Balanced."
	case power.Balanced:
		return "Allows one bounded local recap job at a time, within the token and time limits below."
	case power.Saver:
		return "Pauses optional hourly recaps; user-started work and capture continue."
	case power.Performance:
		return "Allows up to two bounded local recap jobs at a time, within the limits below."
	default:
		return "Select a power mode."
	}
}

func powerBudgetDraftValues(config power.Config) (jobs, tokens, seconds string) {
	effective, _ := power.Resolve(power.Config{Mode: config.Mode}, power.SystemState{})
	if effective.MaxBackgroundAIJobs == 0 {
		effective, _ = power.Resolve(power.Config{Mode: power.Balanced}, power.SystemState{})
	}
	if config.MaxBackgroundAIJobs > 0 {
		effective.MaxBackgroundAIJobs = config.MaxBackgroundAIJobs
	}
	if config.MaxBackgroundTokens > 0 {
		effective.MaxBackgroundTokens = config.MaxBackgroundTokens
	}
	if config.MaxBackgroundDuration > 0 {
		effective.MaxBackgroundDuration = config.MaxBackgroundDuration
	}
	return strconv.Itoa(effective.MaxBackgroundAIJobs), strconv.Itoa(effective.MaxBackgroundTokens), strconv.Itoa(int(effective.MaxBackgroundDuration / time.Second))
}

func parsePowerBudget(mode power.Mode, jobsText, tokensText, secondsText string) (power.Config, bool) {
	if mode == power.Saver {
		return power.Config{Mode: mode}, true
	}
	jobs, jobsErr := strconv.Atoi(strings.TrimSpace(jobsText))
	tokens, tokensErr := strconv.Atoi(strings.TrimSpace(tokensText))
	seconds, secondsErr := strconv.Atoi(strings.TrimSpace(secondsText))
	if jobsErr != nil || tokensErr != nil || secondsErr != nil {
		return power.Config{}, false
	}
	defaults, err := power.Resolve(power.Config{Mode: mode}, power.SystemState{})
	if err != nil || defaults.MaxBackgroundAIJobs == 0 || jobs < 1 || jobs > defaults.MaxBackgroundAIJobs || tokens < 1 || tokens > defaults.MaxBackgroundTokens || seconds < 1 || seconds > int(defaults.MaxBackgroundDuration/time.Second) {
		return power.Config{}, false
	}
	return power.Config{Mode: mode, MaxBackgroundAIJobs: jobs, MaxBackgroundTokens: tokens, MaxBackgroundDuration: time.Duration(seconds) * time.Second}, true
}

func powerConfigChanged(current, draft power.Config) bool {
	normalize := func(config power.Config) power.Config {
		if config.Mode == power.Saver {
			return power.Config{Mode: power.Saver}
		}
		defaults, err := power.Resolve(power.Config{Mode: config.Mode}, power.SystemState{})
		if err != nil {
			return config
		}
		if config.MaxBackgroundAIJobs == 0 {
			config.MaxBackgroundAIJobs = defaults.MaxBackgroundAIJobs
		}
		if config.MaxBackgroundTokens == 0 {
			config.MaxBackgroundTokens = defaults.MaxBackgroundTokens
		}
		if config.MaxBackgroundDuration == 0 {
			config.MaxBackgroundDuration = defaults.MaxBackgroundDuration
		}
		return config
	}
	return normalize(current) != normalize(draft)
}

func powerBudgetHint(mode power.Mode) string {
	if mode == power.Saver {
		return "Blur Saver pauses optional hourly recaps; user-started requests remain available."
	}
	defaults, err := power.Resolve(power.Config{Mode: mode}, power.SystemState{})
	if err != nil || defaults.MaxBackgroundAIJobs == 0 {
		return "Choose a valid power mode to set background limits."
	}
	return fmt.Sprintf("Allowed limits: 1–%d jobs, 1–%d tokens, and 1–%d seconds.", defaults.MaxBackgroundAIJobs, defaults.MaxBackgroundTokens, int(defaults.MaxBackgroundDuration/time.Second))
}

func powerStateSummary(effective power.Effective, err error) string {
	if effective.RequestedMode == "" {
		return "Power settings need repair; save a mode to restore the policy."
	}
	if effective.RequestedMode == power.FollowSystem {
		if effective.System.Known {
			if effective.System.SaverEnabled {
				return "System battery-saver mode is on; Blur Saver is active."
			}
			return "System battery-saver mode is off; Balanced is active."
		}
		return "System power state is unavailable; Follow System is using Balanced."
	}
	if err != nil {
		return "System power state is unavailable; your selected mode remains active."
	}
	return "Manual mode: " + powerModeLabel(effective.Mode) + "."
}

func (d *desktop) savePowerMode() {
	if d == nil || d.client == nil || !d.client.CanConfigurePower() || d.loading || d.powerSaving {
		return
	}
	config, valid := parsePowerBudget(d.powerDraftMode, d.powerJobsDraft, d.powerTokensDraft, d.powerSecondsDraft)
	if !valid {
		return
	}
	d.powerSaving = true
	d.powerStatus = "Saving power mode…"
	d.app.RequestUpdate()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := d.client.ConfigurePower(ctx, config)
		cancel()
		d.app.Post(func() {
			d.powerSaving = false
			if err != nil {
				d.powerStatus = "Power mode was not saved. Try again."
			} else {
				d.powerConfig, d.powerNeedsRepair = config, false
				d.powerStatus = "Power mode saved."
			}
			if d.quickNotePending == false && d.status != "" {
				d.resizeQuickPanel(d.askResult != nil)
			}
			d.app.RequestUpdate()
		})
	}()
}

func (d *desktop) refreshPowerState() {
	if d == nil || d.client == nil || !d.client.CanConfigurePower() || d.powerChecking || d.powerSaving {
		return
	}
	d.powerChecking = true
	d.powerStatus = "Checking the system power mode…"
	d.app.RequestUpdate()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		effective, err := d.client.EffectivePower(ctx)
		cancel()
		d.app.Post(func() {
			d.powerChecking = false
			d.powerStatus = powerStateSummary(effective, err)
			d.app.RequestUpdate()
		})
	}()
}

func deleteAllActionLabel(confirmed bool) string {
	if confirmed {
		return "Delete permanently"
	}
	return "Delete all memory"
}

func deleteAllConfirmationCopy() string {
	return "Permanently deletes saved notes, knowledge library entries, captured sources, and derived recap items. Your encryption key and AI settings remain. This cannot be undone."
}

var retentionOptions = []int{0, 30, 90, 180, 365}

func retentionOptionLabel(days int, selected bool) string {
	label := "Off"
	switch days {
	case 30:
		label = "30 days"
	case 90:
		label = "90 days"
	case 180:
		label = "180 days"
	case 365:
		label = "1 year"
	}
	return label
}

// retentionCurrentLabel names the active policy, or says it is unknown when it
// could not be read, so an unread policy is never shown as "Off".
func retentionCurrentLabel(days int, unknown bool) string {
	if unknown {
		return "Currently unknown"
	}
	return "Currently " + retentionOptionLabel(days, false)
}

// retentionChangeEnabled blocks changes while the policy could not be read and
// the draft would still be the zero value ("Off").
func retentionChangeEnabled(loading, unknown bool, draftDays int) bool {
	return !loading && !(unknown && draftDays == 0)
}

func retentionApplyLabel(days int) string {
	if days == 0 {
		return "Turn off automatic pruning"
	}
	return "Apply and prune now"
}

func retentionConfirmationCopy(days int) string {
	if days == 0 {
		return "Automatic pruning will stop. Existing memory will remain until you delete it yourself."
	}
	return fmt.Sprintf("Blur will permanently delete captured sources and derived recap items older than %d days now. Records from before this version use the upgrade date as their age baseline. This cannot be undone.", days)
}

func (d *desktop) retentionView() ui.View {
	rows := []ui.View{wrapLabel("Memory retention").Style(styleSectionTitle)}
	rows = append(rows, d.groupCopy("Memory retention", retentionCurrentLabel(d.retentionDays, d.retentionUnknown)+".",
		"Unconfigured stores keep 90 days by default; choose Off only if you want memory kept indefinitely. Pruning runs at launch and daily while Blur is open, with failed sweeps retried every 15 minutes.",
		"Capture times are encrypted. Existing records use the date Blur is first upgraded, so they are never expired based on an unknown age.")...)
	first := make([]ui.View, 0, 3)
	second := make([]ui.View, 0, 2)
	for i, days := range retentionOptions {
		button := choiceButton(retentionOptionLabel(days, d.retentionDraft == days), d.retentionDraft == days).Enabled(!d.loading).OnClick(func() {
			d.retentionDraft = days
			d.pendingRetention = false
			d.app.RequestUpdate()
		})
		if i < 3 {
			first = append(first, button)
		} else {
			second = append(second, button)
		}
	}
	rows = append(rows, ui.HBox(first...).Spacing(8), ui.HBox(second...).Spacing(8))
	if d.pendingRetention {
		rows = append(rows,
			wrapLabel(retentionConfirmationCopy(d.retentionDraft)).Style(modern.MutedText),
			ui.HBox(
				flatButton("Cancel").Enabled(!d.loading).OnClick(func() { d.pendingRetention = false; d.app.RequestUpdate() }),
				primaryButton(retentionApplyLabel(d.retentionDraft)).Enabled(retentionChangeEnabled(d.loading, d.retentionUnknown, d.retentionDraft)).OnClick(d.applyRetention),
			).Spacing(8),
		)
	} else {
		label := "Review retention change"
		if d.retentionDraft > 0 {
			label = "Review and prune now"
		}
		rows = append(rows, secondaryButton(label).Enabled(retentionChangeEnabled(d.loading, d.retentionUnknown, d.retentionDraft)).OnClick(func() { d.pendingRetention = true; d.app.RequestUpdate() }))
	}
	if d.retentionError {
		rows = append(rows, wrapLabel("The last retention sweep did not finish. Blur will retry; memory has not been marked as fully pruned.").Style(modern.MutedText))
	}
	return surfaceBox(14, 8, hugButtons(rows)...)
}

func (d *desktop) applyRetention() {
	if d == nil || !retentionChangeEnabled(d.loading, d.retentionUnknown, d.retentionDraft) || !d.pendingRetention || d.client == nil || !d.client.CanConfigureRetention() {
		return
	}
	days := d.retentionDraft
	d.pendingRetention = false
	d.loading = true
	d.status = "Updating memory retention…"
	d.app.RequestUpdate()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		err := d.client.ConfigureRetentionDays(ctx, days)
		removed := 0
		if err == nil && days > 0 {
			removed, err = d.client.RunRetentionNow(ctx, time.Now())
		}
		policy, policyErr := d.client.RetentionPolicy(ctx)
		cancel()
		d.app.Post(func() {
			d.loading = false
			if policyErr == nil {
				d.retentionDays, d.retentionDraft, d.retentionUnknown = policy.Days, policy.Days, false
			}
			if err != nil || policyErr != nil {
				d.retentionError = true
				d.status = "Retention could not finish. Blur will retry; any completed deletions were permanent."
				if removed > 0 {
					d.refreshAfterMemoryChange()
				}
			} else {
				d.retentionError = false
				if days == 0 {
					d.status = "Automatic memory pruning is off. Existing memory was kept."
				} else {
					d.status = fmt.Sprintf("Retention is set to %d days. Pruned %d expired source(s) and their recap data.", days, removed)
				}
				if removed > 0 {
					d.refreshAfterMemoryChange()
				}
			}
			d.app.RequestUpdate()
		})
	}()
}

func (d *desktop) refreshAfterMemoryChange() {
	d.invalidateMemoryViews()
	d.answers = make(map[string]string)
	switch d.page {
	case "Today":
		d.loadToday()
	case "Search":
		if strings.TrimSpace(d.query) != "" && !d.loading {
			d.runSearch()
		}
	case "Library":
		d.loadLibrary()
	case "Time":
		if !d.loading {
			d.loadActivityReport()
		}
	}
}

func (d *desktop) invalidateMemoryViews() {
	if d == nil {
		return
	}
	d.memoryGeneration++
	d.todayRequestID++
	d.hourlyRequestID++
	d.todayLoading = false
	d.today = TodayView{}
	d.hourlyLoading, d.hourlyError = false, false
	d.hourlySummaries = nil
	d.expandedChronicleID = ""
	d.chronicleSources = nil
	d.chronicleSourcesLoading = false
	d.clearKnowledgeSuggestions()
	d.searchHits = nil
	d.searchComplete, d.searchFailed = false, false
	d.expandedSourceID = ""
	d.library = knowledge.Page{}
	d.libraryLoaded = false
	d.libraryLoading = false
	d.report = activity.ReportResult{}
	d.reportReady = false
	d.invalidateAsk()
	d.loading = false
	if d.status == "Searching locally…" || d.status == "Reading the selected dates locally…" {
		d.status = ""
	}
}

func (d *desktop) deleteAllMemory() {
	if d == nil || d.loading || d.knowledgeSaving || !d.pendingDeleteAll || d.client == nil || !d.client.CanDeleteAll() {
		return
	}
	d.invalidateAsk()
	wasCapturing := d.capture != nil && d.capture.Running()
	d.loading = true
	d.status = "Deleting all stored memory…"
	d.pendingDeleteAll = false
	d.app.RequestUpdate()
	go func() {
		var removed int
		var err error
		pauseFailed := false
		if wasCapturing {
			err = d.capture.Pause(context.Background())
			pauseFailed = err != nil
		}
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			removed, err = d.client.DeleteAll(ctx)
			cancel()
		}
		d.app.Post(func() {
			d.loading = false
			if err != nil {
				if pauseFailed {
					d.status = "Capture could not be paused, so no memory was deleted. Pause capture and retry."
				} else {
					d.status = "Could not finish deleting memory. Completed deletions are permanent; capture remains paused. Retry to continue. " + friendlyError(err)
					d.markCapturePaused(wasCapturing)
					d.today = TodayView{}
					d.searchHits = nil
					d.askResult = nil
					d.expandedAnswer = false
					d.library = knowledge.Page{}
					d.libraryLoaded = false
					d.justAdded = nil
					d.report = activity.ReportResult{}
					d.reportReady = false
					d.answers = make(map[string]string)
					d.loadToday()
					if d.page == "Library" {
						d.loadLibrary()
					}
				}
			} else {
				d.markCapturePaused(wasCapturing)
				d.today = TodayView{}
				d.searchHits = nil
				d.askResult = nil
				d.expandedAnswer = false
				d.pendingDelete = ""
				d.library = knowledge.Page{}
				d.libraryLoaded = false
				d.justAdded = nil
				d.report = activity.ReportResult{}
				d.reportReady = false
				d.answers = make(map[string]string)
				d.status = fmt.Sprintf("Deleted %d stored source(s), library entries, and derived items. The encryption key and AI settings are unchanged.", removed)
				d.loadToday()
				if d.page == "Library" {
					d.loadLibrary()
				}
			}
			d.app.RequestUpdate()
		})
	}()
}

func browserCaptureDisclosure() string {
	return "Active browser pages are captured via Accessibility; address bars, tabs, and form fields are omitted. Known private-window markers are skipped, but detection varies by browser and locale. Blur never navigates or reads history. Safari extension capture is a separate optional signed build."
}

func captureDescription(running, enabled bool) string {
	if running {
		return "Capture is on for the currently focused app. Blur never navigates its history."
	}
	if enabled {
		return "Capture starts automatically when Blur opens. Accessibility access may be required; Blur never requests it automatically."
	}
	return "Capture is paused. Start it any time. Blur reads only the currently focused app and never navigates its history."
}

func shouldOfferCaptureSetup(todayState string, captureAvailable, captureRunning bool) bool {
	return todayState == "empty" && captureAvailable && !captureRunning
}

func (d *desktop) localAISettingsView() ui.View {
	savedSettings := LocalAISettings{}
	if d.client != nil {
		savedSettings = d.client.LocalAISettings()
	}
	state := "Off. No model requests are made."
	if d.localAIEnabled {
		if effectiveProvider(savedSettings) == ProviderOpenAICompatible {
			state = "On. Sanitized quick notes are processed when you save them."
			if savedSettings.RemoteAskEnabled {
				state += " Remote Ask is on for the saved endpoint."
			} else {
				state += " Remote Ask is off."
			}
		} else {
			state = "On. Notes and explicit memory questions use this local Ollama server."
		}
	}
	if d.aiNeedsRepair {
		state = "Saved model settings could not be read. AI is off; save valid local settings to repair them."
	}
	if d.client == nil || d.client.localAI == nil {
		return ui.VBox(wrapLabel("Local note model").Style(styleSectionTitle), wrapLabel("Model settings are unavailable on this build."), wrapLabel(state).Style(modern.MutedText)).Spacing(8).Padding(14).Style(modern.Card)
	}
	rows := []ui.View{
		wrapLabel("Note processing model").Style(styleSectionTitle),
		wrapLabel(state).Style(modern.MutedText),
		wrapLabel(localAIDataFlowCopy(d.localAIEnabled && !d.aiNeedsRepair, effectiveProvider(savedSettings), savedSettings.RemoteAskEnabled)).Style(modern.MutedText),
		ui.HBox(
			choiceButton("Local Ollama", d.modelProvider == ProviderOllama).OnClick(func() {
				if !providerSelectionChanges(d.modelProvider, ProviderOllama) {
					return
				}
				d.modelProvider = ProviderOllama
				d.remoteAskEnabled = false
				d.modelEndpoint = "http://127.0.0.1:11434"
				d.app.RequestUpdate()
			}),
			choiceButton("Remote OpenAI-compatible", d.modelProvider == ProviderOpenAICompatible).OnClick(func() {
				if !providerSelectionChanges(d.modelProvider, ProviderOpenAICompatible) {
					return
				}
				defaultEndpoint := "https://api.openai.com/v1"
				saved := d.client.LocalAISettings()
				d.remoteAskEnabled = saved.Provider == ProviderOpenAICompatible && saved.Endpoint == defaultEndpoint && saved.Model == strings.TrimSpace(d.modelName) && saved.RemoteAskEnabled
				d.modelProvider = ProviderOpenAICompatible
				d.modelEndpoint = defaultEndpoint
				d.app.RequestUpdate()
			}),
		).Spacing(8),
		ui.TextInput().Text(d.modelEndpoint).Placeholder("http://127.0.0.1:11434").OnText(func(value string) {
			if value != d.modelEndpoint {
				d.remoteAskEnabled = false
			}
			d.modelEndpoint = value
			d.app.RequestUpdate()
		}),
		ui.TextInput().Text(d.modelName).Placeholder("Model name").OnText(func(value string) {
			if value != d.modelName {
				d.remoteAskEnabled = false
			}
			d.modelName = value
			d.app.RequestUpdate()
		}),
	}
	if !d.loading {
		if notice := localAISettingsDraftNotice(savedSettings, d.modelProvider, d.modelEndpoint, d.modelName, d.remoteAskEnabled); notice != "" {
			rows = append(rows, wrapLabel(notice).Style(modern.MutedText))
		}
	}
	if d.modelProvider == ProviderOpenAICompatible {
		rows = append(rows,
			ui.TextInput().Text(d.modelCredential).Placeholder("API key (kept in OS keychain)").Obscured(true).OnText(func(value string) { d.modelCredential = value; d.app.RequestUpdate() }),
			wrapLabel(remoteAskConsentCopy(savedSettings.RemoteAskEnabled)).Style(modern.MutedText),
		)
		askConsentReady := d.localAIEnabled && !d.aiNeedsRepair && !d.loading && savedSettings.Provider == ProviderOpenAICompatible && savedSettings.Endpoint == strings.TrimSpace(d.modelEndpoint) && savedSettings.Model == strings.TrimSpace(d.modelName)
		askConsentLabel, askConsentPrimary := "Save provider settings before changing Ask consent", false
		if askConsentReady && savedSettings.RemoteAskEnabled {
			askConsentLabel = "Disable remote Ask"
		} else if askConsentReady {
			askConsentLabel, askConsentPrimary = "Allow Ask to send matching sources to this endpoint", true
		}
		askConsent := flatButton(askConsentLabel)
		if askConsentPrimary {
			askConsent = primaryButton(askConsentLabel)
		}
		rows = append(rows, hug(askConsent.Enabled(askConsentReady).OnClick(func() {
			d.remoteAskEnabled = !savedSettings.RemoteAskEnabled
			d.configureLocalAI(true)
		})))
	}
	if d.localAIEnabled {
		rows = append(rows,
			primaryButton("Save and enable").Enabled(!d.loading).OnClick(func() { d.configureLocalAI(true) }),
			flatButton("Disable model").Enabled(!d.loading).OnClick(func() { d.configureLocalAI(false) }),
		)
	} else {
		label := "Enable local model"
		if d.modelProvider == ProviderOpenAICompatible {
			label = "Enable remote provider & send notes"
		}
		rows = append(rows, primaryButton(label).Enabled(!d.loading && strings.TrimSpace(d.modelName) != "").OnClick(func() { d.configureLocalAI(true) }))
	}
	return surfaceBox(14, 8, hugButtons(rows)...)
}

func remoteAskConsentCopy(enabled bool) string {
	if enabled {
		return "Remote Ask is on: your question and up to eight matching captured sources go to the saved endpoint only when you ask. With no matches, only your question is sent. The provider may retain requests."
	}
	return "Remote Ask is off. Allowing it sends your question and up to eight matching captured sources to this endpoint only when you ask. With no matches, only your question is sent; the provider may retain requests."
}

func localAISettingsDraftNotice(saved LocalAISettings, provider, endpoint, model string, remoteAskDraft bool) string {
	provider = strings.TrimSpace(provider)
	endpoint = strings.TrimSpace(endpoint)
	model = strings.TrimSpace(model)
	askConsent := provider == ProviderOpenAICompatible && remoteAskDraft
	targetChanged := effectiveProvider(saved) != provider || saved.Endpoint != endpoint || saved.Model != model
	if !targetChanged && saved.RemoteAskEnabled == askConsent {
		return ""
	}
	if !saved.Enabled {
		return "Unsaved model settings. No model requests start until you save and enable them."
	}
	if saved.RemoteAskEnabled {
		if targetChanged {
			return "Unsaved model settings. The saved model remains active, and remote Ask remains active for the saved endpoint until you save. Saving a different endpoint or model turns it off."
		}
		return "Unsaved model settings. The saved model and its remote Ask permission remain active until the change is saved."
	}
	return "Unsaved model settings. The saved model remains active until you save."
}

func (d *desktop) chronicleSettingsView() ui.View {
	ready := d.localAIEnabled && !d.aiNeedsRepair && d.modelProvider == ProviderOllama
	state := "Off. Blur won’t automatically send captured text for summaries; Ask remains user-started."
	if d.backgroundSummaries {
		state = "On. Completed hours are summarized only by Ollama on this device."
	}
	if !ready && !d.backgroundSummaries {
		state += " Enable local Ollama first."
	}
	label := "Enable hourly recaps"
	if d.backgroundSummaries {
		label = "Pause hourly recaps"
	}
	rows := []ui.View{wrapLabel("Hourly recaps").Style(styleSectionTitle)}
	rows = append(rows, d.groupCopy("Hourly recaps", state, "Only sanitized sources from the completed hour are used. Recaps stay encrypted here and are removed when any cited source is deleted. No remote provider is used.")...)
	rows = append(rows, flatButton(label).Enabled(!d.loading && (ready || d.backgroundSummaries)).OnClick(d.toggleBackgroundSummaries))
	return surfaceBox(14, 7, hugButtons(rows)...)
}

func (d *desktop) toggleBackgroundSummaries() {
	if d == nil || d.client == nil || d.loading {
		return
	}
	enabled := !d.backgroundSummaries
	d.loading = true
	d.status = "Updating hourly recap preference…"
	d.app.RequestUpdate()
	go func() {
		err := d.client.SetBackgroundSummaries(context.Background(), enabled)
		d.app.Post(func() {
			d.loading = false
			if err != nil {
				d.status = backgroundSummariesErrorStatus(enabled, err)
			} else {
				d.backgroundSummaries = enabled
				if enabled {
					d.status = "Hourly recaps enabled for future captured hours on this device."
				} else {
					d.status = "Hourly recaps paused. Existing recaps remain in encrypted memory until deleted."
				}
			}
			d.app.RequestUpdate()
		})
	}()
}

// backgroundSummariesErrorStatus explains why the hourly recap preference was
// not changed; only an unavailable-settings failure while enabling points the
// user at local Ollama.
func backgroundSummariesErrorStatus(enabled bool, err error) string {
	switch {
	case errors.Is(err, errLocalAIUnavailable) && enabled:
		return "Hourly recap setting was not changed. Enable local Ollama first."
	case errors.Is(err, errLocalAIUnavailable):
		return "Hourly recaps could not be paused because the setting could not be saved. Try again."
	}
	return "Hourly recap setting was not changed. Try again."
}

// backgroundSummariesPausedNote is appended to a save status when saving
// switched hourly recaps off as a side effect.
func backgroundSummariesPausedNote(before, after bool) string {
	if before && !after {
		return " Hourly recaps were paused."
	}
	return ""
}

func localAIDataFlowCopy(enabled bool, provider string, remoteAskConsent ...bool) string {
	if !enabled {
		return "No AI requests are made while the model is off. Your saved notes and captured sources remain on this device."
	}
	if provider == ProviderOpenAICompatible {
		if len(remoteAskConsent) > 0 && remoteAskConsent[0] {
			return "Sanitized notes you save, and your question plus up to eight matching captured sources when you explicitly ask, go to the configured remote provider. With no matches, only your question is sent. It may retain requests."
		}
		return "Only sanitized notes you save are sent to the configured remote provider, which may retain requests. Captured sources are never sent remotely unless you separately enable remote Ask."
	}
	return "Sanitized notes are processed when you save them. When you explicitly ask, your question plus any matching captured sources go to loopback Ollama on this device. With no matches, only your question is sent; Ollama may retain request logs or model state."
}

func (d *desktop) configureLocalAI(enabled bool) {
	if d == nil || d.client == nil || d.loading {
		return
	}
	d.invalidateAsk()
	settings := d.client.LocalAISettings()
	settings.Enabled = enabled
	settings.RemoteAskEnabled = enabled && d.modelProvider == ProviderOpenAICompatible && d.remoteAskEnabled
	hadBackgroundSummaries := settings.BackgroundSummaries
	if !enabled || d.modelProvider != ProviderOllama {
		settings.BackgroundSummaries = false
	}
	if enabled {
		settings.Endpoint, settings.Model = strings.TrimSpace(d.modelEndpoint), strings.TrimSpace(d.modelName)
		settings.Provider = d.modelProvider
	}
	d.loading = true
	provider := d.modelProvider
	credential := d.modelCredential
	if enabled {
		d.status = "Saving AI settings…"
	} else {
		d.status = "Disabling AI model…"
	}
	d.app.RequestUpdate()
	go func() {
		var err error
		if provider == ProviderOpenAICompatible {
			err = d.client.ConfigureAI(context.Background(), settings, credential)
		} else {
			err = d.client.ConfigureLocalAI(context.Background(), settings)
		}
		d.app.Post(func() {
			d.loading = false
			d.localAIEnabled = d.client.LocalAIEnabled()
			d.aiNeedsRepair = d.client.LocalAISettingsNeedRepair()
			d.backgroundSummaries = d.client.LocalAISettings().BackgroundSummaries
			d.remoteAskEnabled = d.client.LocalAISettings().RemoteAskEnabled
			if err == nil {
				d.modelCredential = ""
			}
			if err != nil {
				d.status = "Settings were not saved. Check the endpoint, model name, and provider credential."
			} else if enabled {
				if provider == ProviderOpenAICompatible {
					if d.client.LocalAISettings().RemoteAskEnabled {
						d.status = "Remote provider enabled. Saved notes and matching sources for explicit Ask go to the configured endpoint."
					} else {
						d.status = "Remote provider enabled. Only sanitized quick-note text is sent when you save a note."
					}
				} else {
					d.status = "Local model enabled. Blur sends sanitized quick-note text only to this device."
				}
			} else {
				d.status = "Model disabled. Active note requests were canceled."
			}
			if err == nil {
				d.status += backgroundSummariesPausedNote(hadBackgroundSummaries, d.backgroundSummaries)
			}
			d.app.RequestUpdate()
		})
	}()
}

// providerSelectionChanges is false when the clicked provider is already
// selected, so the typed endpoint and consent state are left alone.
func providerSelectionChanges(current, clicked string) bool { return current != clicked }

func (d *desktop) toggleCapture() {
	if d.capture == nil || d.loading {
		return
	}
	wasRunning := d.capture.Running()
	wantEnabled := !wasRunning
	action := "resume"
	if wasRunning {
		action = "pause"
	}
	slog.Info("capture menu action requested", "action", action)
	previousConfig := d.preferences
	config := previousConfig
	config.CaptureEnabled = wantEnabled
	d.loading = true
	if wasRunning {
		d.status = "Pausing capture…"
	} else {
		d.status = "Checking Accessibility permission…"
	}
	d.app.RequestUpdate()
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.Error("capture toggle goroutine panicked", "panic_type", fmt.Sprintf("%T", recovered), "stack", string(debug.Stack()))
				_ = d.client.ConfigureApplicationPreferences(context.Background(), previousConfig)
				d.app.Post(func() {
					d.loading = false
					d.status = "Capture action failed unexpectedly. Check Blur’s log before retrying."
					d.updateMenuBarCaptureState()
					d.app.RequestUpdate()
				})
			}
		}()
		var report platform.CapabilityReport
		configureCtx, cancelConfigure := context.WithTimeout(context.Background(), 8*time.Second)
		err := d.client.ConfigureApplicationPreferences(configureCtx, config)
		cancelConfigure()
		if err != nil {
			slog.Error("capture preference update failed", "stage", "configure", "error", err)
		}
		if err == nil && wasRunning {
			pauseCtx, cancelPause := context.WithTimeout(context.Background(), 8*time.Second)
			err = d.capture.Pause(pauseCtx)
			cancelPause()
			if err != nil {
				slog.Error("capture worker did not pause cleanly", "stage", "pause", "error", err)
			}
		} else if err == nil {
			capabilityCtx, cancelCapability := context.WithTimeout(context.Background(), 8*time.Second)
			report, err = d.capture.Capability(capabilityCtx)
			cancelCapability()
			slog.Info("capture capability checked", "state", report.State, "error", err)
			if err == nil && report.State != platform.Available {
				switch report.State {
				case platform.PermissionRequired:
					err = errCapturePermissionRequired
				default:
					err = errCaptureUnavailable
				}
			}
			if err == nil {
				// The worker context must outlive this menu action. The controller
				// owns its cancellation and will stop it only on an explicit pause.
				err = d.capture.Start(context.Background())
				if err != nil {
					slog.Error("capture worker start failed", "error", err)
				} else {
					slog.Info("capture worker started")
					d.app.Post(d.noteCaptureStarted)
				}
			}
		}
		if err != nil && !errors.Is(err, errCapturePermissionRequired) && !errors.Is(err, errCaptureUnavailable) {
			rollbackCtx, cancelRollback := context.WithTimeout(context.Background(), 3*time.Second)
			if rollbackErr := d.client.ConfigureApplicationPreferences(rollbackCtx, previousConfig); rollbackErr != nil {
				slog.Error("capture preference rollback failed", "stage", "rollback", "error", rollbackErr)
			}
			cancelRollback()
		}
		d.app.Post(func() {
			d.loading = false
			d.captureState = captureAvailabilityState(report, d.capture.Running(), err)
			if err == nil || errors.Is(err, errCapturePermissionRequired) || errors.Is(err, errCaptureUnavailable) {
				d.preferences.CaptureEnabled = wantEnabled
			}
			switch {
			case err == nil && d.capture.Running():
				d.status = "Capture is on for the currently focused app."
			case err == nil && wantEnabled:
				d.captureState = "unavailable"
				d.status = "Capture stopped unexpectedly. Check Settings, then resume capture."
			case err == nil:
				d.status = "Capture is paused."
			case errors.Is(err, errCapturePermissionRequired):
				d.setupRequired = true
				d.startPermissionWatch()
				d.status = "Enable Accessibility for Blur in System Settings. Blur will check automatically and resume capture when permission is available."
			case errors.Is(err, errCaptureUnavailable):
				d.status = "Capture is enabled but unavailable. Check accessibility availability and try again."
			case errors.Is(err, context.DeadlineExceeded):
				d.status = "Capture did not respond in time. Its setting was restored; check Blur’s log before retrying."
			default:
				d.status = "Capture could not be changed. Its setting was restored; check Blur’s log before retrying."
			}
			d.updateMenuBarCaptureState()
			if err != nil {
				slog.Warn("capture menu action completed with error", "running", d.capture.Running(), "error", err)
			} else {
				slog.Info("capture menu action completed", "running", d.capture.Running())
			}
			if err == nil {
				d.loadToday()
			}
			d.app.RequestUpdate()
		})
	}()
}

func (d *desktop) openSettingsFromMenu() {
	if d == nil || d.app == nil {
		return
	}
	d.app.Post(func() {
		d.dashboardOpen = true
		d.openSettings(d.settingsGroup)
	})
}

func (d *desktop) toggleCaptureFromMenu() {
	if d == nil || d.app == nil {
		return
	}
	d.app.Post(func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.Error("menu capture action panicked", "panic_type", fmt.Sprintf("%T", recovered), "stack", string(debug.Stack()))
				d.loading = false
				d.status = "Capture action failed unexpectedly. Check Blur’s log before retrying."
				d.app.RequestUpdate()
			}
		}()
		d.toggleCapture()
	})
}

func (d *desktop) toggleSafariPageCapture() {
	if d == nil || d.capture == nil || d.loading || !safariPageCaptureToggleAllowed(d.preferences.SafariPageCaptureEnabled, safariPageCaptureBuildConfigured()) {
		return
	}
	previous := d.preferences
	next := previous
	next.SafariPageCaptureEnabled = !previous.SafariPageCaptureEnabled
	wasRunning := d.capture.Running()
	d.loading = true
	if next.SafariPageCaptureEnabled {
		d.status = "Enabling Safari page capture…"
	} else {
		d.status = "Disabling Safari page capture…"
	}
	d.app.RequestUpdate()
	go func() {
		ctx := context.Background()
		err := d.client.ConfigureApplicationPreferences(ctx, next)
		if err == nil && wasRunning {
			err = d.capture.Pause(ctx)
			if err == nil {
				err = d.capture.Start(ctx)
				if err == nil {
					d.app.Post(d.noteCaptureStarted)
				}
			}
		}
		if err != nil {
			_ = d.client.ConfigureApplicationPreferences(context.Background(), previous)
			if wasRunning && !d.capture.Running() {
				d.startCaptureNoted(context.Background())
			}
		}
		d.app.Post(func() {
			d.loading = false
			if err == nil {
				d.preferences = next
				if next.SafariPageCaptureEnabled {
					d.status = "Safari page capture enabled for active, non-private tabs."
				} else {
					d.status = "Safari page capture disabled."
				}
			} else {
				d.preferences = previous
				d.status = "Safari capture setting could not be applied; the previous setting was restored."
			}
			d.app.RequestUpdate()
		})
	}()
}

func (d *desktop) openAccessibilitySettings() {
	if d == nil || d.app == nil || d.loading || d.captureState != "permission_required" {
		return
	}
	if err := openDesktopAccessibilitySettings(); err != nil {
		d.status = "System Settings could not be opened. Enable Blur under Privacy & Security → Accessibility, then retry capture."
	} else {
		d.status = "Enable Blur under Privacy & Security → Accessibility, then return to Blur. It will check automatically."
	}
	d.app.RequestUpdate()
}

func (d *desktop) toggleMenuBarItem() {
	if d == nil || d.client == nil || d.app == nil || d.loading {
		return
	}
	previous := d.preferences
	next := previous
	next.MenuBarItemEnabled = !previous.MenuBarItemEnabled
	if err := setDesktopMenuBarVisible(d.app, d.ensurePauseScheduler(), next.MenuBarItemEnabled, d.openQuickPanel, d.openSettingsFromMenu, d.toggleCaptureFromMenu); err != nil {
		d.status = "The menu bar item could not be changed."
		d.app.RequestUpdate()
		return
	}
	if err := d.client.ConfigureApplicationPreferences(context.Background(), next); err != nil {
		_ = setDesktopMenuBarVisible(d.app, d.ensurePauseScheduler(), previous.MenuBarItemEnabled, d.openQuickPanel, d.openSettingsFromMenu, d.toggleCaptureFromMenu)
		d.status = "The menu bar preference could not be saved."
		d.app.RequestUpdate()
		return
	}
	d.preferences = next
	d.status = "Menu bar setting saved."
	d.app.RequestUpdate()
}

func (d *desktop) restoreDefaultPreferences() {
	if d == nil || d.client == nil || d.app == nil || d.loading {
		return
	}
	defaults := preferences.DefaultConfig()
	if err := d.client.ConfigureApplicationPreferences(context.Background(), defaults); err != nil {
		d.status = "Default preferences could not be saved."
		d.app.RequestUpdate()
		return
	}
	if err := setDesktopMenuBarVisible(d.app, d.ensurePauseScheduler(), defaults.MenuBarItemEnabled, d.openQuickPanel, d.openSettingsFromMenu, d.toggleCaptureFromMenu); err != nil {
		d.status = "Preferences were restored, but the menu bar item could not be shown."
	}
	d.preferences = defaults
	d.applyQuickAskHotkey()
	d.captureDefaultDisabledDraft = defaults.CaptureDefaultDisabled
	d.excludedAppsDraft = strings.Join(defaults.ExcludedApps, "\n")
	d.excludedDomainsDraft = strings.Join(defaults.ExcludedDomains, "\n")
	d.includedAppsDraft = strings.Join(defaults.IncludedApps, "\n")
	d.includedDomainsDraft = strings.Join(defaults.IncludedDomains, "\n")
	d.mcpStatus = ""
	d.preferencesNeedsRepair = false
	d.status = "Default preferences restored. Capture will start when available."
	if defaults.CaptureEnabled {
		d.startCaptureOnLaunch()
	}
	d.app.RequestUpdate()
}

func friendlyError(err error) string {
	if errors.Is(err, ErrNotConfigured) {
		return "Encrypted local storage is not set up; nothing was saved."
	}
	if errors.Is(err, ErrInvalidRequest) || errors.Is(err, notes.ErrInvalid) {
		return "That input could not be accepted. Check its length and try again."
	}
	return "Blur could not complete that local operation. If it was a note, it remains saved when storage had already accepted it."
}
