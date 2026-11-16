package client

import (
	"context"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/knowledge/enrich"
)

func (c *localAIController) SetLibraryRuntime(sources enrich.Sources, repository enrich.Repository, options enrich.Options) {
	if c == nil {
		return
	}
	c.configMu.Lock()
	defer c.configMu.Unlock()
	c.mu.Lock()
	c.librarySources, c.libraryRepo, c.libraryOptions = sources, repository, options
	previous := c.replaceLibraryWorkerLocked()
	c.mu.Unlock()
	if previous != nil {
		previous.Close()
	}
}

// Caller closes the returned worker outside mu; callbacks can read settings.
func (c *localAIController) replaceLibraryWorkerLocked() *enrich.Service {
	previous := c.libraryWorker
	c.libraryWorker = nil
	settings := c.settings
	if settings.Enabled && settings.AutomaticLibrary && !c.needsRepair && isLocalCompletionProvider(effectiveProvider(settings)) && c.libraryRepo != nil && c.librarySources != nil {
		generator, err := newConfiguredKnowledgeGenerator(settings)
		if err == nil {
			c.libraryWorker, err = enrich.NewService(c.librarySources, c.libraryRepo, generator, c.libraryOptions)
			if err == nil {
				c.libraryWorker.Start()
			} else {
				generator.CloseIdleConnections()
			}
		}
	}
	return previous
}

func (c *localAIController) SetAutomaticLibrary(ctx context.Context, enabled bool) error {
	if c == nil || ctx == nil {
		return errLocalAIUnavailable
	}
	c.configMu.Lock()
	defer c.configMu.Unlock()
	c.mu.RLock()
	settings, repair := c.settings, c.needsRepair
	c.mu.RUnlock()
	settings.AutomaticLibrary = enabled
	if repair || validateLocalAISettings(settings) != nil {
		return errLocalAIUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := writeLocalAISettings(c.path, settings); err != nil {
		return errLocalAIUnavailable
	}
	c.mu.Lock()
	c.settings = settings
	previous := c.replaceLibraryWorkerLocked()
	c.mu.Unlock()
	if previous != nil {
		previous.Close()
	}
	if c.libraryOptions.OnChange != nil {
		c.libraryOptions.OnChange()
	}
	return nil
}

func (c *localAIController) libraryService() *enrich.Service {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.libraryWorker
}
func (c *localAIController) NotifyLibrarySource(id string) {
	if s := c.libraryService(); s != nil {
		s.NotifySource(id)
	}
}
func (c *localAIController) WakeLibrary() {
	if s := c.libraryService(); s != nil {
		s.Wake()
	}
}
func (c *localAIController) RetryLibrary() {
	if s := c.libraryService(); s != nil {
		s.RetryInvalidOutputs()
	}
}
func (c *localAIController) WakeLibraryCaptures() {
	if s := c.libraryService(); s != nil {
		s.WakeCaptures()
	}
}
func (c *localAIController) InvalidateLibrary() {
	if s := c.libraryService(); s != nil {
		s.Invalidate()
	}
}

func (c *Client) LibraryStatus() enrich.Status {
	if c == nil || c.localAI == nil {
		return enrich.Status{State: "disabled"}
	}
	if s := c.localAI.libraryService(); s != nil {
		return s.Status()
	}
	if c.localAI.Settings().AutomaticLibrary {
		return enrich.Status{State: "unavailable", ErrorCode: "settings_invalid"}
	}
	return enrich.Status{State: "disabled"}
}

func (c *Client) SetAutomaticLibrary(ctx context.Context, enabled bool) error {
	if c == nil || c.localAI == nil {
		return ErrNotConfigured
	}
	return c.localAI.SetAutomaticLibrary(ctx, enabled)
}

// CommitDerivedMemory holds the mutation fence for one bounded encrypted write.
// A model response obtained before deletion cannot be published after deletion.
// The write must not call Client or invoke change callbacks under this lock.
func (c *Client) CommitDerivedMemory(epoch uint64, write func() error) error {
	if c == nil || write == nil {
		return ErrMemoryChanging
	}
	c.memoryChangeMu.Lock()
	defer c.memoryChangeMu.Unlock()
	if c.memoryMutationDepth != 0 || c.memoryMutationEpoch != epoch {
		return ErrMemoryChanging
	}
	return write()
}

func (c *Client) inferredRepository() (enrich.Repository, bool) {
	if c == nil || c.data == nil {
		return nil, false
	}
	repository, ok := c.data.(enrich.Repository)
	return repository, ok
}

// knowledgePage preserves existing manual cursors, then continues into the
// inferred directory. Every continuation is the last visible entity's ID.
func (c *Client) knowledgePage(after string) (knowledge.Page, error) {
	return c.knowledgePageContext(context.Background(), after)
}

func (c *Client) knowledgePageContext(ctx context.Context, after string) (knowledge.Page, error) {
	page, err := c.readKnowledgePageContext(ctx, after)
	if err != nil {
		return page, err
	}
	c.libraryAttributions(ctx, page.Entities)
	if err := ctx.Err(); err != nil {
		return knowledge.Page{}, err
	}
	return page, nil
}

func (c *Client) readKnowledgePageContext(ctx context.Context, after string) (knowledge.Page, error) {
	if c == nil || c.data == nil || ctx == nil || len(after) > 128 {
		return knowledge.Page{}, ErrInvalidRequest
	}
	if err := ctx.Err(); err != nil {
		return knowledge.Page{}, err
	}
	checked := func(page knowledge.Page, err error) (knowledge.Page, error) {
		if cancelled := ctx.Err(); cancelled != nil {
			return knowledge.Page{}, cancelled
		}
		return page, err
	}
	repository, ok := c.inferredRepository()
	if !ok {
		return checked(c.data.ListKnowledge(ctx, knowledge.PageRequest{Limit: 50, After: after}))
	}
	if enrich.IsEntityID(after) {
		return checked(repository.ListInferredKnowledge(ctx, enrich.PageRequest{After: after, Limit: 50}))
	}
	page, err := checked(c.data.ListKnowledge(ctx, knowledge.PageRequest{Limit: 50, After: after}))
	if err != nil || page.HasMore {
		return page, err
	}
	remaining := 50 - len(page.Entities)
	if remaining == 0 {
		next, err := checked(repository.ListInferredKnowledge(ctx, enrich.PageRequest{Limit: 1}))
		if err != nil {
			return knowledge.Page{}, err
		}
		page.HasMore = len(next.Entities) > 0
		if page.HasMore {
			page.Next = page.Entities[len(page.Entities)-1].ID
		} else {
			page.Next = ""
		}
		return checked(page, nil)
	}
	next, err := checked(repository.ListInferredKnowledge(ctx, enrich.PageRequest{Limit: remaining}))
	if err != nil {
		return knowledge.Page{}, err
	}
	page.Entities = append(page.Entities, next.Entities...)
	page.HasMore = next.HasMore
	page.Next = next.Next
	return checked(page, nil)
}

func (c *Client) InferredKnowledge(ctx context.Context, id string) (knowledge.Entity, error) {
	repository, ok := c.inferredRepository()
	if !ok || ctx == nil {
		return knowledge.Entity{}, ErrNotConfigured
	}
	entity, found, err := repository.GetInferredKnowledge(ctx, id)
	if err != nil {
		return knowledge.Entity{}, err
	}
	if !found {
		return knowledge.Entity{}, ErrInvalidRequest
	}
	entity = c.libraryAttribution(ctx, entity)
	if err := ctx.Err(); err != nil {
		return knowledge.Entity{}, err
	}
	return entity, nil
}

func (c *Client) ChangeInferredFact(ctx context.Context, entityID, factID, text string, dismiss bool) error {
	repository, ok := c.inferredRepository()
	if !ok || ctx == nil {
		return ErrNotConfigured
	}
	c.NotifyMemoryChanging()
	defer c.NotifyMemoryStable()
	if dismiss {
		return repository.DismissInferredFact(ctx, entityID, factID)
	}
	return repository.CorrectInferredFact(ctx, entityID, factID, strings.TrimSpace(text))
}

func (c *Client) inferredEntityEvidence(ctx context.Context, plan assistant.Plan) ([]assistant.Evidence, error) {
	repository, ok := c.inferredRepository()
	if !ok || strings.TrimSpace(plan.Entity) == "" {
		return nil, nil
	}
	entities, err := repository.FindInferredKnowledge(ctx, plan.Entity)
	if err != nil {
		return nil, err
	}
	return c.inferredEntitiesEvidence(ctx, plan, entities, true)
}

func (c *Client) inferredEntitiesEvidence(ctx context.Context, plan assistant.Plan, entities []knowledge.Entity, includeSources bool) ([]assistant.Evidence, error) {
	var evidence []assistant.Evidence
	var sources []string
	for _, entity := range entities {
		app, domain := "", ""
		if entity.Inference != nil {
			app, domain = entity.Inference.AppID, entity.Inference.Domain
		}
		if len(plan.AppIDs) > 0 && !slices.Contains(plan.AppIDs, app) || len(plan.Domains) > 0 && !slices.Contains(plan.Domains, domain) {
			continue
		}
		// Corrections use separate evidence labels rather than pretending they
		// were contained in an older captured source.
		for _, origin := range []assistant.EvidenceOrigin{assistant.OriginUserCorrection, assistant.OriginInferred} {
			var facts []string
			for _, fact := range entity.Facts {
				if fact.State == knowledge.Dismissed {
					continue
				}
				user := false
				for _, source := range fact.Sources {
					user = user || source.UserAuthored
				}
				if user != (origin == assistant.OriginUserCorrection) {
					continue
				}
				facts = append(facts, fact.Text)
			}
			if origin == assistant.OriginInferred {
				for _, loop := range entity.OpenLoops {
					if loop.State == knowledge.Dismissed || strings.TrimSpace(loop.Text) == "" {
						continue
					}
					if loop.State == knowledge.Confirmed || loop.State == knowledge.Deterministic {
						facts = append(facts, "Open: "+loop.Text)
					} else {
						facts = append(facts, "Possible open item (inferred, not confirmed): "+loop.Text)
					}
					for _, source := range loop.Sources {
						if source.ID != "" {
							sources = append(sources, source.ID)
						}
					}
				}
			}
			if len(facts) == 0 {
				continue
			}
			text := entity.Name + "\n" + strings.Join(facts, "\n")
			item := assistant.Evidence{SourceID: syntheticID("library", entity.ID, string(origin)), AppID: app, AppName: "Library", Domain: domain, Text: text, Layer: assistant.LayerDigest, Origin: origin}
			// Preserve complete inspectable text. Context packing records any
			// excerpt as truncated rather than silently replacing the evidence.
			if len(text) > entityTextBytes {
				item.MatchEnd = entityTextBytes
				for !utf8.RuneStart(text[item.MatchEnd]) {
					item.MatchEnd--
				}
			}
			evidence = append(evidence, item)
			if len(evidence) >= planEvidenceItems/2 {
				break
			}
		}
		sources = append(sources, entity.ActivitySourceIDs...)
		if len(evidence) >= planEvidenceItems/2 {
			break
		}
	}
	if len(evidence) == 0 {
		return nil, nil
	}
	if !includeSources {
		return evidence, nil
	}
	linked, err := c.hydrateSources(ctx, uniqueStrings(sources), nil, planEvidenceItems-len(evidence))
	if err != nil {
		return nil, err
	}
	return append(evidence, linked...), nil
}

const maxTopicKnowledgeCandidates = 16

// Durable stores can resolve all exact subject names in one encrypted read
// snapshot. Keep the Repository fallback for in-memory and older adapters.
type inferredKnowledgeBatchSearcher interface {
	FindInferredKnowledgeByNames(context.Context, []string, int) ([]knowledge.Entity, bool, error)
}

// inferredTopicEvidence retrieves corrected and inferred Library facts when a
// normal topic question contains an exact saved subject name. It keeps the
// model-assisted source search path intact and adds only name-index matches.
func (c *Client) inferredTopicEvidence(ctx context.Context, plan assistant.Plan) ([]assistant.Evidence, bool, []string, error) {
	repository, ok := c.inferredRepository()
	if !ok || len(plan.Terms) == 0 {
		return nil, false, nil, nil
	}
	queries := topicKnowledgeQueries(plan.Terms)
	entities := make([]knowledge.Entity, 0, maxTopicKnowledgeCandidates)
	limited := false
	if batch, ok := repository.(inferredKnowledgeBatchSearcher); ok {
		matches, more, err := batch.FindInferredKnowledgeByNames(ctx, queries, maxTopicKnowledgeCandidates)
		if err != nil {
			return nil, false, nil, err
		}
		limited = more
		entities = append(entities, matches...)
	} else {
		seen := make(map[string]bool, maxTopicKnowledgeCandidates)
		for _, query := range queries {
			if err := ctx.Err(); err != nil {
				return nil, false, nil, err
			}
			matches, err := repository.FindInferredKnowledge(ctx, query)
			if err != nil {
				return nil, false, nil, err
			}
			for _, entity := range matches {
				app, domain := "", ""
				if entity.Inference != nil {
					app, domain = entity.Inference.AppID, entity.Inference.Domain
				}
				if len(plan.AppIDs) > 0 && !slices.Contains(plan.AppIDs, app) || len(plan.Domains) > 0 && !slices.Contains(plan.Domains, domain) || seen[entity.ID] {
					continue
				}
				if len(entities) == maxTopicKnowledgeCandidates {
					limited = true
					break
				}
				seen[entity.ID] = true
				entities = append(entities, entity)
			}
			if limited {
				break
			}
		}
	}
	seen := make(map[string]bool, len(entities))
	filtered := entities[:0]
	for _, entity := range entities {
		app, domain := "", ""
		if entity.Inference != nil {
			app, domain = entity.Inference.AppID, entity.Inference.Domain
		}
		if len(plan.AppIDs) > 0 && !slices.Contains(plan.AppIDs, app) || len(plan.Domains) > 0 && !slices.Contains(plan.Domains, domain) || seen[entity.ID] {
			continue
		}
		seen[entity.ID] = true
		filtered = append(filtered, entity)
	}
	entities = filtered
	if len(entities) == 0 {
		return nil, limited, nil, nil
	}
	limited = limited || len(entities) > planEvidenceItems/2
	evidence, err := c.inferredEntitiesEvidence(ctx, plan, entities, false)
	if err != nil {
		return nil, limited, nil, err
	}
	// A correction records exactly which captured sources it supersedes. Keep
	// those original captures available to Search, but do not show their stale
	// fact alongside the correction in an ordinary topic Ask.
	superseded := make(map[string]bool)
	for _, entity := range entities {
		corrected, hasSupersededSources := false, false
		for _, fact := range entity.Facts {
			if fact.State == knowledge.Dismissed {
				continue
			}
			for _, source := range fact.Sources {
				if source.UserAuthored {
					corrected = true
					for _, sourceID := range fact.Supersedes {
						superseded[sourceID] = true
						hasSupersededSources = true
					}
				}
			}
		}
		// Older corrected records predate the explicit supersession list. Their
		// fact-to-source edge was already replaced, so conservatively suppress
		// this entity's activity sources from topic answers while leaving Search
		// untouched.
		if corrected && !hasSupersededSources {
			for _, sourceID := range entity.ActivitySourceIDs {
				superseded[sourceID] = true
			}
		}
	}
	ids := make([]string, 0, len(superseded))
	for sourceID := range superseded {
		ids = append(ids, sourceID)
	}
	sort.Strings(ids)
	return evidence, limited, ids, nil
}

func topicKnowledgeQueries(terms []string) []string {
	seen := make(map[string]bool, 12)
	queries := make([]string, 0, 12)
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] || len(queries) == 12 {
			return
		}
		seen[value] = true
		queries = append(queries, value)
	}
	// Longer phrases prefer exact multiword Library names. Reverse order helps
	// when a project is the final noun in a natural-language question.
	for width := min(3, len(terms)); width >= 2; width-- {
		for start := len(terms) - width; start >= 0; start-- {
			add(strings.Join(terms[start:start+width], " "))
		}
	}
	for index := len(terms) - 1; index >= 0; index-- {
		add(terms[index])
	}
	if len(queries) < 12 {
		for index := 0; index < len(terms); index++ {
			add(terms[index])
		}
	}
	return queries
}
