package client

import (
	"context"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/semantic"
	"github.com/hazcod/blur/internal/tasks"
)

const maxTaskSemanticCandidates = 256

type taskSearchMatch struct {
	item     tasks.Item
	evidence assistant.Evidence
	target   assistant.TargetRef
	at       time.Time
	score    float32
	exact    bool
}

type taskSearchPage struct {
	matches           []taskSearchMatch
	hasMore           bool
	semanticIssue     string
	historyIncomplete bool
}

// searchTasks ranks only current task titles. State and scheduling evidence is
// assembled from a fresh read of each winner, never from an immutable note.
func (c *Client) searchTasks(ctx context.Context, question string, terms []string, scope semantic.Query, limit int, matchOnly bool, linkedSourceIDs ...string) (taskSearchPage, error) {
	return c.searchTasksMode(ctx, question, terms, scope, limit, matchOnly, false, linkedSourceIDs...)
}

func (c *Client) searchTasksMode(ctx context.Context, question string, terms []string, scope semantic.Query, limit int, matchOnly, exactOnly bool, linkedSourceIDs ...string) (taskSearchPage, error) {
	linkedSources := make(map[string]struct{}, len(linkedSourceIDs))
	for _, id := range linkedSourceIDs {
		if id != "" {
			linkedSources[id] = struct{}{}
		}
	}
	return c.searchTaskCandidates(ctx, question, terms, scope, limit, matchOnly, nil, exactOnly, linkedSources)
}

func (c *Client) searchTaskCandidates(ctx context.Context, question string, terms []string, scope semantic.Query, limit int, matchOnly bool, taskFilter *taskQuery, exactOnly bool, linkedSources map[string]struct{}) (taskSearchPage, error) {
	// A source-linked project task is an exact relation even when its title
	// doesn't repeat the project name. Only the outstanding-work flow supplies
	// these IDs; ordinary natural-language ranking remains unchanged.
	var result taskSearchPage
	if len(scope.Domains) > 0 || len(scope.AppIDs) > 0 && !containsTaskApp(scope.AppIDs) {
		return result, nil
	}
	epoch, stable := c.BeginMemoryRead()
	if !stable {
		return result, ErrMemoryChanging
	}
	items, err := c.liveTasks(ctx)
	if err != nil {
		return result, err
	}
	legacySemantic := c.localAI == nil && c.memoryIndex != nil && c.memoryIndex.Settings().Enabled
	selectedSemantic := c.localAI != nil
	semanticEnabled := (legacySemantic || selectedSemantic) && !exactOnly
	semanticLimit := maxTaskSemanticCandidates
	if selectedSemantic {
		semanticLimit = recallCandidates
	}
	var candidates []taskSearchMatch
	var semanticItems []tasks.Item
	matchTimes := make(map[string]time.Time)
	now := c.askClock()
	for _, item := range items {
		if assistant.IsOutstandingQuestion(question) && item.State != tasks.Open {
			continue
		}
		at := item.UpdatedAt
		if taskFilter != nil {
			var matched, incomplete bool
			at, matched, incomplete = matchTaskQuery(item, *taskFilter, now)
			result.historyIncomplete = result.historyIncomplete || incomplete
			if !matched {
				continue
			}
		} else if !taskInSearchScope(item, scope) {
			continue
		}
		matchTimes[item.ID] = at
		var tokens []string
		if len(terms) > 0 {
			var err error
			// Titles are bounded documents, not queries. Every accepted title
			// must remain searchable even when it has more than 16 words.
			tokens, err = search.NormalizeTokens(item.Title, len(item.Title)+1)
			if err != nil {
				continue
			}
		}
		exact := len(terms) > 0 || taskFilter != nil
		for _, term := range terms {
			found := false
			for _, token := range tokens {
				if token == term {
					found = true
					break
				}
			}
			if !found {
				exact = false
				break
			}
		}
		if !exact && len(linkedSources) > 0 {
			for _, source := range item.Sources() {
				if _, ok := linkedSources[source]; ok {
					exact = true
					break
				}
			}
		}
		if exact {
			candidates = append(candidates, taskSearchMatch{item: item, at: at, exact: true, score: 1})
		} else if !semanticEnabled {
			continue
		} else if len(semanticItems) < semanticLimit {
			semanticItems = append(semanticItems, item)
		} else {
			result.hasMore = true
		}
	}
	queryCtx := ctx
	if semanticEnabled && len(semanticItems) > 0 && (legacySemantic || len(candidates) == 0) {
		texts := make([]string, len(semanticItems))
		for n, item := range semanticItems {
			texts[n] = item.Title
		}
		var scores []float32
		var rankErr error
		if legacySemantic {
			legacyCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
			scores, rankErr = c.memoryIndex.RankCandidates(legacyCtx, question, texts)
			cancel()
		} else {
			var model configuredAskModel
			var generator assistant.PreparedGeneralGenerator
			var finish func()
			model, generator, queryCtx, finish, rankErr = c.beginSelectedRecall(ctx, epoch)
			if rankErr != nil {
				return taskSearchPage{}, rankErr
			}
			defer finish()
			if model == nil {
				rankErr = assistant.ErrAskUnavailable
			} else {
				for i, text := range texts {
					start, end := selectedRecallPassage(text, nil)
					texts[i] = text[start:end]
				}
				scores, rankErr = rankSelectedRecall(queryCtx, model, generator, question, texts, true)
			}
			if queryCtx.Err() != nil {
				return taskSearchPage{}, queryCtx.Err()
			}
			// A finite candidate/title budget cannot establish exhaustive recall.
			result.semanticIssue = "limited"
		}
		clear(texts)
		if rankErr == nil && len(scores) == len(semanticItems) {
			minimum := float32(.3)
			if matchOnly || selectedSemantic {
				minimum = .55
			}
			for n, score := range scores {
				if score >= minimum && score <= 1.001 && !math.IsNaN(float64(score)) {
					item := semanticItems[n]
					candidates = append(candidates, taskSearchMatch{item: item, at: matchTimes[item.ID], score: score})
				}
			}
		} else {
			result.semanticIssue = "unavailable"
		}
		clear(scores)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if taskFilter != nil && taskFilter.Next && !candidates[i].at.Equal(candidates[j].at) {
			return candidates[i].at.Before(candidates[j].at)
		}
		if candidates[i].exact != candidates[j].exact {
			return candidates[i].exact
		}
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		return candidates[i].item.ID < candidates[j].item.ID
	})
	repository, _ := c.taskRepository()
	for _, candidate := range candidates {
		if len(result.matches) >= limit {
			result.hasMore = true
			break
		}
		current, found, err := repository.GetTask(ctx, candidate.item.ID)
		if err != nil {
			return taskSearchPage{}, err
		}
		before, _ := taskEvidence(candidate.item, candidate.item.UpdatedAt, "")
		evidence, target := taskEvidence(current, current.UpdatedAt, "")
		if !found || evidence.SourceID != before.SourceID {
			continue
		}
		if taskFilter != nil {
			evidence, target = taskEvidence(current, candidate.at, taskFilter.HistoryState)
		}
		candidate.item, candidate.evidence, candidate.target = current, evidence, target
		result.matches = append(result.matches, candidate)
	}
	if err := queryCtx.Err(); err != nil {
		return taskSearchPage{}, err
	}
	if !c.MemoryReadCurrent(epoch) {
		return taskSearchPage{}, ErrMemoryChanging
	}
	return result, nil
}

func containsTaskApp(ids []string) bool {
	for _, id := range ids {
		if id == "blur.tasks" {
			return true
		}
	}
	return false
}

func taskInSearchScope(item tasks.Item, scope semantic.Query) bool {
	if scope.Start.IsZero() && scope.End.IsZero() {
		return true
	}
	at := item.UpdatedAt
	return (scope.Start.IsZero() || !at.Before(scope.Start)) && (scope.End.IsZero() || at.Before(scope.End))
}

func mergeTaskSearchHits(memory []SearchHit, taskPage taskSearchPage, limit int) ([]SearchHit, bool) {
	var hits []SearchHit
	used, more := 0, taskPage.hasMore
	add := func(hit SearchHit) {
		bytes := len(hit.Text) + len(hit.AppID) + len(hit.Domain)
		if len(hits) >= limit || bytes > MaxSearchBytes-used {
			more = true
			return
		}
		used += bytes
		hits = append(hits, hit)
	}
	// Keep both collections visible when either fills its candidate budget.
	// Task relevance scores cannot be compared with the source index's ranking.
	for index := 0; index < max(len(memory), len(taskPage.matches)); index++ {
		if index < len(taskPage.matches) {
			match := taskPage.matches[index]
			item := tasks.Clone(match.item)
			ev := match.evidence
			add(SearchHit{SourceID: ev.SourceID, AppID: ev.AppID, Text: ev.Text, MatchEnd: ev.MatchEnd, Task: &item})
		}
		if index < len(memory) {
			add(memory[index])
		}
	}
	return hits, more
}

// validateTaskTargets prevents an edited record or native receipt from being
// published as current task evidence after slow answer generation.
func (c *Client) validateTaskTargets(ctx context.Context, result assistant.Result) error {
	repository, ok := c.taskRepository()
	if !ok {
		return nil
	}
	for _, target := range result.Targets {
		if target.Kind != "task" {
			continue
		}
		item, found, err := repository.GetTask(ctx, target.ID)
		if err != nil {
			return err
		}
		if !found || item.Revision != target.Revision {
			return ErrMemoryChanging
		}
		current, _ := taskEvidence(item, item.UpdatedAt, "")
		valid := false
		for _, evidence := range result.Sources {
			if evidence.SourceID == target.SourceID && strings.HasSuffix(evidence.Text, current.Text) {
				valid = true
				break
			}
		}
		if !valid {
			return ErrMemoryChanging
		}
	}
	return ctx.Err()
}

// Keep a bounded mixture of matching current tasks and source memory. A large
// set of semantic task candidates cannot displace every exact source match.
func mergeTaskEvidence(memory []assistant.Evidence, page taskSearchPage, limit int) ([]assistant.Evidence, []assistant.TargetRef) {
	var evidence []assistant.Evidence
	var targets []assistant.TargetRef
	used := 0
	seen := map[string]bool{}
	add := func(item assistant.Evidence, target *assistant.TargetRef) {
		if len(evidence) >= limit || seen[item.SourceID] || len(item.Text) > MaxSearchBytes-used {
			return
		}
		used += len(item.Text)
		seen[item.SourceID] = true
		evidence = append(evidence, item)
		if target != nil {
			ref := *target
			ref.Label = "T" + strconv.Itoa(len(targets)+1)
			targets = append(targets, ref)
		}
	}
	for index := 0; index < max(len(memory), len(page.matches)); index++ {
		if index < len(page.matches) {
			match := page.matches[index]
			add(match.evidence, &match.target)
		}
		if index < len(memory) {
			add(memory[index], nil)
		}
	}
	return evidence, targets
}
