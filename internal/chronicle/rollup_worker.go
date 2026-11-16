package chronicle

import (
	"context"
	"errors"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/hazcod/blur/internal/model"
	"github.com/hazcod/blur/internal/power"
)

const (
	// RollupSkippedNoLocalProvider is the status reason reported while rollup
	// generation is not possible because no local background provider runs.
	RollupSkippedNoLocalProvider = "skipped_no_local_provider"

	rollupScanDays          = 35
	rollupScanWeeks         = 10
	maxRollupsGeneratedPass = 6
	rollupFirstDelay        = 30 * time.Second
	rollupInterval          = 15 * time.Minute
	rollupRetryDelay        = 2 * time.Minute
	rollupPageSize          = 64
)

// kickRollups asks the rollup loop to look for work soon. It never blocks.
func (w *Worker) kickRollups() {
	if w == nil {
		return
	}
	select {
	case w.rollupKick <- struct{}{}:
	default:
	}
}

func (w *Worker) rollupLoop() {
	timer := time.NewTimer(rollupFirstDelay)
	defer timer.Stop()
	for {
		select {
		case <-w.ctx.Done():
			return
		case <-w.rollupKick:
		case <-timer.C:
		}
		delay := rollupInterval
		if err := w.ProcessRollups(w.ctx); err != nil && w.ctx.Err() == nil && !errors.Is(err, ErrWorkerDisabled) {
			delay = rollupRetryDelay
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(delay)
	}
}

func (w *Worker) rollupNow() time.Time {
	if w.config.Now != nil {
		return w.config.Now().UTC()
	}
	return time.Now().UTC()
}

func (w *Worker) rollupZone() (*time.Location, string, bool) {
	names := []string{}
	if w.config.Zone != nil {
		names = append(names, w.config.Zone())
	}
	names = append(names, strings.TrimPrefix(os.Getenv("TZ"), ":"), time.Local.String(), "UTC")
	for _, name := range names {
		if name == "" || name == "Local" {
			continue
		}
		if loc, err := time.LoadLocation(name); err == nil {
			return loc, name, true
		}
	}
	return nil, "", false
}

func (w *Worker) setRollupStatus(reason string, pending, dirty int) {
	w.mu.Lock()
	w.telemetry.rollupReason = reason
	w.telemetry.rollupsPending = pending
	w.telemetry.rollupsDirty = dirty
	w.mu.Unlock()
}

// ProcessRollups runs one rollup pass: it marks hours that predate rollups,
// then regenerates every dirty day that can be generated, then every dirty
// ISO week whose days are settled. It uses only the configured local
// background provider and is a no-op, reported as skipped_no_local_provider,
// when there is none.
func (w *Worker) ProcessRollups(ctx context.Context) error {
	if w == nil || ctx == nil || ctx.Err() != nil {
		return ErrWorkerDisabled
	}
	repo, ok := w.repository.(RollupRepository)
	gen, generates := w.generator.(RollupGenerator)
	if !ok || !generates {
		return ErrWorkerDisabled
	}
	w.rollupMu.Lock()
	defer w.rollupMu.Unlock()
	if !w.enabled() {
		w.setRollupStatus(RollupSkippedNoLocalProvider, 0, 0)
		return nil
	}
	loc, zone, ok := w.rollupZone()
	if !ok {
		w.setRollupStatus("zone_unavailable", 0, 0)
		return nil
	}
	now := w.rollupNow()
	if !w.rollupBackfilled {
		if err := w.backfillRollups(ctx, repo, loc, zone, now); err != nil {
			w.recordRollupError(err)
			return err
		}
		w.rollupBackfilled = true
	}
	generated := 0
	passErr := func() error {
		days, err := listAllRollups(ctx, repo, RollupLevelDay, now.AddDate(0, 0, -rollupScanDays), now.AddDate(0, 0, 2))
		if err != nil {
			return err
		}
		for _, day := range days {
			if !day.Dirty || !dayGeneratable(day, now) {
				continue
			}
			if generated >= maxRollupsGeneratedPass {
				break
			}
			did, err := w.refreshDayRollup(ctx, repo, gen, day, loc, zone)
			if err != nil {
				return err
			}
			if did {
				generated++
			}
		}
		weeks, err := listAllRollups(ctx, repo, RollupLevelWeek, now.AddDate(0, 0, -7*rollupScanWeeks), now.AddDate(0, 0, 2))
		if err != nil {
			return err
		}
		for _, week := range weeks {
			if !week.Dirty || generated >= maxRollupsGeneratedPass {
				continue
			}
			did, err := w.refreshWeekRollup(ctx, repo, gen, week, loc, zone, now)
			if err != nil {
				return err
			}
			if did {
				generated++
			}
		}
		return nil
	}()
	pending, dirty := w.countRollups(ctx, repo, now)
	w.setRollupStatus("", pending, dirty)
	if passErr != nil {
		w.recordRollupError(passErr)
	}
	return passErr
}

func (w *Worker) recordRollupError(err error) {
	if err != nil && !errors.Is(err, model.ErrBackgroundPreempted) {
		w.logger.Warn("chronicle rollup failed", "result", "failed", "code", workerErrorCode(err))
	}
	w.recordAttempt(err)
}

// A day is generatable once it has ended in its zone, or while it already has
// generated text that later hours made stale.
func dayGeneratable(day Rollup, now time.Time) bool {
	return !now.Before(day.End) || day.Text != ""
}

func listAllRollups(ctx context.Context, repo RollupRepository, level string, start, end time.Time) ([]Rollup, error) {
	var out []Rollup
	after := ""
	for page := 0; page < 64; page++ {
		rows, next, err := repo.ListChronicleRollups(ctx, level, start, end, after, rollupPageSize)
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
		if next == "" {
			return out, nil
		}
		if len(rows) == 0 || next == after {
			return nil, ErrInvalid
		}
		after = next
	}
	return nil, ErrInvalid
}

func (w *Worker) countRollups(ctx context.Context, repo RollupRepository, now time.Time) (pending, dirty int) {
	days, err := listAllRollups(ctx, repo, RollupLevelDay, now.AddDate(0, 0, -rollupScanDays), now.AddDate(0, 0, 2))
	if err != nil {
		return 0, 0
	}
	weeks, err := listAllRollups(ctx, repo, RollupLevelWeek, now.AddDate(0, 0, -7*rollupScanWeeks), now.AddDate(0, 0, 2))
	if err != nil {
		return 0, 0
	}
	for _, day := range days {
		if day.Dirty {
			dirty++
			if dayGeneratable(day, now) {
				pending++
			}
		}
	}
	for _, week := range weeks {
		if week.Dirty {
			dirty++
			if !now.Before(week.End) {
				pending++
			}
		}
	}
	return pending, dirty
}

// backfillRollups gives hours saved before rollups existed a dirty day
// placeholder, so they are summarized once instead of never.
func (w *Worker) backfillRollups(ctx context.Context, repo RollupRepository, loc *time.Location, zone string, now time.Time) error {
	seen := map[string]bool{}
	from := now.AddDate(0, 0, -rollupScanDays)
	for from.Before(now) {
		to := from.AddDate(0, 0, 31)
		if to.After(now) {
			to = now
		}
		after := ""
		for page := 0; page < 64; page++ {
			rows, next, err := repo.ListChronicles(ctx, from, to, after, rollupPageSize)
			if err != nil {
				return err
			}
			for _, row := range rows {
				key := DayKeyAt(row.Start, loc)
				if seen[key] {
					continue
				}
				seen[key] = true
				id := DayRollupID(key)
				if _, found, err := repo.GetChronicleRollup(ctx, id); err != nil {
					return err
				} else if found {
					continue
				}
				placeholder, err := NewRollupPlaceholder(id, zone)
				if err != nil {
					return err
				}
				if err := repo.PutChronicleRollup(ctx, placeholder); err != nil {
					return err
				}
			}
			if next == "" {
				break
			}
			after = next
		}
		from = to
	}
	return nil
}

// rebase recomputes a rollup's period in the current zone. A rollup created
// under another zone covers a different set of hours and is regenerated.
func rebaseRollup(r Rollup, loc *time.Location, zone string) (Rollup, bool) {
	_, _, start, end, ok := RollupPeriod(r.ID, loc)
	if !ok {
		return r, false
	}
	r.Zone, r.Start, r.End = zone, start, end
	return r, true
}

func (w *Worker) refreshDayRollup(ctx context.Context, repo RollupRepository, gen RollupGenerator, day Rollup, loc *time.Location, zone string) (bool, error) {
	day, ok := rebaseRollup(day, loc, zone)
	if !ok {
		return false, ErrInvalid
	}
	var hours []RollupChild
	after := ""
	for page := 0; page < 8; page++ {
		rows, next, err := repo.ListChronicles(ctx, day.Start, day.End, after, rollupPageSize)
		if err != nil {
			return false, err
		}
		for _, row := range rows {
			if strings.TrimSpace(row.Text) == "" || len(row.SourceIDs) == 0 {
				continue
			}
			label := row.Start.In(loc).Format("15:04") + "-" + row.End.In(loc).Format("15:04")
			hours = append(hours, RollupChild{ID: row.HourID, Label: label, Text: row.Text, SourceIDs: row.SourceIDs})
		}
		if next == "" {
			break
		}
		after = next
	}
	sort.SliceStable(hours, func(i, j int) bool { return hours[i].ID < hours[j].ID })
	if len(hours) == 0 {
		// Nothing left to summarize: keep the period but drop stale text.
		hadText := day.Text != ""
		cleared := clearRollup(day)
		cleared.Dirty = false
		if err := repo.PutChronicleRollup(ctx, cleared); err != nil {
			return false, err
		}
		if hadText {
			return false, w.markWeekDirty(ctx, repo, cleared)
		}
		return false, nil
	}
	input := NewRollupInput(RollupLevelDay, hours)
	label := "Day " + day.PeriodKey + " (" + day.Start.In(loc).Weekday().String() + "), times in " + zone
	return w.storeRollup(ctx, repo, gen, day, input, label)
}

func (w *Worker) refreshWeekRollup(ctx context.Context, repo RollupRepository, gen RollupGenerator, week Rollup, loc *time.Location, zone string, now time.Time) (bool, error) {
	week, ok := rebaseRollup(week, loc, zone)
	if !ok {
		return false, ErrInvalid
	}
	if now.Before(week.End) {
		return false, nil
	}
	days, err := listAllRollups(ctx, repo, RollupLevelDay, week.Start, week.End)
	if err != nil {
		return false, err
	}
	var children []RollupChild
	for _, day := range days {
		if day.Dirty && dayGeneratable(day, now) {
			// A day is still waiting for its own recap; summarize the week after.
			return false, nil
		}
		if day.Text == "" || len(day.SourceIDs) == 0 {
			continue
		}
		d := day.Start.In(loc)
		children = append(children, RollupChild{ID: day.ID, Label: d.Format("Mon 2006-01-02"), Text: day.Text, SourceIDs: day.SourceIDs})
	}
	if len(children) == 0 {
		cleared := clearRollup(week)
		cleared.Dirty = false
		return false, repo.PutChronicleRollup(ctx, cleared)
	}
	input := NewRollupInput(RollupLevelWeek, children)
	label := "Week " + week.PeriodKey + " (Monday " + week.Start.In(loc).Format("2006-01-02") + " to Sunday " + week.End.In(loc).AddDate(0, 0, -1).Format("2006-01-02") + ")"
	return w.storeRollup(ctx, repo, gen, week, input, label)
}

func clearRollup(r Rollup) Rollup {
	r.Text, r.InputHash, r.Model, r.GeneratedAt = "", "", "", time.Time{}
	r.ChildIDs, r.SourceIDs = nil, nil
	return r
}

// storeRollup regenerates a rollup unless its input is unchanged. The bool
// reports whether the model was called.
func (w *Worker) storeRollup(ctx context.Context, repo RollupRepository, gen RollupGenerator, rollup Rollup, input RollupInput, label string) (bool, error) {
	hash := input.Hash()
	if rollup.Text != "" && rollup.InputHash == hash {
		rollup.Dirty = false
		return false, repo.PutChronicleRollup(ctx, rollup)
	}
	prompt, err := BuildRollupPrompt(input, label)
	if err != nil {
		return false, err
	}
	text, modelName, err := w.generateRollupText(model.WithBackgroundInferenceGroup(ctx, "recaps"), gen, prompt)
	if err != nil {
		return false, err
	}
	rollup.Text, rollup.InputHash, rollup.Model = text, hash, modelName
	rollup.ChildIDs, rollup.SourceIDs = input.ChildIDs(), input.SourceIDs()
	rollup.GeneratedAt, rollup.Dirty = w.rollupNow(), false
	if err := ValidateRollup(rollup); err != nil {
		return false, err
	}
	if err := repo.PutChronicleRollup(ctx, rollup); err != nil {
		return false, err
	}
	w.recordAttempt(nil)
	w.logger.Info("chronicle rollup stored", "result", "stored", "level", rollup.Level, "source_count", len(rollup.SourceIDs))
	if rollup.Level == RollupLevelDay {
		if err := w.markWeekDirty(ctx, repo, rollup); err != nil {
			return true, err
		}
	}
	return true, nil
}

// markWeekDirty queues the ISO week containing a regenerated day.
func (w *Worker) markWeekDirty(ctx context.Context, repo RollupRepository, day Rollup) error {
	key, ok := WeekKeyForDate(day.PeriodKey)
	if !ok {
		return ErrInvalid
	}
	id := WeekRollupID(key)
	week, found, err := repo.GetChronicleRollup(ctx, id)
	if err != nil {
		return err
	}
	if !found {
		week, err = NewRollupPlaceholder(id, day.Zone)
		if err != nil {
			return err
		}
	} else if week.Dirty {
		return nil
	}
	week.Dirty = true
	return repo.PutChronicleRollup(ctx, week)
}

// generateRollupText applies the same local-only background policy as hourly
// recaps: policy and power limits, token admission and the shared job slots.
func (w *Worker) generateRollupText(parent context.Context, gen RollupGenerator, prompt RollupPrompt) (string, string, error) {
	started := time.Now()
	effective, err := w.policy.Effective(parent)
	if err != nil && !errors.Is(err, power.ErrSystemStateUnavailable) {
		return "", "", err
	}
	if effective.MaxBackgroundAIJobs < 1 || effective.MaxBackgroundTokens <= MaxRollupOutputTokens {
		return "", "", ErrWorkerPolicy
	}
	budget := effective.MaxBackgroundDuration - time.Since(started)
	if budget <= 0 || budget > power.MaxBackgroundDuration {
		return "", "", ErrWorkerPolicy
	}
	ctx, cancel := context.WithTimeout(parent, budget)
	stop := context.AfterFunc(w.ctx, cancel)
	defer func() { stop(); cancel() }()
	capacity, err := model.RuntimeContextLimit(ctx, w.generator, model.ContextTokenLimit)
	if err != nil {
		return "", "", err
	}
	inputLimit := min(effective.MaxBackgroundTokens-MaxRollupOutputTokens, capacity-MaxRollupOutputTokens-model.PromptTemplateReserve)
	if inputLimit < 1 {
		return "", "", ErrWorkerPolicy
	}
	tokens, err := gen.CountRollupTokens(ctx, prompt)
	if err != nil {
		return "", "", err
	}
	if tokens < 1 || tokens > inputLimit || !effective.AllowBackgroundAI(power.LocalProvider, tokens+MaxRollupOutputTokens, effective.MaxBackgroundDuration) {
		return "", "", ErrWorkerPolicy
	}
	release, err := w.acquire(ctx, effective.MaxBackgroundAIJobs)
	if err != nil {
		return "", "", err
	}
	defer release()
	raw, err := gen.GenerateRollup(ctx, prompt, MaxRollupOutputTokens)
	if err != nil {
		return "", "", err
	}
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	text, err := ValidateRollupText(raw)
	if err != nil {
		return "", "", err
	}
	modelName := ""
	if named, ok := gen.(interface {
		RollupModel(context.Context) (string, error)
	}); ok {
		if name, err := named.RollupModel(ctx); err == nil && len(name) <= MaxRollupModelBytes && !strings.ContainsFunc(name, func(c rune) bool { return c < 0x20 || c == 0x7f }) {
			modelName = name
		}
	}
	return text, modelName, nil
}
