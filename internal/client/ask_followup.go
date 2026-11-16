package client

import (
	"context"
	"encoding/hex"
	"slices"
	"strings"
	"time"

	"github.com/hazcod/blur/internal/appidentity"
	"github.com/hazcod/blur/internal/assistant"
)

// RetrieveSources is a bounded read of an earlier answer's handles, never a
// replay of its cached text. Raw sources are rehydrated, derived items are
// selected again from current storage, and current authored corrections win.
func (c *Client) RetrieveSources(ctx context.Context, plan assistant.Plan, ids []string) (assistant.Retrieval, error) {
	if c == nil || c.data == nil || ctx == nil {
		return assistant.Retrieval{}, ErrNotConfigured
	}
	if len(ids) > assistant.MaxEvidenceItems {
		return assistant.Retrieval{}, ErrInvalidRequest
	}
	for _, id := range ids {
		decoded, err := hex.DecodeString(id)
		if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != id {
			return assistant.Retrieval{}, ErrInvalidRequest
		}
	}
	if err := ctx.Err(); err != nil {
		return assistant.Retrieval{}, err
	}
	epoch, stable := c.BeginMemoryRead()
	if !stable {
		return assistant.Retrieval{}, ErrMemoryChanging
	}
	var result assistant.Retrieval
	if plan.SubjectFollowup {
		// Factual questions may need a source never cited in the preceding
		// answer. The planner pins the named subject and current filters;
		// MatchOnly forbids an unrelated recent-source fallback.
		if strings.TrimSpace(plan.Entity) == "" || len(plan.Terms) == 0 || !plan.MatchOnly {
			return assistant.Retrieval{}, ErrInvalidRequest
		}
		lookup := plan
		lookup.Kind = assistant.KindLookup
		fresh, err := c.RetrievePlan(ctx, lookup)
		if err != nil {
			return assistant.Retrieval{}, err
		}
		result = fresh
		if plan.Scope.IsZero() {
			// Current library corrections remain authoritative for unbounded
			// subject questions; they cannot establish what happened inside a
			// newly requested historical window.
			derived, err := c.inferredEntityEvidence(ctx, plan)
			if err != nil {
				return assistant.Retrieval{}, err
			}
			seen := make(map[string]bool)
			var evidence []assistant.Evidence
			for _, item := range append(derived, fresh.Evidence...) {
				if len(evidence) >= assistant.MaxEvidenceItems {
					break
				}
				if !seen[item.SourceID] && followupSourceInScope(item, plan) {
					evidence = append(evidence, item)
					seen[item.SourceID] = true
				}
			}
			result.Evidence = evidence
		}
	} else if plan.Kind == assistant.KindTime {
		direct, handled, err := c.AnswerDirect(ctx, plan)
		if err != nil {
			return assistant.Retrieval{}, err
		}
		if handled && direct.Text != "" {
			text := "Recorded foreground activity: " + direct.Text + " This measures saved app activity, not attention or productivity. It cannot account for periods when capture was paused or unavailable."
			result.Evidence = []assistant.Evidence{{SourceID: syntheticID("activity-followup", plan.Question, plan.Scope.Start.UTC().Format(time.RFC3339Nano), plan.Scope.End.UTC().Format(time.RFC3339Nano), direct.Text), AppID: "blur.activity", AppName: "Recorded activity", Text: text}}
		}
	} else if len(ids) > 0 {
		raw, err := c.hydrateSources(ctx, uniqueStrings(ids), nil, assistant.MaxEvidenceItems)
		if err != nil {
			return assistant.Retrieval{}, err
		}
		for _, item := range raw {
			if !plan.Scope.Explicit && item.AppID != appidentity.BundleID && followupSourceInScope(item, plan) {
				result.Evidence = append(result.Evidence, item)
			}
		}
		// Current summaries/corrections may use app-owned synthetic handles. A
		// fresh selection replaces only the anchored items; it never admits the
		// whole recent window to a request to rewrite an earlier answer.
		fresh, err := c.RetrievePlan(ctx, plan)
		if err != nil {
			return assistant.Retrieval{}, err
		}
		result.Diagnosis = fresh.Diagnosis
		var selected []assistant.Evidence
		for _, item := range fresh.Evidence {
			if slices.Contains(ids, item.SourceID) || plan.Kind == assistant.KindEntity && item.Origin == assistant.OriginUserCorrection {
				selected = append(selected, item)
			}
		}
		// Current task/profile direct reads are safe only for handles which still
		// match; changed/deleted records cannot validate an old answer's prose.
		if direct, handled, err := c.AnswerDirect(ctx, plan); err != nil {
			return assistant.Retrieval{}, err
		} else if handled {
			for _, item := range direct.Sources {
				if slices.Contains(ids, item.SourceID) {
					selected = append(selected, item)
				}
			}
			result.Targets = direct.Targets
		}
		seen := map[string]bool{}
		for _, item := range selected {
			seen[item.SourceID] = true
		}
		for _, item := range result.Evidence {
			if !seen[item.SourceID] {
				selected = append(selected, item)
				seen[item.SourceID] = true
			}
		}
		if len(selected) > assistant.MaxEvidenceItems {
			selected = selected[:assistant.MaxEvidenceItems]
		}
		result.Evidence = selected
		for _, target := range fresh.Targets {
			if seen[target.SourceID] {
				result.Targets = append(result.Targets, target)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return assistant.Retrieval{}, err
	}
	if !c.MemoryReadCurrent(epoch) {
		return assistant.Retrieval{}, ErrMemoryChanging
	}
	return result, nil
}

func followupSourceInScope(item assistant.Evidence, plan assistant.Plan) bool {
	if len(plan.AppIDs) > 0 && !slices.ContainsFunc(plan.AppIDs, func(id string) bool { return strings.EqualFold(id, item.AppID) }) {
		return false
	}
	if len(plan.Domains) > 0 && !slices.ContainsFunc(plan.Domains, func(domain string) bool { return strings.EqualFold(domain, item.Domain) }) {
		return false
	}
	return true
}

var _ assistant.SourceRetriever = (*Client)(nil)
