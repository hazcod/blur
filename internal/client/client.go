// Package macosclient contains the bounded, content-only API exposed to the
// native macOS shell. It has no capture or provider controls.
package client

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"sync"
	time "time"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/chronicle"
	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/knowledge/derive"
	"github.com/hazcod/blur/internal/model"
	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/power"
	"github.com/hazcod/blur/internal/preferences"
	"github.com/hazcod/blur/internal/retention"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/storage"
	"github.com/hazcod/blur/internal/storage/durable"
	"github.com/hazcod/blur/internal/today"
)

const (
	MaxQueryBytes    = 512
	MaxSearchResults = 50
	MaxSearchBytes   = 2 << 20
)

var (
	ErrInvalidRequest = errors.New("macos client: invalid request")
	ErrNotConfigured  = errors.New("macos client: local memory not configured")
	ErrActivityLimit  = errors.New("macos client: activity report exceeds bounded range")
	ErrMemoryChanging = errors.New("macos client: memory is changing")
	errKnowledgePage  = errors.New("macos client: invalid knowledge page")
)

func appendKnowledgePage(current, next knowledge.Page, after string) (knowledge.Page, error) {
	if len(next.Entities) > knowledge.MaxPageSize || (next.HasMore != (next.Next != "")) {
		return knowledge.Page{}, errKnowledgePage
	}
	if next.HasMore && (len(next.Entities) == 0 || next.Entities[len(next.Entities)-1].ID != next.Next || next.Next == after) {
		return knowledge.Page{}, errKnowledgePage
	}
	seen := make(map[string]struct{}, len(current.Entities)+len(next.Entities))
	for _, entity := range current.Entities {
		if entity.ID == "" {
			return knowledge.Page{}, errKnowledgePage
		}
		seen[entity.ID] = struct{}{}
	}
	for _, entity := range next.Entities {
		if entity.ID == "" {
			return knowledge.Page{}, errKnowledgePage
		}
		if _, duplicate := seen[entity.ID]; duplicate {
			return knowledge.Page{}, errKnowledgePage
		}
		seen[entity.ID] = struct{}{}
	}
	if after == "" {
		return next, nil
	}
	if !current.HasMore || current.Next != after || len(current.Entities)+len(next.Entities) > knowledge.MaxEntities {
		return knowledge.Page{}, errKnowledgePage
	}
	merged := knowledge.Page{Entities: make([]knowledge.Entity, 0, len(current.Entities)+len(next.Entities)), Next: next.Next, HasMore: next.HasMore}
	merged.Entities = append(merged.Entities, current.Entities...)
	merged.Entities = append(merged.Entities, next.Entities...)
	return merged, nil
}

const (
	MaxActivityReportDays     = 31
	MaxActivityReportSegments = activity.MaxReportSegments
	maxActivityReportUTCdays  = 33
)

type ActivityData interface {
	ListActivitySegmentsForDates(context.Context, []string, int) ([]activity.Segment, error)
}

type TodaySnapshotWriter interface {
	PutTodaySnapshot(context.Context, today.Snapshot) error
}

type Data interface {
	GetTodaySnapshot(context.Context, string) (today.Snapshot, bool, error)
	SearchExact(context.Context, search.ExactQuery) (search.ExactPage, error)
	ListKnowledge(context.Context, knowledge.PageRequest) (knowledge.Page, error)
	ConfirmProposal(context.Context, string, string) error
	AnswerQuestion(context.Context, string, string, string, time.Time) error
	DismissQuestion(context.Context, string, string) error
	GetObservation(context.Context, string) (capture.SanitizedObservation, bool, error)
}

type NoteAdder interface {
	Add(context.Context, string) (notes.Result, error)
}

type SourceDeleter interface {
	DeleteSource(context.Context, string) (int, error)
}

type AllSourceDeleter interface {
	DeleteAll(context.Context) (int, error)
}

type Client struct {
	data                   Data
	chronicles             storage.ChronicleStore
	notes                  NoteAdder
	deletion               SourceDeleter
	localAI                *localAIController
	retention              *retention.Service
	power                  *power.Controller
	preferences            *preferences.Store
	mcpControl             MCPPreferenceControl
	replica                *replicaService
	retentionStartupFailed bool
	captureChanges         chan struct{}
	captureStateChanges    chan struct{}
	safariCaptureMu        sync.Mutex
	safariCaptureStatus    string
	safariCaptureChanges   chan struct{}
	memoryChangeMu         sync.Mutex
	memoryStartMu          sync.Mutex
	activeAsks             map[uint64]context.CancelFunc
	nextAskID              uint64
	memoryChanges          chan struct{}
	memoryMutationDepth    int
	memoryMutationEpoch    uint64
	memoryInvalidator      func()
	memoryHooks            []func()
	appsMu                 sync.Mutex
	apps                   appsCache
	captureState           func() (bool, time.Time)
	askNow                 func() time.Time
	lifeMu                 sync.Mutex
	lifeCtx                context.Context
	lifeCancel             context.CancelFunc
	lifeClosed             bool
	inflight               sync.WaitGroup
}

const (
	noteOperationTimeout  = 60 * time.Second
	shortOperationTimeout = 15 * time.Second
)

// ErrClientClosed is returned for operations started after, or interrupted by,
// Close.
var ErrClientClosed = errors.New("macos client: closed")

// beginOperation derives a bounded context that is also cancelled by Close and
// is tracked so Close can wait before the store is released.
func (c *Client) beginOperation(parent context.Context, timeout time.Duration) (context.Context, func(), error) {
	if parent == nil {
		return nil, nil, ErrNotConfigured
	}
	c.lifeMu.Lock()
	if c.lifeClosed {
		c.lifeMu.Unlock()
		return nil, nil, ErrClientClosed
	}
	c.inflight.Add(1)
	life := c.lifeCtx
	c.lifeMu.Unlock()
	ctx, cancel := context.WithTimeout(parent, timeout)
	stop := context.AfterFunc(life, cancel)
	return ctx, func() {
		stop()
		cancel()
		c.inflight.Done()
	}, nil
}

// Close cancels in-flight operations, waits for them and rejects new ones.
func (c *Client) Close() {
	if c == nil {
		return
	}
	c.lifeMu.Lock()
	c.lifeClosed = true
	cancel := c.lifeCancel
	c.lifeMu.Unlock()
	if cancel != nil {
		cancel()
	}
	c.inflight.Wait()
}

// addMemoryChangingHook registers a synchronous callback run at the start of
// every memory mutation window. It must not call back into Client.
func (c *Client) addMemoryChangingHook(hook func()) {
	if c == nil || hook == nil {
		return
	}
	c.memoryChangeMu.Lock()
	c.memoryHooks = append(c.memoryHooks, hook)
	c.memoryChangeMu.Unlock()
}

func NewClient(data Data) *Client {
	client := &Client{data: data, captureChanges: make(chan struct{}, 1), captureStateChanges: make(chan struct{}, 1), safariCaptureChanges: make(chan struct{}, 1), activeAsks: make(map[uint64]context.CancelFunc), memoryChanges: make(chan struct{}, 1)}
	client.lifeCtx, client.lifeCancel = context.WithCancel(context.Background())
	client.addMemoryChangingHook(client.invalidateKnownApps)
	if repository, ok := data.(storage.ChronicleStore); ok {
		client.chronicles = repository
	}
	return client
}

func (c *Client) ChroniclesForDate(ctx context.Context, date string) ([]chronicle.Summary, error) {
	if c == nil || c.chronicles == nil || ctx == nil || !validDate(date) {
		return nil, ErrNotConfigured
	}
	parsed, _ := time.Parse("2006-01-02", date)
	zone, err := loadZone(localZoneName())
	if err != nil {
		return nil, err
	}
	start := time.Date(parsed.Year(), parsed.Month(), parsed.Day(), 0, 0, 0, 0, zone)
	end := time.Date(parsed.Year(), parsed.Month(), parsed.Day()+1, 0, 0, 0, 0, zone)
	rows, _, err := c.chronicles.ListChronicles(ctx, start.UTC(), end.UTC(), "", 26)
	return rows, err
}

func (c *Client) ChronicleSources(ctx context.Context, hourID string) ([]SearchHit, error) {
	rows, _, err := c.ChronicleSourcesPage(ctx, hourID, "", 8)
	return rows, err
}

func (c *Client) ChronicleSourcesPage(ctx context.Context, hourID, after string, limit int) ([]SearchHit, string, error) {
	if c == nil || c.chronicles == nil || ctx == nil || limit < 1 || limit > 8 || after != "" && !validOpaqueID(after) {
		return nil, "", ErrInvalidRequest
	}
	summary, found, err := c.chronicles.GetChronicle(ctx, hourID)
	if err != nil {
		return nil, "", err
	}
	if !found || len(summary.SourceIDs) > chronicle.MaxEvidence {
		return nil, "", ErrInvalidRequest
	}
	start := 0
	if after != "" {
		index := sort.SearchStrings(summary.SourceIDs, after)
		if index >= len(summary.SourceIDs) || summary.SourceIDs[index] != after {
			return nil, "", ErrInvalidRequest
		}
		start = index + 1
	}
	end := min(start+limit, len(summary.SourceIDs))
	result := make([]SearchHit, 0, end-start)
	for _, id := range summary.SourceIDs[start:end] {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		observation, found, err := c.data.GetObservation(ctx, id)
		if err != nil {
			return nil, "", err
		}
		if !found {
			return nil, "", ErrInvalidRequest
		}
		text := observation.Text()
		truncated := len(text) > 4<<10
		if truncated {
			text = text[:4<<10]
			for !utf8.ValidString(text) {
				text = text[:len(text)-1]
			}
		}
		result = append(result, SearchHit{SourceID: id, AppID: observation.AppID(), Domain: observation.Domain(), Text: text, Truncated: truncated})
	}
	next := ""
	if end < len(summary.SourceIDs) && end > 0 {
		next = summary.SourceIDs[end-1]
	}
	return result, next, nil
}

// CanSuggestKnowledge reports whether an explicitly invoked, loopback-only
// knowledge derivation path is configured.
func (c *Client) CanSuggestKnowledge() bool {
	if c == nil || c.data == nil || c.chronicles == nil || c.localAI == nil {
		return false
	}
	return c.localAI.canSuggestKnowledge()
}

// SuggestKnowledgeFromChronicle asks the configured local Ollama model for a
// few transient, cited Library proposals from only one explicitly selected
// recap. It never persists model output.
func (c *Client) SuggestKnowledgeFromChronicle(ctx context.Context, hourID string) ([]derive.Proposal, error) {
	if ctx == nil || ctx.Err() != nil || c == nil || c.data == nil || c.chronicles == nil || c.localAI == nil || !validChronicleID(hourID) {
		return nil, ErrNotConfigured
	}
	generator, ok := c.localAI.knowledgeGenerator()
	if !ok {
		return nil, ErrNotConfigured
	}
	operationCtx, cancel := context.WithCancel(ctx)
	requestID, registered := c.registerAsk(cancel)
	if !registered {
		cancel()
		return nil, ErrMemoryChanging
	}
	defer func() {
		cancel()
		c.unregisterAsk(requestID)
	}()
	defer func() {
		if closer, ok := generator.(interface{ CloseIdleConnections() }); ok {
			closer.CloseIdleConnections()
		}
	}()
	rows, _, err := c.chronicles.GetChronicle(operationCtx, hourID)
	if err != nil {
		return nil, err
	}
	if rows.HourID != hourID || len(rows.SourceIDs) == 0 || len(rows.SourceIDs) > chronicle.MaxEvidence {
		return nil, ErrInvalidRequest
	}
	hits, _, err := c.ChronicleSourcesPage(operationCtx, hourID, "", derive.MaxEvidenceSources)
	if err != nil {
		return nil, err
	}
	evidence := boundedKnowledgeEvidence(hits)
	if err := derive.ValidateInput(evidence); err != nil {
		return nil, ErrInvalidRequest
	}
	evidence, err = fitKnowledgeEvidence(operationCtx, generator, evidence)
	if err != nil {
		return nil, err
	}
	proposals, err := generator.Generate(operationCtx, evidence)
	if err != nil {
		return nil, err
	}
	if operationCtx.Err() != nil || derive.ValidateSuggestions(evidence, proposals) != nil {
		return nil, ErrInvalidRequest
	}
	return proposals, nil
}

func boundedKnowledgeEvidence(hits []SearchHit) []derive.Evidence {
	result := make([]derive.Evidence, 0, min(len(hits), derive.MaxEvidenceSources))
	bytesUsed := 0
	for _, hit := range hits {
		if len(result) >= derive.MaxEvidenceSources {
			break
		}
		itemBytes := len(hit.SourceID) + len(hit.Text) + 16
		if bytesUsed+itemBytes > derive.MaxEvidenceBytes {
			break
		}
		result = append(result, derive.Evidence{SourceID: hit.SourceID, Text: hit.Text})
		bytesUsed += itemBytes
	}
	return result
}

func fitKnowledgeEvidence(ctx context.Context, generator derive.Generator, evidence []derive.Evidence) ([]derive.Evidence, error) {
	for len(evidence) > 0 {
		count, err := generator.CountInputTokens(ctx, evidence)
		if err != nil {
			return nil, err
		}
		if count <= derive.MaxInputTokens && count+derive.MaxOutputTokens+model.PromptTemplateReserve <= model.ContextTokenLimit {
			return evidence, nil
		}
		evidence = evidence[:len(evidence)-1]
	}
	return nil, ErrInvalidRequest
}

func validChronicleID(value string) bool {
	if len(value) != len("hour:")+len("20060102T15Z") || !strings.HasPrefix(value, "hour:") {
		return false
	}
	_, err := time.Parse("20060102T15Z", value[len("hour:"):])
	return err == nil
}

func (c *Client) CanViewChronicles() bool { return c != nil && c.chronicles != nil }

func (c *Client) ChronicleSource(ctx context.Context, sourceID string) (SearchHit, error) {
	if c == nil || c.data == nil || ctx == nil || !validKnowledgeID(sourceID) {
		return SearchHit{}, ErrInvalidRequest
	}
	observation, found, err := c.data.GetObservation(ctx, sourceID)
	if err != nil {
		return SearchHit{}, err
	}
	if !found {
		return SearchHit{}, ErrInvalidRequest
	}
	return SearchHit{SourceID: sourceID, AppID: observation.AppID(), Domain: observation.Domain(), Text: observation.Text()}, nil
}

// CaptureChanges returns a coalesced, payload-free signal for newly persisted
// capture data. It contains no observation text or identifiers.
func (c *Client) CaptureChanges() <-chan struct{} {
	if c == nil {
		return nil
	}
	return c.captureChanges
}

// NotifyCaptureChanged is safe to call from the capture worker and never blocks.
func (c *Client) NotifyCaptureChanged() {
	if c == nil || c.captureChanges == nil {
		return
	}
	select {
	case c.captureChanges <- struct{}{}:
	default:
	}
}

// CaptureStateChanges reports payload-free, coalesced foreground capture
// policy changes. Unlike CaptureChanges, it does not imply new stored content.
func (c *Client) CaptureStateChanges() <-chan struct{} {
	if c == nil {
		return nil
	}
	return c.captureStateChanges
}

func (c *Client) NotifyCaptureStateChanged() {
	if c == nil || c.captureStateChanges == nil {
		return
	}
	select {
	case c.captureStateChanges <- struct{}{}:
	default:
	}
}

const (
	safariCaptureOffStatus         = "Safari page capture is off."
	safariCaptureStartingStatus    = "Safari page capture is starting. Safari may ask for permission for individual websites."
	safariCaptureReadyStatus       = "Safari page capture is ready for permitted active tabs. Private windows are excluded."
	safariCaptureUnavailableStatus = "Safari page capture could not start. Check the app's Safari extension and website permissions; native capture remains active."
)

func (c *Client) setSafariCaptureStatus(status string) {
	if c == nil {
		return
	}
	c.safariCaptureMu.Lock()
	c.safariCaptureStatus = status
	c.safariCaptureMu.Unlock()
	select {
	case c.safariCaptureChanges <- struct{}{}:
	default:
	}
}

func (c *Client) SafariCaptureStatus() string {
	if c == nil {
		return safariCaptureOffStatus
	}
	c.safariCaptureMu.Lock()
	defer c.safariCaptureMu.Unlock()
	return c.safariCaptureStatus
}

func (c *Client) SafariCaptureChanges() <-chan struct{} {
	if c == nil {
		return nil
	}
	return c.safariCaptureChanges
}

// MemoryChanges returns a coalesced, payload-free signal emitted immediately
// before stored memory is removed. It contains no observation text or IDs.
func (c *Client) MemoryChanges() <-chan struct{} {
	if c == nil {
		return nil
	}
	return c.memoryChanges
}

// SetMemoryInvalidator installs a synchronous UI-state invalidation callback.
// It must not call back into Client. The desktop uses this to remove visible
// cached evidence before a deletion is allowed to begin.
func (c *Client) SetMemoryInvalidator(invalidate func()) {
	if c == nil {
		return
	}
	c.memoryChangeMu.Lock()
	c.memoryInvalidator = invalidate
	c.memoryChangeMu.Unlock()
}

// MemoryIsChanging reports whether a deletion window is open.
func (c *Client) MemoryIsChanging() bool {
	if c == nil {
		return false
	}
	c.memoryChangeMu.Lock()
	defer c.memoryChangeMu.Unlock()
	return c.memoryMutationDepth > 0
}

// BeginMemoryRead captures the current mutation epoch only when memory is
// stable. UI reads verify it again when installing results, so queued callbacks
// cannot reintroduce evidence after a deletion has started or completed.
func (c *Client) BeginMemoryRead() (uint64, bool) {
	if c == nil {
		return 0, false
	}
	c.memoryChangeMu.Lock()
	defer c.memoryChangeMu.Unlock()
	return c.memoryMutationEpoch, c.memoryMutationDepth == 0
}

// MemoryReadCurrent reports whether a read started in the supplied epoch may
// publish. The check also rejects callbacks while any mutation remains active.
func (c *Client) MemoryReadCurrent(epoch uint64) bool {
	if c == nil {
		return false
	}
	c.memoryChangeMu.Lock()
	defer c.memoryChangeMu.Unlock()
	return c.memoryMutationDepth == 0 && c.memoryMutationEpoch == epoch
}

// PublishMemoryRead holds the mutation fence through a small result-install
// callback. The callback must only publish already-loaded view state and must
// not call back into Client.
func (c *Client) PublishMemoryRead(epoch uint64, publish func()) bool {
	if c == nil || publish == nil {
		return false
	}
	c.memoryChangeMu.Lock()
	defer c.memoryChangeMu.Unlock()
	if c.memoryMutationDepth != 0 || c.memoryMutationEpoch != epoch {
		return false
	}
	publish()
	return true
}

// NotifyMemoryChanging cancels active Ask operations and non-blockingly signals
// the UI before a deletion or retention sweep can mutate stored evidence.
func (c *Client) NotifyMemoryChanging() {
	if c == nil {
		return
	}
	c.memoryStartMu.Lock()
	defer c.memoryStartMu.Unlock()
	c.memoryChangeMu.Lock()
	first := c.memoryMutationDepth == 0
	c.memoryMutationDepth++
	c.memoryMutationEpoch++
	if c.memoryMutationEpoch == 0 {
		c.memoryMutationEpoch++
	}
	cancels := make([]context.CancelFunc, 0, len(c.activeAsks))
	for _, cancel := range c.activeAsks {
		cancels = append(cancels, cancel)
	}
	invalidate := c.memoryInvalidator
	hooks := append([]func(){}, c.memoryHooks...)
	select {
	case c.memoryChanges <- struct{}{}:
	default:
	}
	c.memoryChangeMu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	for _, hook := range hooks {
		hook()
	}
	if first && invalidate != nil {
		invalidate()
	}
}

// NotifyMemoryStable ends a deletion window and wakes the UI to refresh its
// bounded views. It is safe to call even after a partial deletion failure.
func (c *Client) NotifyMemoryStable() {
	if c == nil {
		return
	}
	c.memoryChangeMu.Lock()
	if c.memoryMutationDepth > 0 {
		c.memoryMutationDepth--
	}
	select {
	case c.memoryChanges <- struct{}{}:
	default:
	}
	c.memoryChangeMu.Unlock()
}

func (c *Client) registerAsk(cancel context.CancelFunc) (uint64, bool) {
	c.memoryChangeMu.Lock()
	defer c.memoryChangeMu.Unlock()
	if c.memoryMutationDepth > 0 {
		return 0, false
	}
	c.nextAskID++
	if c.nextAskID == 0 {
		c.nextAskID++
	}
	c.activeAsks[c.nextAskID] = cancel
	return c.nextAskID, true
}

func (c *Client) unregisterAsk(id uint64) {
	c.memoryChangeMu.Lock()
	delete(c.activeAsks, id)
	c.memoryChangeMu.Unlock()
}

func (c *Client) CanAddNote() bool { return c != nil && c.data != nil && c.notes != nil }

func (c *Client) WithNotes(adder NoteAdder) *Client {
	if c != nil {
		c.notes = adder
	}
	return c
}

func (c *Client) WithDeletion(deleter SourceDeleter) *Client {
	if c != nil {
		c.deletion = deleter
	}
	return c
}

func (c *Client) WithRetention(service *retention.Service) *Client {
	if c != nil {
		c.retention = service
	}
	return c
}

func (c *Client) WithPower(controller *power.Controller) *Client {
	if c != nil {
		c.power = controller
	}
	return c
}

func (c *Client) WithPreferences(store *preferences.Store) *Client {
	if c != nil {
		c.preferences = store
	}
	return c
}

func (c *Client) ApplicationPreferences(ctx context.Context) (preferences.Config, error) {
	if c == nil || c.preferences == nil {
		return preferences.Config{}, ErrNotConfigured
	}
	return c.preferences.Load(ctx)
}

func (c *Client) ConfigureApplicationPreferences(ctx context.Context, config preferences.Config) error {
	if c == nil || c.preferences == nil {
		return ErrNotConfigured
	}
	previous, err := c.preferences.Load(ctx)
	if err != nil {
		return err
	}
	changed := previous.ClaudeMCPEnabled != config.ClaudeMCPEnabled
	if changed && c.mcpControl == nil && config.ClaudeMCPEnabled {
		return ErrNotConfigured
	}
	if changed && c.mcpControl != nil {
		if err := c.mcpControl.SetEnabled(ctx, config.ClaudeMCPEnabled); err != nil {
			return err
		}
	}
	if err := c.preferences.Save(ctx, config); err != nil {
		if changed && c.mcpControl != nil {
			rollbackCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = c.mcpControl.SetEnabled(rollbackCtx, previous.ClaudeMCPEnabled)
			cancel()
		}
		return err
	}
	if previous.CaptureEnabled && !config.CaptureEnabled {
		c.flushReplica() // pause: push pending changes to the sync folder
	}
	return nil
}

func (c *Client) CanConfigurePower() bool { return c != nil && c.power != nil }

func (c *Client) PowerConfig(ctx context.Context) (power.Config, error) {
	if c == nil || c.power == nil {
		return power.Config{}, ErrNotConfigured
	}
	return c.power.Config(ctx)
}

func (c *Client) ConfigurePower(ctx context.Context, config power.Config) error {
	if c == nil || c.power == nil {
		return ErrNotConfigured
	}
	return c.power.Configure(ctx, config)
}

func (c *Client) EffectivePower(ctx context.Context) (power.Effective, error) {
	if c == nil || c.power == nil {
		return power.Effective{}, ErrNotConfigured
	}
	return c.power.Effective(ctx)
}

func (c *Client) CanConfigureRetention() bool { return c != nil && c.retention != nil }

func (c *Client) RetentionPolicy(ctx context.Context) (durable.RetentionPolicy, error) {
	if c == nil || c.retention == nil {
		return durable.RetentionPolicy{}, ErrNotConfigured
	}
	return c.retention.Policy(ctx)
}

func (c *Client) ConfigureRetentionDays(ctx context.Context, days int) error {
	if c == nil || c.retention == nil {
		return ErrNotConfigured
	}
	return c.retention.ConfigureDays(ctx, days)
}

func (c *Client) RunRetentionNow(ctx context.Context, now time.Time) (int, error) {
	if c == nil || c.retention == nil {
		return 0, ErrNotConfigured
	}
	return c.retention.RunNow(ctx, now)
}

func (c *Client) WithLocalAI(controller *localAIController) *Client {
	if c != nil {
		c.localAI = controller
	}
	return c
}

func (c *Client) LocalAISettings() LocalAISettings {
	if c == nil || c.localAI == nil {
		return LocalAISettings{}
	}
	return c.localAI.Settings()
}

func (c *Client) LocalAIEnabled() bool { return c != nil && c.localAI != nil && c.localAI.Enabled() }

func (c *Client) LocalAISettingsNeedRepair() bool {
	return c != nil && c.localAI != nil && c.localAI.NeedsRepair()
}

// CanAskMemory reports whether a configured model is enabled for cited
// questions over locally retrieved memory. Remote providers require separate
// persisted consent before captured evidence can be sent.
func (c *Client) CanAskMemory() bool {
	if c == nil || c.data == nil || c.localAI == nil {
		return false
	}
	_, ok := c.localAI.askService(c)
	return ok
}

// AskMemory retrieves a small exact-match evidence set and asks the configured
// provider. Remote providers are used only after separate explicit consent.
func (c *Client) AskMemory(ctx context.Context, question string) (assistant.Result, error) {
	return c.AskMemoryWithHistory(ctx, question, nil)
}

// AskMemoryWithHistory is AskMemory with earlier turns, so a follow-up can
// inherit the previous scope and app.
func (c *Client) AskMemoryWithHistory(ctx context.Context, question string, history []assistant.Turn) (assistant.Result, error) {
	if c == nil || c.data == nil || c.localAI == nil || ctx == nil {
		return assistant.Result{}, ErrNotConfigured
	}
	opCtx, done, err := c.beginOperation(ctx, noteOperationTimeout)
	if err != nil {
		return assistant.Result{}, err
	}
	defer done()
	service, ok := c.localAI.askService(c)
	if !ok {
		return assistant.Result{}, assistant.ErrAskUnavailable
	}
	askCtx, cancel := context.WithCancel(opCtx)
	requestID, ok := c.registerAsk(cancel)
	if !ok {
		cancel()
		return assistant.Result{}, ErrMemoryChanging
	}
	defer func() {
		cancel()
		c.unregisterAsk(requestID)
	}()
	return service.AskWithHistory(askCtx, question, history)
}

func (c *Client) ConfigureLocalAI(ctx context.Context, settings LocalAISettings) error {
	if c == nil || c.localAI == nil {
		return ErrNotConfigured
	}
	return c.localAI.Configure(ctx, settings)
}

// ConfigureAI configures an explicitly selected note-processing provider. Any
// credential is transient input and is persisted only through the OS secret store.
func (c *Client) ConfigureAI(ctx context.Context, settings LocalAISettings, credential string) error {
	if c == nil || c.localAI == nil {
		return ErrNotConfigured
	}
	return c.localAI.Configure(ctx, settings, credential)
}

func (c *Client) SetBackgroundSummaries(ctx context.Context, enabled bool) error {
	if c == nil || c.localAI == nil {
		return ErrNotConfigured
	}
	return c.localAI.SetBackgroundSummaries(ctx, enabled)
}

func (c *Client) CanDeleteSource() bool { return c != nil && c.deletion != nil }

func (c *Client) CanDeleteAll() bool {
	if c == nil || c.deletion == nil {
		return false
	}
	_, ok := c.deletion.(AllSourceDeleter)
	return ok
}

// DeleteSource removes one exact sanitized source and its derived records.
// The UI must require an explicit confirmation before calling it.
func (c *Client) DeleteSource(sourceID string) (int, error) {
	if c == nil || c.deletion == nil {
		return 0, ErrNotConfigured
	}
	decoded, err := hex.DecodeString(sourceID)
	if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != sourceID {
		clear(decoded)
		return 0, ErrInvalidRequest
	}
	clear(decoded)
	c.NotifyMemoryChanging()
	defer c.NotifyMemoryStable()
	ctx, done, err := c.beginOperation(context.Background(), shortOperationTimeout)
	if err != nil {
		return 0, err
	}
	defer done()
	return c.deletion.DeleteSource(ctx, sourceID)
}

// DeleteAll removes every stored source and its source-linked derivatives.
// Callers must pause capture and require explicit confirmation first.
func (c *Client) DeleteAll(ctx context.Context) (int, error) {
	if c == nil || c.deletion == nil || ctx == nil {
		return 0, ErrNotConfigured
	}
	deleter, ok := c.deletion.(AllSourceDeleter)
	if !ok {
		return 0, ErrNotConfigured
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	c.NotifyMemoryChanging()
	defer c.NotifyMemoryStable()
	return deleter.DeleteAll(ctx)
}

func (c *Client) AddNote(text string) (notes.Result, error) {
	if c == nil || c.notes == nil {
		return notes.Result{}, ErrNotConfigured
	}
	if len(text) == 0 || len(text) > notes.MaxNoteBytes || !utf8.ValidString(text) || strings.TrimSpace(text) == "" || strings.IndexByte(text, 0) >= 0 {
		return notes.Result{}, ErrInvalidRequest
	}
	ctx, done, err := c.beginOperation(context.Background(), noteOperationTimeout)
	if err != nil {
		return notes.Result{}, err
	}
	defer done()
	return c.notes.Add(ctx, text)
}

// ConfirmProposal activates one stored suggestion only after an explicit user
// action. It never creates a system notification or touches another app.
func (c *Client) ConfirmProposal(date, proposalID string) error {
	if c == nil || c.data == nil {
		return ErrNotConfigured
	}
	if !validDate(date) || len(proposalID) == 0 || len(proposalID) > 160 || !utf8.ValidString(proposalID) || strings.IndexByte(proposalID, 0) >= 0 {
		return ErrInvalidRequest
	}
	ctx, done, err := c.beginOperation(context.Background(), shortOperationTimeout)
	if err != nil {
		return err
	}
	defer done()
	return c.data.ConfirmProposal(ctx, date, proposalID)
}

func (c *Client) AnswerQuestion(date, questionID, answer string) error {
	if c == nil || c.data == nil {
		return ErrNotConfigured
	}
	if !validDate(date) || !validOpaqueID(questionID) || len(answer) == 0 || len(answer) > notes.MaxNoteBytes || !utf8.ValidString(answer) || strings.TrimSpace(answer) == "" || strings.IndexByte(answer, 0) >= 0 {
		return ErrInvalidRequest
	}
	ctx, done, err := c.beginOperation(context.Background(), shortOperationTimeout)
	if err != nil {
		return err
	}
	defer done()
	return c.data.AnswerQuestion(ctx, date, questionID, answer, time.Now())
}

func (c *Client) DismissQuestion(date, questionID string) error {
	if c == nil || c.data == nil {
		return ErrNotConfigured
	}
	if !validDate(date) || !validOpaqueID(questionID) {
		return ErrInvalidRequest
	}
	ctx, done, err := c.beginOperation(context.Background(), shortOperationTimeout)
	if err != nil {
		return err
	}
	defer done()
	return c.data.DismissQuestion(ctx, date, questionID)
}

type TodayView struct {
	State        string         `json:"state"`
	Date         string         `json:"date"`
	CaptureState string         `json:"capture_state"`
	Snapshot     today.Snapshot `json:"snapshot"`
}

type SearchHit struct {
	SourceID  string `json:"source_id"`
	AppID     string `json:"app_id"`
	Domain    string `json:"domain"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated,omitempty"`
}

func (c *Client) Today(date string) (TodayView, error) {
	if !validDate(date) {
		return TodayView{}, ErrInvalidRequest
	}
	out := TodayView{State: "empty", Date: date, CaptureState: "not_configured"}
	if c == nil || c.data == nil {
		return out, nil
	}
	ctx, done, err := c.beginOperation(context.Background(), shortOperationTimeout)
	if err != nil {
		return TodayView{}, err
	}
	defer done()
	epoch, _ := c.BeginMemoryRead()
	snapshot, found, err := c.data.GetTodaySnapshot(ctx, date)
	if err != nil {
		return TodayView{}, err
	}
	if reader, ok := c.data.(ActivityData); ok {
		if writer, ok := c.data.(TodaySnapshotWriter); ok {
			zone := localZoneName()
			if _, iana := time.LoadLocation(zone); iana == nil && found && snapshot.Zone != "" {
				zone = snapshot.Zone
			}
			snapshot, found, err = materializeTodayActivity(ctx, date, zone, snapshot, found, reader, fencedTodayWriter{client: c, epoch: epoch, writer: writer})
			if err != nil {
				return TodayView{}, err
			}
		}
	}
	if found {
		out.State, out.CaptureState, out.Snapshot = "ready", "paused", snapshot.Clone()
	}
	return out, nil
}

func (c *Client) Search(query string) (search.ExactPage, error) {
	return c.SearchContext(context.Background(), query)
}

func (c *Client) SearchContext(ctx context.Context, query string) (search.ExactPage, error) {
	return c.searchContextLimit(ctx, query, MaxSearchResults)
}

func (c *Client) searchContextLimit(ctx context.Context, query string, limit int) (search.ExactPage, error) {
	if c == nil || c.data == nil || ctx == nil || limit < 1 || limit > MaxSearchResults || len(query) > MaxQueryBytes || !utf8.ValidString(query) || strings.IndexByte(query, 0) >= 0 {
		return search.ExactPage{}, ErrInvalidRequest
	}
	if err := ctx.Err(); err != nil {
		return search.ExactPage{}, err
	}
	query = strings.TrimSpace(query)
	terms, err := search.NormalizeTokens(query, 32)
	if err != nil || len(terms) == 0 {
		return search.ExactPage{}, ErrInvalidRequest
	}
	return c.searchExactTerms(ctx, terms, limit, 0, limit)
}

func (c *Client) searchExactTerms(ctx context.Context, terms []string, limit, minTermMatches, maxRows int) (search.ExactPage, error) {
	if c == nil || c.data == nil || ctx == nil || len(terms) == 0 || limit < 1 || limit > MaxSearchResults || maxRows < limit || maxRows > search.MaxIndexRows {
		return search.ExactPage{}, ErrInvalidRequest
	}
	if err := ctx.Err(); err != nil {
		return search.ExactPage{}, err
	}
	page, err := c.data.SearchExact(ctx, search.ExactQuery{Terms: terms, MinTermMatches: minTermMatches, TopK: limit, MaxRows: maxRows, MaxBytes: search.MaxIndexBytes})
	if err != nil {
		return search.ExactPage{}, err
	}
	if err := ctx.Err(); err != nil {
		return search.ExactPage{}, err
	}
	if len(page.Hits) > MaxSearchResults {
		return search.ExactPage{}, ErrInvalidRequest
	}
	return page, nil
}

// SearchMemory hydrates exact top-k index hits under one aggregate byte
// budget. Results contain only sanitized evidence text.
func (c *Client) SearchMemory(query string) ([]SearchHit, error) {
	return c.SearchMemoryContext(context.Background(), query)
}

func (c *Client) SearchMemoryContext(ctx context.Context, query string) ([]SearchHit, error) {
	return c.searchMemoryContext(ctx, query, MaxSearchResults)
}

func (c *Client) searchMemoryContext(ctx context.Context, query string, limit int) ([]SearchHit, error) {
	page, err := c.searchContextLimit(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	return c.hydrateSearchPage(ctx, page)
}

func (c *Client) hydrateSearchPage(ctx context.Context, page search.ExactPage) ([]SearchHit, error) {
	results := make([]SearchHit, 0, len(page.Hits))
	used := 0
	for _, hit := range page.Hits {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if hit.Bytes < 1 || hit.Bytes > MaxSearchBytes-used {
			return nil, ErrInvalidRequest
		}
		observation, found, err := c.data.GetObservation(ctx, hit.SourceID)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		bytes := len(observation.Text()) + len(observation.AppID()) + len(observation.Domain())
		if observation.ContentID() != hit.SourceID || bytes != hit.Bytes || bytes > MaxSearchBytes-used {
			return nil, ErrInvalidRequest
		}
		used += bytes
		results = append(results, SearchHit{SourceID: observation.ContentID(), AppID: observation.AppID(), Domain: observation.Domain(), Text: observation.Text()})
	}
	return results, nil
}

func (c *Client) retrieveMemoryContext(ctx context.Context, query string, limit int) ([]SearchHit, error) {
	if c == nil || c.data == nil || ctx == nil || limit < 1 || limit > assistant.MaxEvidenceSources || len(query) > MaxQueryBytes || !utf8.ValidString(query) || strings.IndexByte(query, 0) >= 0 {
		return nil, ErrInvalidRequest
	}
	terms, err := search.NormalizeTokens(query, 32)
	if err != nil || len(terms) == 0 {
		return nil, ErrInvalidRequest
	}
	// Both passes share a small row budget, which is large enough to establish
	// ranking for ordinary questions but never becomes a collection scan.
	maxRows := min(search.MaxIndexRows, limit*16)
	page, err := c.searchExactTerms(ctx, terms, limit, 0, maxRows)
	if err != nil {
		return nil, err
	}
	if len(page.Hits) == 0 && len(terms) >= 3 {
		minMatches := max(2, (len(terms)+1)/2)
		page, err = c.searchExactTerms(ctx, terms, limit, minMatches, maxRows)
		if err != nil {
			return nil, err
		}
	}
	return c.hydrateSearchPage(ctx, page)
}

// Retrieve adapts exact local search to the assistant's minimal evidence port.
func (c *Client) Retrieve(ctx context.Context, question string) ([]assistant.Evidence, error) {
	query, err := assistant.QuestionSearchQuery(question)
	if err != nil {
		return nil, err
	}
	hits, err := c.retrieveMemoryContext(ctx, query, assistant.MaxEvidenceSources)
	if err != nil {
		return nil, err
	}
	evidence := make([]assistant.Evidence, 0, len(hits))
	for _, hit := range hits {
		evidence = append(evidence, assistant.Evidence{SourceID: hit.SourceID, AppID: hit.AppID, Domain: hit.Domain, Text: hit.Text})
	}
	return evidence, nil
}

func (c *Client) Knowledge(after string) (knowledge.Page, error) {
	if c == nil || c.data == nil || len(after) > 128 {
		return knowledge.Page{}, ErrInvalidRequest
	}
	return c.data.ListKnowledge(context.Background(), knowledge.PageRequest{Limit: 50, After: after})
}

type knowledgeEntityWriter interface {
	PutKnowledgeEntity(context.Context, knowledge.Entity) error
}

type knowledgeEvidenceWriter interface {
	PutKnowledgeEntityFromSource(context.Context, knowledge.Entity, string) error
}

type knowledgeEntityDeleter interface {
	DeleteKnowledgeEntity(context.Context, string) error
}

type knowledgeUnmerger interface {
	UnmergeKnowledge(context.Context, string, string) error
}

func (c *Client) CanAddKnowledge() bool {
	if c == nil || c.data == nil {
		return false
	}
	_, ok := c.data.(knowledgeEntityWriter)
	return ok
}

func (c *Client) CanDeleteKnowledge() bool {
	if c == nil || c.data == nil {
		return false
	}
	_, ok := c.data.(knowledgeEntityDeleter)
	return ok
}

// DeleteKnowledge removes one explicitly selected library entity. It does not
// delete the captured sources that may have contributed to that entity.
func (c *Client) DeleteKnowledge(ctx context.Context, id string) error {
	if ctx == nil || ctx.Err() != nil || !validKnowledgeID(id) || c == nil || c.data == nil {
		return ErrInvalidRequest
	}
	deleter, ok := c.data.(knowledgeEntityDeleter)
	if !ok {
		return ErrNotConfigured
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return deleter.DeleteKnowledgeEntity(ctx, id)
}

func validKnowledgeID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for i := range id {
		c := id[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func (c *Client) CanUnmergeKnowledge() bool {
	if c == nil || c.data == nil {
		return false
	}
	_, ok := c.data.(knowledgeUnmerger)
	return ok
}

func (c *Client) UnmergeKnowledge(ctx context.Context, sourceID, targetID string) error {
	if ctx == nil || ctx.Err() != nil || !validKnowledgeID(sourceID) || !validKnowledgeID(targetID) || sourceID == targetID || c == nil || c.data == nil {
		return ErrInvalidRequest
	}
	unmerger, ok := c.data.(knowledgeUnmerger)
	if !ok {
		return ErrNotConfigured
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return unmerger.UnmergeKnowledge(ctx, sourceID, targetID)
}

// AddKnowledge creates an explicitly user-authored, confirmed library entity.
// Its optional note is stored only as a confirmed fact with user-authored
// provenance; captured observations and AI are not consulted.
func (c *Client) AddKnowledge(ctx context.Context, kind knowledge.Kind, name, note string) (knowledge.Entity, error) {
	if ctx == nil || ctx.Err() != nil || c == nil || c.data == nil {
		return knowledge.Entity{}, ErrInvalidRequest
	}
	writer, ok := c.data.(knowledgeEntityWriter)
	if !ok {
		return knowledge.Entity{}, ErrNotConfigured
	}
	if kind != knowledge.Person && kind != knowledge.Project && kind != knowledge.Topic && kind != knowledge.Place && kind != knowledge.Workflow {
		return knowledge.Entity{}, ErrInvalidRequest
	}
	name = strings.TrimSpace(name)
	note = strings.TrimSpace(note)
	if len(name) == 0 || len(name) > knowledge.MaxTextBytes || !utf8.ValidString(name) || strings.IndexByte(name, 0) >= 0 || len(note) > knowledge.MaxTextBytes || !utf8.ValidString(note) || strings.IndexByte(note, 0) >= 0 {
		return knowledge.Entity{}, ErrInvalidRequest
	}
	entityID, err := newKnowledgeID("entity-")
	if err != nil {
		return knowledge.Entity{}, err
	}
	entity := knowledge.Entity{ID: entityID, Kind: kind, Name: name, State: knowledge.Confirmed}
	if note != "" {
		factID, err := newKnowledgeID("fact-")
		if err != nil {
			return knowledge.Entity{}, err
		}
		entity.Facts = []knowledge.Fact{{ID: factID, Text: note, State: knowledge.Confirmed, Sources: []knowledge.Source{{UserAuthored: true}}}}
	}
	if entity.Validate() != nil {
		return knowledge.Entity{}, ErrInvalidRequest
	}
	if err := ctx.Err(); err != nil {
		return knowledge.Entity{}, err
	}
	if err := writer.PutKnowledgeEntity(ctx, entity); err != nil {
		return knowledge.Entity{}, err
	}
	return knowledge.Clone(entity), nil
}

// AcceptKnowledgeSuggestion persists one user-reviewed model proposal as a
// confirmed Library fact. The source is rechecked against this recap and the
// durable writer rechecks it atomically with the entity write.
func (c *Client) AcceptKnowledgeSuggestion(ctx context.Context, hourID string, proposal derive.Proposal) (knowledge.Entity, error) {
	if ctx == nil || ctx.Err() != nil || c == nil || c.data == nil || c.chronicles == nil || !validChronicleID(hourID) {
		return knowledge.Entity{}, ErrInvalidRequest
	}
	writer, ok := c.data.(knowledgeEvidenceWriter)
	if !ok {
		return knowledge.Entity{}, ErrNotConfigured
	}
	summary, found, err := c.chronicles.GetChronicle(ctx, hourID)
	if err != nil {
		return knowledge.Entity{}, err
	}
	if !found || summary.HourID != hourID || len(summary.SourceIDs) == 0 || len(summary.SourceIDs) > chronicle.MaxEvidence {
		return knowledge.Entity{}, ErrInvalidRequest
	}
	hits, _, err := c.ChronicleSourcesPage(ctx, hourID, "", derive.MaxEvidenceSources)
	if err != nil {
		return knowledge.Entity{}, err
	}
	var selected *SearchHit
	for i := range hits {
		if hits[i].SourceID == proposal.SourceID {
			selected = &hits[i]
			break
		}
	}
	if selected == nil {
		return knowledge.Entity{}, ErrInvalidRequest
	}
	evidence := []derive.Evidence{{SourceID: selected.SourceID, Text: selected.Text}}
	if derive.ValidateSuggestions(evidence, []derive.Proposal{proposal}) != nil {
		return knowledge.Entity{}, ErrInvalidRequest
	}
	entityID, err := newKnowledgeID("entity-")
	if err != nil {
		return knowledge.Entity{}, err
	}
	factID, err := newKnowledgeID("fact-")
	if err != nil {
		return knowledge.Entity{}, err
	}
	entity := knowledge.Entity{ID: entityID, Kind: proposal.Kind, Name: proposal.Name, State: knowledge.Confirmed, Facts: []knowledge.Fact{{ID: factID, Text: proposal.Fact, State: knowledge.Confirmed, Sources: []knowledge.Source{{ID: proposal.SourceID}}}}}
	if entity.Validate() != nil || ctx.Err() != nil {
		return knowledge.Entity{}, ErrInvalidRequest
	}
	if err := writer.PutKnowledgeEntityFromSource(ctx, entity, proposal.SourceID); err != nil {
		return knowledge.Entity{}, err
	}
	return knowledge.Clone(entity), nil
}

func newKnowledgeID(prefix string) (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		clear(value[:])
		return "", err
	}
	id := prefix + hex.EncodeToString(value[:])
	clear(value[:])
	return id, nil
}

// ActivityReport returns deterministic, local-calendar time totals. It reads
// only UTC date partitions overlapping the requested range, bounds both date
// span and segment count, and clips boundary segments before aggregation.
func (c *Client) ActivityReport(startDate, endDate, zoneName string) (activity.ReportResult, error) {
	if c == nil || c.data == nil {
		return activity.ReportResult{}, ErrNotConfigured
	}
	reader, ok := c.data.(ActivityData)
	if !ok {
		return activity.ReportResult{}, ErrNotConfigured
	}
	start, err := time.Parse("2006-01-02", startDate)
	if err != nil || start.Format("2006-01-02") != startDate {
		return activity.ReportResult{}, ErrInvalidRequest
	}
	last, err := time.Parse("2006-01-02", endDate)
	if err != nil || last.Format("2006-01-02") != endDate || last.Before(start) || int(last.Sub(start)/(24*time.Hour))+1 > MaxActivityReportDays {
		return activity.ReportResult{}, ErrInvalidRequest
	}
	zone, err := loadZone(zoneName)
	if err != nil {
		return activity.ReportResult{}, ErrInvalidRequest
	}
	startLocal := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, zone)
	endLocal := time.Date(last.Year(), last.Month(), last.Day()+1, 0, 0, 0, 0, zone)
	var dates []string
	for day := startLocal.UTC().Truncate(24 * time.Hour); day.Before(endLocal); day = day.Add(24 * time.Hour) {
		dates = append(dates, day.Format("2006-01-02"))
	}
	if len(dates) == 0 || len(dates) > maxActivityReportUTCdays {
		return activity.ReportResult{}, ErrInvalidRequest
	}
	segments, err := reader.ListActivitySegmentsForDates(context.Background(), dates, MaxActivityReportSegments)
	if err != nil {
		return activity.ReportResult{}, err
	}
	if len(segments) > MaxActivityReportSegments {
		return activity.ReportResult{}, ErrActivityLimit
	}
	for i := range segments {
		segment := &segments[i]
		if segment.Validate() != nil {
			return activity.ReportResult{}, ErrInvalidRequest
		}
		if segment.WallEnd.After(startLocal) && segment.WallStart.Before(endLocal) {
			if segment.WallStart.Before(startLocal) {
				segment.WallStart = startLocal
			}
			if segment.WallEnd.After(endLocal) {
				segment.WallEnd = endLocal
			}
			segment.Duration = int64(segment.WallEnd.Sub(segment.WallStart))
		} else {
			segment.Duration = 0
		}
	}
	filtered := segments[:0]
	for _, segment := range segments {
		if segment.Duration > 0 {
			filtered = append(filtered, segment)
		}
	}
	return activity.Report(filtered, zone)
}

// HourSoFar returns a deterministic, model-free preview of the current UTC
// clock hour built from in-memory evidence and activity metadata. It never
// persists or calls a model. While memory is changing it returns an empty
// draft and no error.
func (c *Client) HourSoFar(ctx context.Context, now time.Time) (chronicle.Draft, error) {
	if c == nil || c.data == nil {
		return chronicle.Draft{}, ErrNotConfigured
	}
	epoch, stable := c.BeginMemoryRead()
	if !stable {
		return chronicle.Draft{}, nil
	}
	now = now.UTC()
	hour := chronicle.Hour{Start: now.Truncate(time.Hour), End: now.Truncate(time.Hour).Add(time.Hour)}
	if c.localAI != nil {
		c.localAI.mu.RLock()
		feed := c.localAI.chronicleFeed
		c.localAI.mu.RUnlock()
		if current, ok := feed.CurrentHour(); ok && current.Start.Equal(hour.Start) {
			hour = current
		}
	}
	var lines []chronicle.ActivityLine
	if reader, ok := c.data.(ActivityData); ok {
		segments, err := reader.ListActivitySegmentsForDates(ctx, []string{hour.Start.Format("2006-01-02")}, MaxActivityReportSegments)
		if err != nil {
			return chronicle.Draft{}, err
		}
		if len(segments) > MaxActivityReportSegments {
			return chronicle.Draft{}, ErrActivityLimit
		}
		clipped := segments[:0]
		for _, segment := range segments {
			if segment.Validate() != nil {
				return chronicle.Draft{}, ErrInvalidRequest
			}
			if segment.WallStart.Before(hour.Start) {
				segment.WallStart = hour.Start
			}
			if segment.WallEnd.After(now) {
				segment.WallEnd = now
			}
			if segment.Duration = int64(segment.WallEnd.Sub(segment.WallStart)); segment.Duration > 0 {
				clipped = append(clipped, segment)
			}
		}
		report, err := activity.Report(clipped, time.UTC)
		if err != nil {
			return chronicle.Draft{}, err
		}
		for _, bucket := range report.Apps {
			if bucket.Key != "" {
				lines = append(lines, chronicle.ActivityLine{Label: displayAppName(bucket.Key), Duration: time.Duration(bucket.Duration)})
			}
		}
		for _, bucket := range report.Domains {
			if bucket.Key != "" {
				lines = append(lines, chronicle.ActivityLine{Label: bucket.Key, Duration: time.Duration(bucket.Duration)})
			}
		}
	}
	draft := chronicle.DraftHour(hour, lines, now)
	if !c.MemoryReadCurrent(epoch) {
		return chronicle.Draft{}, nil
	}
	return draft, nil
}

func validDate(value string) bool {
	parsed, err := time.Parse("2006-01-02", value)
	return err == nil && parsed.Format("2006-01-02") == value
}

func validOpaqueID(value string) bool {
	return len(value) > 0 && len(value) <= 128 && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}
