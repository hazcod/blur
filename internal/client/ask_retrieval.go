package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/search"
)

const (
	knownAppsDays      = 31
	knownAppsSegments  = 4000
	knownAppsTTL       = 5 * time.Minute
	planWindowDays     = 30
	planMaxDates       = 31
	planEvidenceItems  = 8
	planRecapChronicle = 26
	planRecapRaw       = 6
	planFallbackRaw    = 6
	planLookupPool     = 40
	capturePreviewRows = 8
	captureLineBytes   = 80
	timeTopApps        = 5
	entityTextBytes    = 1500
)

// appsCache holds the KnownApps result between memory changes.
type appsCache struct {
	apps []assistant.KnownApp
	at   time.Time
	ok   bool
}

func (c *Client) askClock() time.Time {
	if c != nil && c.askNow != nil {
		return c.askNow()
	}
	return time.Now()
}

// SetCaptureStateReader lets the desktop report whether capture is running so
// an empty answer can say capture is paused. pausedSince may be zero.
func (c *Client) SetCaptureStateReader(read func() (running bool, pausedSince time.Time)) {
	if c == nil {
		return
	}
	c.appsMu.Lock()
	c.captureState = read
	c.appsMu.Unlock()
}

func (c *Client) invalidateKnownApps() {
	c.appsMu.Lock()
	c.apps = appsCache{}
	c.appsMu.Unlock()
}

func (c *Client) planActivity() (ActivityData, bool) {
	if c == nil || c.data == nil {
		return nil, false
	}
	reader, ok := c.data.(ActivityData)
	return reader, ok
}

// utcDates returns the UTC date partitions that can hold segments overlapping
// [start, end), including the previous day, capped to the most recent
// planMaxDates.
func utcDates(start, end time.Time) []string {
	var dates []string
	for day := start.UTC().Truncate(24 * time.Hour).Add(-24 * time.Hour); day.Before(end); day = day.Add(24 * time.Hour) {
		dates = append(dates, day.Format("2006-01-02"))
	}
	if len(dates) > planMaxDates {
		dates = dates[len(dates)-planMaxDates:]
	}
	return dates
}

// windowSegments lists valid segments overlapping [start, end).
func (c *Client) windowSegments(ctx context.Context, start, end time.Time, limit int) ([]activity.Segment, error) {
	reader, ok := c.planActivity()
	if !ok {
		return nil, ErrNotConfigured
	}
	segments, err := reader.ListActivitySegmentsForDates(ctx, utcDates(start, end), limit)
	if err != nil {
		return nil, err
	}
	out := make([]activity.Segment, 0, len(segments))
	for _, segment := range segments {
		if segment.Validate() != nil || !segment.WallEnd.After(start) || !segment.WallStart.Before(end) {
			continue
		}
		out = append(out, segment)
	}
	return out, nil
}

// KnownApps lists the apps and domains seen in activity over the last 30
// local days so the planner can recognise them in questions.
func (c *Client) KnownApps(ctx context.Context) ([]assistant.KnownApp, error) {
	if c == nil || c.data == nil || ctx == nil {
		return nil, ErrNotConfigured
	}
	now := c.askClock()
	c.appsMu.Lock()
	if c.apps.ok && now.Sub(c.apps.at) < knownAppsTTL {
		apps := append([]assistant.KnownApp(nil), c.apps.apps...)
		c.appsMu.Unlock()
		return apps, nil
	}
	c.appsMu.Unlock()
	end := now.Add(time.Minute)
	segments, err := c.windowSegments(ctx, now.AddDate(0, 0, -planWindowDays), end, knownAppsSegments)
	if err != nil {
		if segments, err = c.windowSegments(ctx, now.AddDate(0, 0, -7), end, knownAppsSegments); err != nil {
			return nil, err
		}
	}
	apps := knownAppsFromSegments(segments)
	c.appsMu.Lock()
	c.apps = appsCache{apps: append([]assistant.KnownApp(nil), apps...), at: now, ok: true}
	c.appsMu.Unlock()
	return apps, nil
}

func knownAppsFromSegments(segments []activity.Segment) []assistant.KnownApp {
	byID := map[string]assistant.KnownApp{}
	for _, segment := range segments {
		if id := segment.AppID; id != "" {
			if _, seen := byID["a:"+id]; !seen {
				name := displayAppName(id)
				aliases := []string{strings.ToLower(name), strings.ToLower(strings.ReplaceAll(name, " ", ""))}
				if i := strings.LastIndexByte(id, '.'); i >= 0 && i+1 < len(id) {
					aliases = append(aliases, strings.ToLower(id[i+1:]))
				}
				byID["a:"+id] = assistant.KnownApp{ID: id, Name: name, Aliases: uniqueSorted(aliases)}
			}
		}
		if domain := segment.Domain; domain != "" {
			if _, seen := byID["d:"+domain]; !seen {
				host := strings.TrimPrefix(strings.ToLower(domain), "www.")
				aliases := []string{strings.ToLower(domain), host}
				if labels := strings.Split(host, "."); len(labels) >= 2 {
					aliases = append(aliases, labels[len(labels)-2])
				}
				byID["d:"+domain] = assistant.KnownApp{ID: domain, Name: domain, Aliases: uniqueSorted(aliases), Domain: true}
			}
		}
	}
	keys := make([]string, 0, len(byID))
	for key := range byID {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]assistant.KnownApp, 0, len(keys))
	for _, key := range keys {
		out = append(out, byID[key])
	}
	return out
}

func uniqueSorted(values []string) []string {
	set := map[string]struct{}{}
	for _, v := range values {
		if v != "" {
			set[v] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// planWindow resolves the plan's window. bounded is false for an unbounded
// lookup, where start/end are only a recent horizon for segment mapping.
func (c *Client) planWindow(plan assistant.Plan) (start, end time.Time, bounded bool, zone *time.Location) {
	now := c.askClock()
	zone, err := loadZone(localZoneName())
	if err != nil {
		zone = time.UTC
	}
	scope := plan.Scope
	if scope.IsZero() {
		if plan.Kind == assistant.KindLookup || plan.Kind == assistant.KindEntity {
			return now.AddDate(0, 0, -planWindowDays), now.Add(time.Minute), false, zone
		}
		local := now.In(zone)
		start = time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, zone)
		return start, time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, zone), true, zone
	}
	start, end = scope.Start, scope.End
	if start.IsZero() {
		start = now.AddDate(0, 0, -planWindowDays)
	}
	if end.IsZero() {
		end = now
		if !end.After(start) {
			end = start.Add(time.Minute)
		}
	}
	return start, end, true, zone
}

func lowerSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, v := range values {
		set[strings.ToLower(v)] = struct{}{}
	}
	return set
}

// filterSegments keeps segments naming one of the plan's apps or domains. A
// plan with neither keeps every segment.
func filterSegments(segments []activity.Segment, plan assistant.Plan) []activity.Segment {
	if len(plan.AppIDs) == 0 && len(plan.Domains) == 0 {
		return segments
	}
	apps, domains := lowerSet(plan.AppIDs), lowerSet(plan.Domains)
	out := make([]activity.Segment, 0, len(segments))
	for _, segment := range segments {
		_, appMatch := apps[strings.ToLower(segment.AppID)]
		_, domainMatch := domains[strings.ToLower(segment.Domain)]
		if appMatch && segment.AppID != "" || domainMatch && segment.Domain != "" {
			out = append(out, segment)
		}
	}
	return out
}

func newestFirst(segments []activity.Segment) {
	sort.SliceStable(segments, func(i, j int) bool {
		if segments[i].WallStart.Equal(segments[j].WallStart) {
			return segments[i].ID > segments[j].ID
		}
		return segments[i].WallStart.After(segments[j].WallStart)
	})
}

func uniqueSources(segments []activity.Segment) int {
	set := map[string]struct{}{}
	for _, segment := range segments {
		for _, id := range segment.SourceIDs {
			set[id] = struct{}{}
		}
	}
	return len(set)
}

func newEvidence(sourceID, appID, domain, text string) assistant.Evidence {
	ev := assistant.Evidence{SourceID: sourceID, AppID: appID, Domain: domain, Text: text}
	if appID != "" {
		ev.AppName = displayAppName(appID)
	}
	return ev
}

func (c *Client) hydrateSources(ctx context.Context, ids []string, starts map[string]time.Time, limit int) ([]assistant.Evidence, error) {
	out := make([]assistant.Evidence, 0, min(limit, len(ids)))
	used := 0
	for _, id := range ids {
		if len(out) >= limit {
			break
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		observation, found, err := c.data.GetObservation(ctx, id)
		if err != nil {
			return nil, err
		}
		if !found || observation.ContentID() != id {
			continue
		}
		bytes := len(observation.Text()) + len(observation.AppID()) + len(observation.Domain())
		if bytes > MaxSearchBytes-used {
			break
		}
		used += bytes
		ev := newEvidence(id, observation.AppID(), observation.Domain(), observation.Text())
		if at, ok := starts[id]; ok {
			ev.CapturedAt = at
		}
		out = append(out, ev)
	}
	return out, nil
}

// recentSourceIDs returns source ids of the newest segments first, newest
// source of a segment first, with each id's segment start.
func recentSourceIDs(segments []activity.Segment, limit int) ([]string, map[string]time.Time) {
	ordered := append([]activity.Segment(nil), segments...)
	newestFirst(ordered)
	starts := map[string]time.Time{}
	var ids []string
	for _, segment := range ordered {
		for i := len(segment.SourceIDs) - 1; i >= 0; i-- {
			id := segment.SourceIDs[i]
			if _, dup := starts[id]; dup {
				continue
			}
			starts[id] = segment.WallStart
			ids = append(ids, id)
			if len(ids) >= limit {
				return ids, starts
			}
		}
	}
	return ids, starts
}

// RetrievePlan gathers evidence for a plan without a model.
func (c *Client) RetrievePlan(ctx context.Context, plan assistant.Plan) (assistant.Retrieval, error) {
	if c == nil || c.data == nil || ctx == nil {
		return assistant.Retrieval{}, ErrNotConfigured
	}
	start, end, bounded, zone := c.planWindow(plan)
	_ = zone
	var retrieval assistant.Retrieval
	if reader := c.captureStateReader(); reader != nil {
		running, since := reader()
		retrieval.Diagnosis.CapturePaused, retrieval.Diagnosis.PausedSince = !running, since
	}
	retrieval.Diagnosis.AppKnown = true
	if len(plan.AppIDs)+len(plan.Domains) > 0 {
		retrieval.Diagnosis.AppKnown = c.planAppsKnown(ctx, plan)
	}
	var windowSegs, scoped []activity.Segment
	if _, ok := c.planActivity(); ok {
		var err error
		if windowSegs, err = c.windowSegments(ctx, start, end, MaxActivityReportSegments); err != nil {
			return assistant.Retrieval{}, err
		}
		scoped = filterSegments(windowSegs, plan)
	}
	retrieval.Diagnosis.SourcesInScope = uniqueSources(scoped)

	var err error
	switch plan.Kind {
	case assistant.KindCapture:
		ids, starts := recentSourceIDs(scoped, planEvidenceItems)
		retrieval.Evidence, err = c.hydrateSources(ctx, ids, starts, planEvidenceItems)
	case assistant.KindRecap:
		retrieval.Evidence, err = c.recapEvidence(ctx, start, end, scoped)
	case assistant.KindLookup:
		retrieval.Evidence, err = c.lookupEvidence(ctx, plan, bounded, windowSegs, scoped)
	case assistant.KindEntity:
		retrieval.Evidence, err = c.entityEvidence(ctx, plan, bounded, windowSegs, scoped)
	}
	if err != nil {
		return assistant.Retrieval{}, err
	}
	return retrieval, nil
}

func (c *Client) captureStateReader() func() (bool, time.Time) {
	c.appsMu.Lock()
	defer c.appsMu.Unlock()
	return c.captureState
}

func (c *Client) planAppsKnown(ctx context.Context, plan assistant.Plan) bool {
	known, err := c.KnownApps(ctx)
	if err != nil {
		return false
	}
	ids := map[string]struct{}{}
	for _, app := range known {
		ids[strings.ToLower(app.ID)] = struct{}{}
	}
	for _, id := range append(append([]string(nil), plan.AppIDs...), plan.Domains...) {
		if _, ok := ids[strings.ToLower(id)]; !ok {
			return false
		}
	}
	return true
}

func syntheticID(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func (c *Client) recapEvidence(ctx context.Context, start, end time.Time, scoped []activity.Segment) ([]assistant.Evidence, error) {
	var out []assistant.Evidence
	hourItem := func(sourceID, text string, from, to time.Time) assistant.Evidence {
		ev := assistant.Evidence{SourceID: sourceID, Text: text}
		ev.Layer = assistant.LayerHour
		ev.CapturedAt = from
		ev.EndAt = to
		return ev
	}
	if c.chronicles != nil {
		rows, _, err := c.chronicles.ListChronicles(ctx, start.UTC(), end.UTC(), "", planRecapChronicle)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if strings.TrimSpace(row.Text) == "" {
				continue
			}
			id := syntheticID("chronicle", row.HourID)
			if len(row.SourceIDs) > 0 {
				id = row.SourceIDs[0]
			}
			out = append(out, hourItem(id, row.Text, row.Start, row.End))
		}
	}
	if now := c.askClock(); !now.Before(start) && now.Before(end) {
		draft, err := c.HourSoFar(ctx, now)
		if err != nil {
			return nil, err
		}
		if len(draft.Lines) > 0 {
			covered := false
			for _, ev := range out {
				if ev.CapturedAt.Equal(draft.Start) {
					covered = true
				}
			}
			if !covered {
				out = append(out, hourItem(syntheticID("draft", draft.Start.UTC().Format(time.RFC3339)), strings.Join(draft.Lines, "\n"), draft.Start, draft.End))
			}
		}
	}
	if len(out) < 2 {
		longest := append([]activity.Segment(nil), scoped...)
		sort.SliceStable(longest, func(i, j int) bool { return longest[i].Duration > longest[j].Duration })
		var ids []string
		starts := map[string]time.Time{}
		seen := map[string]struct{}{}
		for _, segment := range longest {
			if len(segment.SourceIDs) == 0 || len(ids) >= planRecapRaw {
				continue
			}
			id := segment.SourceIDs[len(segment.SourceIDs)-1]
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			ids, starts[id] = append(ids, id), segment.WallStart
		}
		raw, err := c.hydrateSources(ctx, ids, starts, planRecapRaw)
		if err != nil {
			return nil, err
		}
		out = append(out, raw...)
	}
	return out, nil
}

func (c *Client) lookupEvidence(ctx context.Context, plan assistant.Plan, bounded bool, windowSegs, scoped []activity.Segment) ([]assistant.Evidence, error) {
	hasFilter := len(plan.AppIDs)+len(plan.Domains) > 0
	var evidence []assistant.Evidence
	if len(plan.Terms) > 0 {
		page, err := c.planSearch(ctx, plan, bounded, windowSegs)
		if err != nil {
			return nil, err
		}
		hits, err := c.hydrateSearchPage(ctx, page)
		if err != nil {
			return nil, err
		}
		starts := map[string]time.Time{}
		for _, segment := range windowSegs {
			for _, id := range segment.SourceIDs {
				starts[id] = segment.WallStart
			}
		}
		for _, hit := range hits {
			ev := newEvidence(hit.SourceID, hit.AppID, hit.Domain, hit.Text)
			if at, ok := starts[hit.SourceID]; ok {
				ev.CapturedAt = at
			}
			evidence = append(evidence, ev)
		}
	}
	if len(evidence) == 0 && (hasFilter || bounded) {
		ids, starts := recentSourceIDs(scoped, planFallbackRaw)
		return c.hydrateSources(ctx, ids, starts, planFallbackRaw)
	}
	return evidence, nil
}

// planSearch runs exact search for the plan's terms, one query per app id or
// domain filter, merged by score and restricted to sources inside the window.
func (c *Client) planSearch(ctx context.Context, plan assistant.Plan, bounded bool, windowSegs []activity.Segment) (search.ExactPage, error) {
	terms := plan.Terms
	if len(terms) > search.MaxTerms {
		terms = terms[:search.MaxTerms]
	}
	pool := planEvidenceItems
	if bounded {
		pool = planLookupPool
	}
	type filter struct{ app, domain string }
	filters := []filter{{}}
	if len(plan.AppIDs)+len(plan.Domains) > 0 {
		filters = nil
		for _, id := range plan.AppIDs {
			filters = append(filters, filter{app: id})
		}
		for _, d := range plan.Domains {
			filters = append(filters, filter{domain: d})
		}
	}
	maxRows := min(search.MaxIndexRows, pool*16)
	best := map[string]search.IndexHit{}
	var merged search.ExactPage
	for _, f := range filters {
		minMatches := 0
		for pass := 0; pass < 2; pass++ {
			if err := ctx.Err(); err != nil {
				return search.ExactPage{}, err
			}
			page, err := c.data.SearchExact(ctx, search.ExactQuery{Terms: terms, AppID: f.app, Domain: f.domain, MinTermMatches: minMatches, TopK: pool, MaxRows: maxRows, MaxBytes: search.MaxIndexBytes})
			if err != nil {
				return search.ExactPage{}, err
			}
			merged.Rows += page.Rows
			for _, hit := range page.Hits {
				if prev, ok := best[hit.SourceID]; !ok || hit.Score > prev.Score {
					best[hit.SourceID] = hit
				}
			}
			if len(page.Hits) > 0 || len(terms) < 3 || pass == 1 {
				break
			}
			minMatches = max(2, (len(terms)+1)/2)
		}
	}
	var inWindow map[string]struct{}
	if bounded {
		inWindow = map[string]struct{}{}
		for _, segment := range windowSegs {
			for _, id := range segment.SourceIDs {
				inWindow[id] = struct{}{}
			}
		}
	}
	for _, hit := range best {
		if inWindow != nil {
			if _, ok := inWindow[hit.SourceID]; !ok {
				continue
			}
		}
		merged.Hits = append(merged.Hits, hit)
	}
	sort.Slice(merged.Hits, func(i, j int) bool {
		if merged.Hits[i].Score != merged.Hits[j].Score {
			return merged.Hits[i].Score > merged.Hits[j].Score
		}
		return merged.Hits[i].SourceID < merged.Hits[j].SourceID
	})
	if len(merged.Hits) > planEvidenceItems {
		merged.Hits = merged.Hits[:planEvidenceItems]
	}
	for _, hit := range merged.Hits {
		merged.Bytes += hit.Bytes
	}
	return merged, nil
}

type knowledgeSearcher interface {
	SearchKnowledgeExact(context.Context, string) (knowledge.Entity, bool, error)
}

func (c *Client) entityEvidence(ctx context.Context, plan assistant.Plan, bounded bool, windowSegs, scoped []activity.Segment) ([]assistant.Evidence, error) {
	if searcher, ok := c.data.(knowledgeSearcher); ok && strings.TrimSpace(plan.Entity) != "" {
		entity, found, err := searcher.SearchKnowledgeExact(ctx, plan.Entity)
		if err != nil && !errors.Is(err, ErrInvalidRequest) {
			if ctx.Err() != nil {
				return nil, err
			}
			found = false
		}
		if found && entity.State != knowledge.Dismissed {
			var sourceIDs []string
			var b strings.Builder
			b.WriteString(entity.Name)
			add := func(text string, sources []knowledge.Source) {
				if text == "" || b.Len()+len(text) > entityTextBytes {
					return
				}
				b.WriteString("\n- " + text)
				for _, s := range sources {
					if s.ID != "" {
						sourceIDs = append(sourceIDs, s.ID)
					}
				}
			}
			for _, fact := range entity.Facts {
				if fact.State != knowledge.Dismissed {
					add(fact.Text, fact.Sources)
				}
			}
			for _, loop := range entity.OpenLoops {
				if loop.State != knowledge.Dismissed {
					add("Open: "+loop.Text, loop.Sources)
				}
			}
			sourceIDs = append(sourceIDs, entity.ActivitySourceIDs...)
			id := syntheticID("entity", entity.ID)
			if len(sourceIDs) > 0 {
				id = sourceIDs[0]
			}
			digest := assistant.Evidence{SourceID: id, Text: b.String()}
			digest.Layer = assistant.LayerDigest
			out := []assistant.Evidence{digest}
			if len(sourceIDs) > 1 {
				linked, err := c.hydrateSources(ctx, uniqueStrings(sourceIDs[1:]), nil, planFallbackRaw)
				if err != nil {
					return nil, err
				}
				for _, ev := range linked {
					if ev.SourceID != id {
						out = append(out, ev)
					}
				}
			}
			return out, nil
		}
	}
	fallback := plan
	fallback.Kind = assistant.KindLookup
	if len(fallback.Terms) == 0 {
		if terms, err := search.NormalizeTokens(plan.Entity, search.MaxTerms); err == nil {
			fallback.Terms = terms
		}
	}
	if len(fallback.Terms) == 0 {
		return nil, nil
	}
	return c.lookupEvidence(ctx, fallback, bounded, windowSegs, scoped)
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, v := range values {
		if _, ok := seen[v]; !ok {
			seen[v] = struct{}{}
			out = append(out, v)
		}
	}
	return out
}

// AnswerDirect answers time and capture-listing plans without a model.
func (c *Client) AnswerDirect(ctx context.Context, plan assistant.Plan) (assistant.Answer, bool, error) {
	if c == nil || c.data == nil || ctx == nil {
		return assistant.Answer{}, false, ErrNotConfigured
	}
	switch plan.Kind {
	case assistant.KindTime:
	case assistant.KindCapture:
		if len(plan.Terms) > 0 {
			return assistant.Answer{}, false, nil
		}
	default:
		return assistant.Answer{}, false, nil
	}
	if _, ok := c.planActivity(); !ok {
		return assistant.Answer{}, false, nil
	}
	start, end, _, zone := c.planWindow(plan)
	segments, err := c.windowSegments(ctx, start, end, MaxActivityReportSegments)
	if err != nil {
		return assistant.Answer{}, false, err
	}
	scoped := filterSegments(segments, plan)
	label := plan.Scope.Label
	if label == "" {
		label = "today"
	}
	subject := strings.Join(append(append([]string(nil), plan.AppNames...), plan.Domains...), ", ")
	if subject == "" {
		names := make([]string, 0, len(plan.AppIDs))
		for _, id := range plan.AppIDs {
			names = append(names, displayAppName(id))
		}
		subject = strings.Join(names, ", ")
	}
	if plan.Kind == assistant.KindTime {
		return directTime(scoped, start, end, zone, label, subject)
	}
	return c.directCapture(ctx, scoped, zone, label, subject)
}

func directTime(segments []activity.Segment, start, end time.Time, zone *time.Location, label, subject string) (assistant.Answer, bool, error) {
	clipped := make([]activity.Segment, 0, len(segments))
	for _, segment := range segments {
		if segment.WallStart.Before(start) {
			segment.WallStart = start
		}
		if segment.WallEnd.After(end) {
			segment.WallEnd = end
		}
		if segment.Duration = int64(segment.WallEnd.Sub(segment.WallStart)); segment.Duration > 0 {
			clipped = append(clipped, segment)
		}
	}
	if len(clipped) == 0 {
		return assistant.Answer{}, false, nil
	}
	report, err := activity.Report(clipped, zone)
	if err != nil {
		return assistant.Answer{}, false, err
	}
	if subject != "" {
		sessions := "sessions"
		if len(clipped) == 1 {
			sessions = "session"
		}
		return assistant.Answer{Text: subject + ": " + formatReportDuration(report.Eligible) + " " + label + ", " + strconv.Itoa(len(clipped)) + " " + sessions + "."}, true, nil
	}
	var apps []string
	for _, bucket := range report.Apps {
		if bucket.Key != "" && len(apps) < timeTopApps {
			apps = append(apps, displayAppName(bucket.Key)+" "+formatReportDuration(bucket.Duration))
		}
	}
	text := upperFirst(label) + ": " + formatReportDuration(report.Eligible) + "."
	if len(apps) > 0 {
		text += " " + strings.Join(apps, ", ") + "."
	}
	return assistant.Answer{Text: text}, true, nil
}

func (c *Client) directCapture(ctx context.Context, segments []activity.Segment, zone *time.Location, label, subject string) (assistant.Answer, bool, error) {
	if len(segments) == 0 {
		return assistant.Answer{}, false, nil
	}
	ordered := append([]activity.Segment(nil), segments...)
	newestFirst(ordered)
	if len(ordered) > capturePreviewRows {
		ordered = ordered[:capturePreviewRows]
	}
	head := "Captures " + label + ":"
	if subject != "" {
		head = subject + " " + label + ":"
	}
	lines := []string{head}
	for _, segment := range ordered {
		line := segment.WallStart.In(zone).Format("15:04")
		if subject == "" && segment.AppID != "" {
			line += " " + displayAppName(segment.AppID)
		}
		line += " (" + formatReportDuration(segment.Duration) + ")"
		if len(segment.SourceIDs) > 0 {
			observation, found, err := c.data.GetObservation(ctx, segment.SourceIDs[0])
			if err != nil {
				return assistant.Answer{}, false, err
			}
			if found {
				if first := firstLineBytes(observation.Text(), captureLineBytes); first != "" {
					line += " " + first
				}
			}
		}
		lines = append(lines, line)
	}
	return assistant.Answer{Text: strings.Join(lines, "\n")}, true, nil
}

func firstLineBytes(text string, limit int) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(line) <= limit {
			return line
		}
		line = line[:limit]
		for !utf8.ValidString(line) {
			line = line[:len(line)-1]
		}
		return line + "…"
	}
	return ""
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	r, n := utf8.DecodeRuneInString(s)
	return strings.ToUpper(string(r)) + s[n:]
}

var (
	_ assistant.PlanRetriever  = (*Client)(nil)
	_ assistant.DirectAnswerer = (*Client)(nil)
	_ assistant.AppLister      = (*Client)(nil)
)
