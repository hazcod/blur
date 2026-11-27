package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/profile"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/tasks"
)

func profileEvidence(record profile.Record) assistant.Evidence {
	sum := sha256.Sum256([]byte("blur-explicit-profile-v1:" + record.ID + ":" + strconv.FormatUint(record.Revision, 10)))
	return assistant.Evidence{SourceID: hex.EncodeToString(sum[:]), AppID: "blur.profile", AppName: "Explicit preference", Text: record.Text, Origin: assistant.OriginUserFact, CapturedAt: record.UpdatedAt}
}

func (c *Client) PersonalAnswerContext(ctx context.Context, plan assistant.Plan) (assistant.PersonalContext, error) {
	repository, ok := c.profileRepository()
	if !ok {
		return assistant.PersonalContext{}, nil
	}
	records, err := profile.ReadAll(ctx, repository)
	if err != nil {
		return assistant.PersonalContext{}, err
	}
	selected := profile.Select(records, plan.Question, profile.Context{AppIDs: plan.AppIDs, Domains: plan.Domains}, plan.Kind != assistant.KindGeneral)
	result := assistant.PersonalContext{Omitted: selected.Omitted}
	for _, record := range selected.Rules {
		result.WritingRules = append(result.WritingRules, record.Text)
	}
	for _, record := range selected.Facts {
		result.Facts = append(result.Facts, profileEvidence(record))
	}
	if _, ok := c.taskRepository(); ok && plan.Kind != assistant.KindGeneral {
		query := taskQuery{State: tasks.Open}
		include := false
		if plan.Kind == assistant.KindRecap {
			start, end, _, _ := c.planWindow(plan)
			query = taskQuery{HistoryState: tasks.Completed, Start: start, End: end}
			include = true
		} else if plan.Scope.IsZero() || plan.Scope.End.After(c.askClock()) {
			include = taskContextQuestion(plan)
		}
		if include {
			answer, err := c.readTaskQuery(ctx, query)
			if err != nil {
				return assistant.PersonalContext{}, err
			}
			result.Facts = append(result.Facts, answer.Sources...)
			result.Targets = answer.Targets
		}
	}
	if len(plan.TechnicalSubjects) > 0 {
		facts := result.Facts[:0]
		for _, item := range result.Facts {
			if technicalSubjectSupport(plan.TechnicalSubjects, item.Text) {
				facts = append(facts, item)
			}
		}
		result.Facts = facts
	}
	return result, nil
}

// directProfile answers explicit inspection questions from authored records.
// It quotes their exact text and provenance, so it needs no model or network.
func (c *Client) directProfile(ctx context.Context, plan assistant.Plan) (assistant.DirectAnswer, bool, error) {
	q := strings.ToLower(strings.Join(strings.Fields(plan.Question), " "))
	kind, all, handled := profile.Fact, false, false
	inspection := false
	for _, prefix := range []string{"what are ", "which are ", "show ", "list ", "tell me my ", "what writing ", "which writing "} {
		if strings.HasPrefix(q, prefix) {
			inspection = true
		}
	}
	for _, phrase := range []string{"my writing rules", "my writing style rules", "my style rules", "my saved rules"} {
		if inspection && strings.Contains(q, phrase) {
			kind, all, handled = profile.WritingRule, true, true
		}
	}
	for _, phrase := range []string{"what do you know about me", "what have i told you about myself", "my preferences", "my personal preferences", "my saved preferences"} {
		if strings.Contains(q, phrase) {
			kind, all, handled = profile.Fact, true, true
		}
	}
	if (strings.HasPrefix(q, "what is ") || strings.HasPrefix(q, "what's ") || strings.HasPrefix(q, "which is ") || inspection) && (strings.Contains(q, "my preferred ") || strings.Contains(q, "my preference for ")) {
		handled = true
	}
	if !handled {
		return assistant.DirectAnswer{}, false, nil
	}
	repository, ok := c.profileRepository()
	if !ok {
		return assistant.DirectAnswer{}, false, nil
	}
	records, err := profile.ReadAll(ctx, repository)
	if err != nil {
		return assistant.DirectAnswer{}, true, err
	}
	if !all {
		selected := profile.Select(records, plan.Question, profile.Context{AppIDs: plan.AppIDs, Domains: plan.Domains}, true)
		records = selected.Facts
	}
	sort.SliceStable(records, func(i, j int) bool {
		if !records[i].UpdatedAt.Equal(records[j].UpdatedAt) {
			return records[i].UpdatedAt.After(records[j].UpdatedAt)
		}
		return records[i].ID < records[j].ID
	})
	answer := assistant.DirectAnswer{}
	label := "preferences"
	if kind == profile.WritingRule {
		label = "writing rules"
	}
	lines := []string{"Your saved " + label + ":"}
	used, omitted := 0, 0
	for _, record := range records {
		if record.Kind != kind {
			continue
		}
		line := "• " + record.Text
		if !record.Enabled {
			line += " (paused)"
		}
		if record.Scope != (profile.Scope{}) {
			line += " (" + profileScopeCopy(record.Scope) + ")"
		}
		if len(answer.Sources) >= assistant.MaxEvidenceSources || used+len(line) > profile.MaxContextBytes {
			omitted++
			continue
		}
		source := profileEvidence(record)
		answer.Sources = append(answer.Sources, source)
		answer.SourceIDs = append(answer.SourceIDs, source.SourceID)
		lines = append(lines, line)
		used += len(line)
	}
	if len(answer.Sources) == 0 {
		answer.Text = "No matching explicit " + label + " are saved."
		return answer, true, nil
	}
	if omitted > 0 {
		lines = append(lines, strconv.Itoa(omitted)+" more saved items are available in Preferences.")
	}
	answer.Text = strings.Join(lines, "\n")
	return answer, true, nil
}

type ProfileResult struct {
	Record profile.Record
	Epoch  uint64
}

func askEvidenceCaption(source assistant.Evidence) string {
	if source.Layer == assistant.LayerCompaction {
		return "Generated summary of saved source text. Original text is available."
	}
	if source.Origin == assistant.OriginUserCorrection {
		return "Your correction in Library."
	}
	if source.Origin == assistant.OriginInferred {
		return "Inferred Library memory. Review if it looks wrong."
	}
	if source.Origin == assistant.OriginUserFact {
		caption := "Explicit preference. Saved by you."
		if !source.CapturedAt.IsZero() {
			caption += " Updated " + source.CapturedAt.Local().Format("2 Jan 2006, 15:04") + "."
		}
		return caption
	}
	if name := recapEvidenceName(source.Layer); name != "" {
		caption := name + "."
		if len(source.SourceIDs) != 0 {
			unit := "saved sources"
			if len(source.SourceIDs) == 1 {
				unit = "saved source"
			}
			caption += " Based on " + strconv.Itoa(len(source.SourceIDs)) + " " + unit + "."
		}
		if source.CoverageLimited {
			caption += " Input coverage is limited."
		}
		return caption
	}
	name := source.AppName
	if name == "" {
		name = displayAppName(source.AppID)
	}
	caption := strings.Trim(strings.Join([]string{name, source.Domain}, ", "), ", ")
	if source.Partial {
		caption += ". Only part of the original content was captured."
	}
	return caption
}

func recapEvidenceName(layer assistant.Layer) string {
	switch layer {
	case assistant.LayerHour:
		return "Hour recap"
	case assistant.LayerDay:
		return "Day recap"
	case assistant.LayerWeek:
		return "Week recap"
	case assistant.LayerMonth:
		return "Month recap"
	case assistant.LayerYear:
		return "Year recap"
	}
	return ""
}

func (c *Client) profileRepository() (profile.Repository, bool) {
	if c == nil || c.data == nil {
		return nil, false
	}
	repository, ok := c.data.(profile.Repository)
	return repository, ok && repository.EncryptedAtRest()
}

func (c *Client) ProfilePage(parent context.Context, after string) (profile.Page, error) {
	repository, ok := c.profileRepository()
	if !ok {
		return profile.Page{}, ErrNotConfigured
	}
	ctx, done, err := c.beginOperation(parent, shortOperationTimeout)
	if err != nil {
		return profile.Page{}, err
	}
	defer done()
	return repository.ListProfile(ctx, profile.PageRequest{After: after, Limit: profile.MaxPageSize})
}

func (c *Client) BrowseProfile(parent context.Context, request profile.BrowseRequest) (profile.BrowsePage, error) {
	repository, ok := c.profileRepository()
	if !ok {
		return profile.BrowsePage{}, ErrNotConfigured
	}
	ctx, done, err := c.beginOperation(parent, shortOperationTimeout)
	if err != nil {
		return profile.BrowsePage{}, err
	}
	defer done()
	return profile.Browse(ctx, repository, request)
}

func (c *Client) CreateProfile(parent context.Context, kind profile.Kind, text string, scope profile.Scope) (ProfileResult, error) {
	epoch, stable := c.BeginMemoryRead()
	if !stable {
		return ProfileResult{}, ErrMemoryChanging
	}
	return c.createProfileAtEpoch(parent, kind, text, scope, epoch)
}
func (c *Client) createProfileAtEpoch(parent context.Context, kind profile.Kind, text string, scope profile.Scope, epoch uint64) (ProfileResult, error) {
	id, err := newTaskRequestID()
	if err != nil {
		return ProfileResult{}, err
	}
	at := time.Now().UTC()
	return c.writeProfileAtEpoch(parent, profile.Record{ID: id, Revision: 1, Kind: kind, Text: text, Scope: scope, Enabled: true, UserAuthored: true, CreatedAt: at, UpdatedAt: at}, 0, epoch)
}

func (c *Client) UpdateProfile(parent context.Context, record profile.Record) (ProfileResult, error) {
	epoch, stable := c.BeginMemoryRead()
	if !stable {
		return ProfileResult{}, ErrMemoryChanging
	}
	return c.updateProfileAtEpoch(parent, record, epoch)
}
func (c *Client) updateProfileAtEpoch(parent context.Context, record profile.Record, epoch uint64) (ProfileResult, error) {
	expected := record.Revision
	record.Revision++
	record.UpdatedAt = time.Now().UTC()
	return c.writeProfileAtEpoch(parent, record, expected, epoch)
}

func (c *Client) writeProfileAtEpoch(parent context.Context, record profile.Record, expected, epoch uint64) (ProfileResult, error) {
	repository, ok := c.profileRepository()
	if !ok {
		return ProfileResult{}, ErrNotConfigured
	}
	ctx, done, err := c.beginOperation(parent, shortOperationTimeout)
	if err != nil {
		return ProfileResult{}, err
	}
	defer done()
	if !c.MemoryReadCurrent(epoch) {
		return ProfileResult{}, ErrMemoryChanging
	}
	record.Text = strings.TrimSpace(record.Text)
	record.Scope.Project, record.Scope.Audience = strings.TrimSpace(record.Scope.Project), strings.TrimSpace(record.Scope.Audience)
	if record.Scope.AppID != "" {
		record.Scope.AppID, err = search.CanonicalAppID(strings.TrimSpace(record.Scope.AppID))
		if err != nil {
			return ProfileResult{}, profile.ErrInvalid
		}
	}
	if record.Scope.Domain != "" {
		record.Scope.Domain, err = search.CanonicalDomain(strings.TrimSpace(record.Scope.Domain))
		if err != nil {
			return ProfileResult{}, profile.ErrInvalid
		}
	}
	if record.Validate() != nil {
		return ProfileResult{}, profile.ErrInvalid
	}
	committed, err := c.commitExplicitMemory(epoch, func() error {
		if expected == 0 {
			old, found, err := repository.GetProfile(ctx, record.ID)
			if err != nil {
				return err
			}
			if found {
				if old.Revision != record.Revision || old.Kind != record.Kind || old.Text != record.Text || old.Scope != record.Scope || old.Enabled != record.Enabled || old.UserAuthored != record.UserAuthored || !old.CreatedAt.Equal(record.CreatedAt) || !old.UpdatedAt.Equal(record.UpdatedAt) {
					return profile.ErrConflict
				}
				return nil // same prepared submission retried after a lost response
			}
		}
		return repository.PutProfile(ctx, record, expected)
	})
	if err != nil {
		return ProfileResult{}, err
	}
	return ProfileResult{Record: record, Epoch: committed}, nil
}

func (c *Client) DeleteProfile(parent context.Context, id string, revision uint64) (uint64, error) {
	epoch, stable := c.BeginMemoryRead()
	if !stable {
		return 0, ErrMemoryChanging
	}
	return c.deleteProfileAtEpoch(parent, id, revision, epoch)
}
func (c *Client) deleteProfileAtEpoch(parent context.Context, id string, revision, epoch uint64) (uint64, error) {
	repository, ok := c.profileRepository()
	if !ok {
		return 0, ErrNotConfigured
	}
	ctx, done, err := c.beginOperation(parent, shortOperationTimeout)
	if err != nil {
		return 0, err
	}
	defer done()
	return c.commitExplicitMemory(epoch, func() error { return repository.DeleteProfile(ctx, id, revision) })
}

// commitExplicitMemory serializes a bounded user-authored change against source
// deletion. A queued action from before Delete All must never resurrect memory.
// The returned epoch lets the UI publish the result only while it remains live.
func (c *Client) commitExplicitMemory(epoch uint64, write func() error) (uint64, error) {
	if c == nil || write == nil {
		return 0, ErrInvalidRequest
	}
	c.memoryChangeMu.Lock()
	if c.memoryMutationDepth != 0 || c.memoryMutationEpoch != epoch {
		c.memoryChangeMu.Unlock()
		return 0, ErrMemoryChanging
	}
	if err := write(); err != nil {
		c.memoryChangeMu.Unlock()
		return 0, err
	}
	c.memoryMutationEpoch++
	if c.memoryMutationEpoch == 0 {
		c.memoryMutationEpoch++
	}
	committed := c.memoryMutationEpoch
	cancels := c.askCancellations()
	invalidate := c.memoryInvalidator
	select {
	case c.memoryChanges <- struct{}{}:
	default:
	}
	c.memoryChangeMu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	if invalidate != nil {
		invalidate()
	}
	return committed, nil
}
