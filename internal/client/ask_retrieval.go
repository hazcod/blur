package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/appidentity"
	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/search"
	"github.com/hazcod/blur/internal/semantic"
	"github.com/hazcod/blur/internal/storage"
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
	apps       []assistant.KnownApp
	at         time.Time
	ok         bool
	generation uint64
}

func (c *Client) askClock() time.Time {
	if c != nil && c.askNow != nil {
		return c.askNow()
	}
	return time.Now()
}

func (c *Client) AskTime() time.Time { return c.askClock() }

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
	c.apps = appsCache{generation: c.apps.generation + 1}
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
	generation := c.apps.generation
	if c.apps.ok && now.Sub(c.apps.at) < knownAppsTTL {
		apps := append([]assistant.KnownApp(nil), c.apps.apps...)
		c.appsMu.Unlock()
		return apps, nil
	}
	c.appsMu.Unlock()
	end := now.Add(time.Minute)
	segments, err := c.windowSegments(ctx, now.AddDate(0, 0, -planWindowDays), end, knownAppsSegments)
	if err != nil {
		segments, err = c.windowSegments(ctx, now.AddDate(0, 0, -7), end, knownAppsSegments)
	}
	// An active session may not have produced any activity segments yet.
	// Include a short recent window of primary captures so app recall works
	// immediately; the activity index above already covers older app history.
	if reader, ok := c.data.(ObservationWindowReader); ok {
		page, readErr := readSourceWindow(ctx, reader, storage.ObservationWindowRequest{Start: now.Add(-24 * time.Hour), End: end, ExcludeAppIDs: []string{appidentity.BundleID}, Limit: storage.MaxObservationPageRows, MaxBytes: storage.MaxObservationPageBytes})
		if readErr == nil {
			for _, observation := range page.Sources {
				segments = append(segments, activity.Segment{AppID: observation.AppID(), Domain: observation.Domain()})
			}
			err = nil
		} else if err != nil {
			return nil, readErr
		}
	}
	if err != nil {
		return nil, err
	}
	apps := knownAppsFromSegments(segments)
	c.appsMu.Lock()
	if c.apps.generation == generation {
		c.apps = appsCache{apps: append([]assistant.KnownApp(nil), apps...), at: now, ok: true, generation: generation}
	}
	c.appsMu.Unlock()
	return apps, nil
}

// RememberCapturedApp adds a newly persisted source's app and domain to a
// warm KnownApps cache. Capture notifications are frequent, so invalidating
// the full 31-day discovery query for every new screen would make Ask latency
// grow with the user's history.
func (c *Client) RememberCapturedApp(observation capture.SanitizedObservation) {
	if c == nil || observation.Validate() != nil {
		return
	}
	c.rememberAppIdentity(observation.AppID(), observation.Domain())
}

// RememberCapturedDocument keeps streamed captures on the same fast app
// discovery path as ordinary observations.
func (c *Client) RememberCapturedDocument(summary capture.CaptureSummary) {
	if c == nil || summary.Validate() != nil {
		return
	}
	header := summary.Header()
	c.rememberAppIdentity(header.AppID(), header.Domain())
}

func (c *Client) rememberAppIdentity(appID, domain string) {
	added := knownAppsFromSegments([]activity.Segment{{AppID: appID, Domain: domain}})
	if len(added) == 0 {
		return
	}
	c.appsMu.Lock()
	defer c.appsMu.Unlock()
	if !c.apps.ok {
		return
	}
	byKey := make(map[string]assistant.KnownApp, len(c.apps.apps)+len(added))
	for _, app := range c.apps.apps {
		byKey[knownAppCacheKey(app)] = app
	}
	for _, app := range added {
		byKey[knownAppCacheKey(app)] = app
	}
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	apps := make([]assistant.KnownApp, 0, len(keys))
	for _, key := range keys {
		apps = append(apps, byKey[key])
	}
	c.apps.apps = apps
}

func knownAppCacheKey(app assistant.KnownApp) string {
	kind := "app:"
	if app.Domain {
		kind = "domain:"
	}
	return kind + strings.ToLower(app.ID)
}

func knownAppsFromSegments(segments []activity.Segment) []assistant.KnownApp {
	byID := map[string]assistant.KnownApp{}
	for _, segment := range segments {
		if id := segment.AppID; id != "" {
			key := "a:" + strings.ToLower(id)
			if _, seen := byID[key]; !seen {
				name := displayAppName(id)
				if installed, ok := appDisplayName(id); ok {
					name = installed
				}
				aliases := []string{strings.ToLower(name), strings.ToLower(strings.ReplaceAll(name, " ", ""))}
				// Generic desktop wrappers are often omitted in conversation:
				// "Herdr" names "Herdr Core", just as "Claude" names "Claude Desktop".
				words := strings.Fields(strings.ToLower(name))
				if len(words) > 1 && genericAppSuffix(words[len(words)-1]) {
					aliases = append(aliases, strings.Join(words[:len(words)-1], " "))
				}
				if i := strings.LastIndexByte(id, '.'); i >= 0 && i+1 < len(id) {
					aliases = append(aliases, strings.ToLower(id[i+1:]))
					if genericAppSuffix(strings.ToLower(id[i+1:])) {
						prefix := id[:i]
						if j := strings.LastIndexByte(prefix, '.'); j >= 0 {
							aliases = append(aliases, strings.ToLower(prefix[j+1:]))
						}
					}
				}
				byID[key] = assistant.KnownApp{ID: id, Name: name, Aliases: uniqueSorted(aliases)}
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

func genericAppSuffix(word string) bool {
	switch word {
	case "app", "core", "desktop", "client":
		return true
	}
	return false
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

// filterSegments intersects app and domain qualifiers. A
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
		if (len(apps) == 0 || appMatch && segment.AppID != "") && (len(domains) == 0 || domainMatch && segment.Domain != "") {
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
		observation, found, err := storage.ReadSourceEvidence(ctx, c.data, id)
		if err != nil {
			return nil, err
		}
		if !found || observation.SourceID() != id {
			continue
		}
		bytes := len(observation.Text()) + len(observation.AppID()) + len(observation.Domain())
		if bytes > MaxSearchBytes-used {
			break
		}
		used += bytes
		ev := newEvidence(id, observation.AppID(), observation.Domain(), observation.Text())
		ev.Partial = observation.Truncated()
		if provenance, ok := observation.Provenance(); ok {
			ev.Document = assistant.DocumentProvenance{
				EditionID: provenance.EditionID, FamilyID: provenance.FamilyID, Publication: provenance.Publication,
				PartIndex: provenance.PartIndex, PartCount: provenance.PartCount,
				TotalBytes: provenance.TotalBytes, Start: provenance.Start, End: provenance.End,
				Coverage: uint8(provenance.Coverage),
			}
		}
		if at, ok := starts[id]; ok {
			ev.CapturedAt = at
		} else {
			at, unknown, err := c.sourceCaptureTime(ctx, id, time.Time{})
			if err != nil {
				return nil, err
			}
			ev.CapturedAt, ev.TimeUnknown = at, unknown
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

// RetrievePlan gathers live evidence. Exact matches remain model free; on a
// miss optional recall uses the same selected AI as answers.
func (c *Client) RetrievePlan(ctx context.Context, plan assistant.Plan) (assistant.Retrieval, error) {
	return c.retrievePlan(ctx, plan, nil)
}

// RetrievePlanWithQueries performs Ask's one bounded recovery pass. The search
// phrases came from the selected model's initial relevance-and-rewrite result;
// this method still reranks every new source before returning it as evidence.
func (c *Client) RetrievePlanWithQueries(ctx context.Context, plan assistant.Plan, queries []string) (assistant.Retrieval, error) {
	if len(queries) == 0 || len(queries) > 3 {
		return assistant.Retrieval{}, ErrInvalidRequest
	}
	return c.retrievePlan(ctx, plan, queries)
}

func (c *Client) retrievePlan(ctx context.Context, plan assistant.Plan, expansionQueries []string) (assistant.Retrieval, error) {
	if c == nil || c.data == nil || ctx == nil {
		return assistant.Retrieval{}, ErrNotConfigured
	}
	start, end, bounded, zone := c.planWindow(plan)
	_ = zone
	var retrieval assistant.Retrieval
	pending, coverageErr := c.ExactIndexPending(ctx)
	if coverageErr != nil {
		return retrieval, coverageErr
	}
	retrieval.Diagnosis.IndexingPending = pending
	if c.memoryIndex != nil {
		switch c.MemoryIndexStatus().State {
		case "disabled":
			retrieval.Diagnosis.SemanticIssue = "disabled"
		case "loading", "indexing":
			retrieval.Diagnosis.SemanticIssue = "loading"
		case "ready":
		default:
			retrieval.Diagnosis.SemanticIssue = "unavailable"
		}
	}
	retrieval.Diagnosis.CaptureIssue = captureIssueForHealth(c.CaptureHealth())
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
	var searchPending bool
	var outstandingSources []string
	voiceRequested := asksRecordedContent(plan)
	var topicLibrary []assistant.Evidence
	var supersededTopicSources []string
	var topicLibraryLimited bool
	if plan.Kind == assistant.KindLookup && !voiceRequested && !plan.WholeDocument {
		topicLibrary, topicLibraryLimited, supersededTopicSources, err = c.inferredTopicEvidence(ctx, plan)
		if err != nil {
			return assistant.Retrieval{}, err
		}
	}
	if voiceRequested {
		voice, limited, voiceErr := c.recordedContentEvidence(ctx, plan)
		if voiceErr != nil {
			return assistant.Retrieval{}, voiceErr
		}
		// Browser playback controls and recommendation titles are not the
		// spoken content. A missing transcript must remain an honest gap.
		retrieval.Evidence = voice
		retrieval.Diagnosis.VoiceRequested = true
		retrieval.Diagnosis.VoiceRecallLimited = limited
		retrieval.Diagnosis.VoiceAssociationUnverified = len(voice) > 0 && !asksAboutNamedSession(plan)
	} else {
		switch plan.Kind {
		case assistant.KindCapture:
			retrieval.Evidence, err = c.recentPlanEvidence(ctx, plan, start, end, scoped, planEvidenceItems)
		case assistant.KindRecap:
			retrieval.Evidence, retrieval.Diagnosis.RecapSampled, err = c.recapEvidenceWithDiagnosis(ctx, plan, start, end, scoped, &retrieval.Diagnosis)
		case assistant.KindLookup:
			retrieval.Evidence, searchPending, err = c.lookupEvidenceWithExpansion(ctx, plan, bounded, windowSegs, scoped, &retrieval.Diagnosis, &retrieval.AlternateQueries, expansionQueries)
		case assistant.KindEntity:
			if assistant.IsOutstandingQuestion(plan.Question) {
				retrieval.Evidence, outstandingSources, err = c.outstandingEntityEvidence(ctx, plan)
				if err == nil && len(retrieval.Evidence) == 0 {
					// The Library may not have distilled a project conversation into
					// an open loop yet. Recover bounded source material for review,
					// while keeping credential screens out of this work-oriented path.
					materialPlan := plan
					materialPlan.Kind, materialPlan.Question = assistant.KindLookup, plan.Entity
					var material []assistant.Evidence
					material, _, err = c.lookupEvidenceWithExpansion(ctx, materialPlan, bounded, windowSegs, scoped, &retrieval.Diagnosis, &retrieval.AlternateQueries, expansionQueries)
					for _, item := range material {
						if item.Layer == assistant.LayerSource && !isAuthenticationPageEvidence(item) {
							item.Origin = assistant.OriginRelatedProject
							retrieval.Evidence = append(retrieval.Evidence, item)
							outstandingSources = append(outstandingSources, item.SourceID)
						}
					}
				}
			} else {
				retrieval.Evidence, searchPending, err = c.entityEvidence(ctx, plan, bounded, windowSegs, scoped, &retrieval.Diagnosis)
			}
		}
	}
	if err != nil {
		return assistant.Retrieval{}, err
	}
	if plan.Kind == assistant.KindLookup && !voiceRequested && !plan.WholeDocument {
		if len(supersededTopicSources) > 0 {
			superseded := make(map[string]bool, len(supersededTopicSources))
			for _, sourceID := range supersededTopicSources {
				superseded[sourceID] = true
			}
			filtered := retrieval.Evidence[:0]
			for _, evidence := range retrieval.Evidence {
				if !superseded[evidence.SourceID] {
					filtered = append(filtered, evidence)
				}
			}
			retrieval.Evidence = filtered
		}
		retrieval.Evidence = uniqueEvidence(append(topicLibrary, retrieval.Evidence...), planEvidenceItems)
		retrieval.Diagnosis.LibraryRecallLimited = topicLibraryLimited
	}
	if (plan.Kind == assistant.KindLookup || plan.Kind == assistant.KindEntity) && !voiceRequested && !plan.WholeDocument {
		taskPage, taskErr := c.searchTasks(ctx, plan.Question, taskTopicTerms(plan.Terms), semantic.Query{AppIDs: plan.AppIDs, Domains: plan.Domains, Start: plan.Scope.Start, End: plan.Scope.End}, planEvidenceItems, plan.MatchOnly, outstandingSources...)
		if taskErr != nil {
			return assistant.Retrieval{}, taskErr
		}
		retrieval.Evidence, retrieval.Targets = mergeTaskEvidence(retrieval.Evidence, taskPage, planEvidenceItems)
		retrieval.Diagnosis.TaskSearchLimited = taskPage.hasMore
		if taskPage.semanticIssue != "" {
			retrieval.Diagnosis.SemanticIssue = taskPage.semanticIssue
		}
	}
	retrieval.Diagnosis.IndexingPending = retrieval.Diagnosis.IndexingPending || searchPending
	for _, item := range retrieval.Evidence {
		if item.Layer == assistant.LayerSource {
			retrieval.Diagnosis.SourcesInScope = max(retrieval.Diagnosis.SourcesInScope, len(retrieval.Evidence))
			retrieval.Diagnosis.AppKnown = true
			break
		}
	}
	return retrieval, nil
}

func looksLikeAuthenticationPage(text string) bool {
	text = strings.ToLower(strings.Join(strings.Fields(text), " "))
	action := strings.Contains(text, "sign in") || strings.Contains(text, "log in") || strings.Contains(text, "create an account")
	field := strings.Contains(text, "password") || strings.Contains(text, "remember me") || strings.Contains(text, "verification code") || strings.Contains(text, "one-time code")
	challenge := strings.Contains(text, "recaptcha") || strings.Contains(text, "two-factor") || strings.Contains(text, "one-time code")
	return action && (field || challenge) || field && challenge
}

func isAuthenticationPageEvidence(item assistant.Evidence) bool {
	// Browser identity/domain metadata can be missing on accessibility captures,
	// so assess page text before using it for work or whole-document answers.
	return looksLikeAuthenticationPage(item.Text)
}

func withoutAuthenticationPages(evidence []assistant.Evidence) []assistant.Evidence {
	filtered := evidence[:0]
	for _, item := range evidence {
		if !isAuthenticationPageEvidence(item) {
			filtered = append(filtered, item)
		}
	}
	return filtered
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

func (c *Client) lookupEvidence(ctx context.Context, plan assistant.Plan, bounded bool, windowSegs, scoped []activity.Segment, diagnosis ...*assistant.Diagnosis) ([]assistant.Evidence, bool, error) {
	return c.lookupEvidenceWithExpansion(ctx, plan, bounded, windowSegs, scoped, firstDiagnosis(diagnosis), nil, nil)
}

func firstDiagnosis(diagnosis []*assistant.Diagnosis) *assistant.Diagnosis {
	if len(diagnosis) == 0 {
		return nil
	}
	return diagnosis[0]
}

func (c *Client) lookupEvidenceWithExpansion(ctx context.Context, plan assistant.Plan, bounded bool, windowSegs, scoped []activity.Segment, diagnosis *assistant.Diagnosis, alternateOutput *[]string, alternateQueries []string) ([]assistant.Evidence, bool, error) {
	hasFilter := len(plan.AppIDs)+len(plan.Domains) > 0
	var evidence []assistant.Evidence
	var pending bool
	if len(plan.Terms) > 0 {
		var page search.ExactPage
		var err error
		if len(alternateQueries) > 0 {
			page, err = c.planSearchWithQueries(ctx, plan, bounded, windowSegs, alternateQueries)
		} else {
			page, err = c.planSearch(ctx, plan, bounded, windowSegs)
		}
		if err != nil {
			return nil, pending, err
		}
		pending = page.IndexingPending
		if page.CoverageLimited && diagnosis != nil {
			diagnosis.SemanticIssue = "limited"
		}
		if alternateOutput != nil {
			*alternateOutput = append([]string(nil), page.AlternateQueries...)
		}
		page = boundSearchHydration(page)
		hits, err := c.hydrateSearchPage(ctx, page)
		if err != nil {
			return nil, pending, err
		}
		locateExactPassages(hits, plan.Terms)
		starts := map[string]time.Time{}
		for _, hit := range hits {
			if !hit.CapturedAt.IsZero() {
				starts[hit.SourceID] = hit.CapturedAt
			}
		}
		for _, segment := range windowSegs {
			for _, id := range segment.SourceIDs {
				if segment.WallStart.After(starts[id]) {
					starts[id] = segment.WallStart
				}
			}
		}
		for _, hit := range hits {
			ev := newEvidence(hit.SourceID, hit.AppID, hit.Domain, hit.Text)
			ev.Partial = hit.Partial
			ev.Document = hit.Document
			ev.CapturedAt, ev.TimeUnknown = hit.CapturedAt, hit.TimeUnknown
			ev.MatchStart, ev.MatchEnd = hit.MatchStart, hit.MatchEnd
			if at, ok := starts[hit.SourceID]; ok {
				ev.CapturedAt = at
				ev.TimeUnknown = false
			}
			if plan.WholeDocument && isAuthenticationPageEvidence(ev) {
				continue
			}
			evidence = append(evidence, ev)
		}
	}
	if plan.WholeDocument && len(evidence) == 0 {
		start, end, _, _ := c.planWindow(plan)
		evidence, err := c.recentPlanEvidence(ctx, plan, start, end, scoped, planEvidenceItems)
		evidence = withoutAuthenticationPages(evidence)
		return evidence, pending, err
	}
	if len(evidence) == 0 && !plan.MatchOnly && (hasFilter || bounded) && (len(plan.Terms) == 0 || c.localAI == nil) {
		start, end, _, _ := c.planWindow(plan)
		evidence, err := c.recentPlanEvidence(ctx, plan, start, end, scoped, planFallbackRaw)
		if plan.WholeDocument {
			evidence = withoutAuthenticationPages(evidence)
		}
		return evidence, pending, err
	}
	return evidence, pending, nil
}

// planSearch runs exact search for the plan's terms, one query per app id or
// domain filter, merged by score and restricted to sources inside the window.
func (c *Client) planSearch(ctx context.Context, plan assistant.Plan, bounded bool, windowSegs []activity.Segment) (search.ExactPage, error) {
	pool := planEvidenceItems
	if bounded {
		pool = planLookupPool
	}
	return c.planSearchCandidatesWithExactRanking(ctx, plan, bounded, windowSegs, planEvidenceItems, pool, min(search.MaxIndexRows, pool*16), false)
}

func (c *Client) planSearchWithQueries(ctx context.Context, plan assistant.Plan, bounded bool, windowSegs []activity.Segment, queries []string) (search.ExactPage, error) {
	if len(queries) == 0 || len(queries) > 3 {
		return search.ExactPage{}, ErrInvalidRequest
	}
	start, end, _, _ := c.planWindow(plan)
	var scopeSources []string
	if bounded {
		for _, segment := range windowSegs {
			scopeSources = append(scopeSources, segment.SourceIDs...)
		}
		scopeSources = uniqueSorted(scopeSources)
		if len(scopeSources) > search.MaxScopeSourceIDs {
			return search.ExactPage{}, ErrActivityLimit
		}
	}
	question := plan.Question
	if question == "" {
		question = plan.SearchQuery()
	}
	return c.selectedModelPageUsingQueries(ctx, question, search.ExactPage{}, semantic.Query{
		Limit: planEvidenceItems, AppIDs: plan.AppIDs, Domains: plan.Domains,
		Start: start, End: end, ScopeSourceIDs: scopeSources,
	}, planEvidenceItems, queries, .3)
}

func (c *Client) planSearchCandidates(ctx context.Context, plan assistant.Plan, bounded bool, windowSegs []activity.Segment, limit, pool, maxRows int) (search.ExactPage, error) {
	return c.planSearchCandidatesWithExactRanking(ctx, plan, bounded, windowSegs, limit, pool, maxRows, true)
}

func (c *Client) planSearchCandidatesWithExactRanking(ctx context.Context, plan assistant.Plan, bounded bool, windowSegs []activity.Segment, limit, pool, maxRows int, rankExactQuestions bool) (search.ExactPage, error) {
	if limit < 1 || limit > MaxSearchResults || pool < limit || pool > MaxSearchResults || maxRows < pool || maxRows > search.MaxIndexRows {
		return search.ExactPage{}, ErrInvalidRequest
	}
	terms := plan.Terms
	if len(terms) > search.MaxTerms {
		terms = terms[:search.MaxTerms]
	}

	filters, err := memoryFilters(plan.AppIDs, plan.Domains)
	if err != nil {
		return search.ExactPage{}, err
	}

	var start, end time.Time
	var scopeSources []string
	if bounded {
		start, end, _, _ = c.planWindow(plan)
		var ids []string
		for _, segment := range windowSegs {
			ids = append(ids, segment.SourceIDs...)
		}
		scopeSources = uniqueSorted(ids)
		if len(scopeSources) > search.MaxScopeSourceIDs {
			return search.ExactPage{}, ErrActivityLimit
		}
	}
	best := map[string]search.IndexHit{}
	partialBest := map[string]search.IndexHit{}
	var merged search.ExactPage
	for _, f := range filters {
		minMatches := 0
		for pass := 0; pass < 2; pass++ {
			if err := ctx.Err(); err != nil {
				return search.ExactPage{}, err
			}
			page, err := c.data.SearchExact(ctx, search.ExactQuery{Terms: terms, AppID: f.app, Domain: f.domain, ExcludeAppIDs: []string{appidentity.BundleID}, CapturedStart: start, CapturedEnd: end, ScopeSourceIDs: scopeSources, MinTermMatches: minMatches, PartialOnly: pass == 1, TopK: pool, MaxRows: maxRows, MaxBytes: search.MaxIndexBytes})
			if err != nil {
				return search.ExactPage{}, err
			}
			merged.Rows += page.Rows
			merged.Nodes += page.Nodes
			merged.HasMore = merged.HasMore || page.HasMore
			merged.IndexingPending = merged.IndexingPending || page.IndexingPending
			for _, hit := range page.Hits {
				if pass == 0 {
					if prev, ok := best[hit.SourceID]; !ok || hit.Score > prev.Score {
						best[hit.SourceID] = hit
					}
					delete(partialBest, hit.SourceID)
				} else if _, exact := best[hit.SourceID]; !exact {
					if prev, ok := partialBest[hit.SourceID]; !ok || hit.Score > prev.Score {
						partialBest[hit.SourceID] = hit
					}
				}
			}
			if pass == 1 || plan.Literal || len(terms) < 2 || len(page.Hits) > 0 && len(page.Hits) < min(pool, recallCandidates) {
				break
			}
			// If strict AND results are absent or already fill the relevance
			// ranker's candidate window, collect majority-term matches too. The
			// exact list can otherwise crowd out semantically relevant captures;
			// the selected model relevance-checks every added candidate before it
			// can become Ask evidence.
			minMatches = max(1, (len(terms)+1)/2)
		}
	}
	partial := make([]search.IndexHit, 0, len(partialBest))
	for _, hit := range partialBest {
		partial = append(partial, hit)
	}
	sort.Slice(partial, func(i, j int) bool {
		if partial[i].Score != partial[j].Score {
			return partial[i].Score > partial[j].Score
		}
		return partial[i].SourceID < partial[j].SourceID
	})
	for _, hit := range best {
		merged.Hits = append(merged.Hits, hit)
	}
	sort.Slice(merged.Hits, func(i, j int) bool {
		if merged.Hits[i].Score != merged.Hits[j].Score {
			return merged.Hits[i].Score > merged.Hits[j].Score
		}
		return merged.Hits[i].SourceID < merged.Hits[j].SourceID
	})
	merged.Hits = reservePartialTermCandidates(merged.Hits, partial, pool, limit)
	question := plan.Question
	if question == "" {
		question = plan.SearchQuery()
	}
	minimumScore := float32(.3)
	if plan.MatchOnly {
		minimumScore = .55
	}
	if !plan.Literal {
		hybrid := c.hybridPage
		if !rankExactQuestions {
			hybrid = c.hybridAskPage
		}
		merged, err = hybrid(ctx, question, merged, semantic.Query{Limit: pool, AppIDs: plan.AppIDs, Domains: plan.Domains, Start: start, End: end, ScopeSourceIDs: scopeSources}, limit, minimumScore)
		if err != nil {
			return search.ExactPage{}, err
		}
	}
	if len(merged.Hits) > limit {
		merged.HasMore = true
		merged.Hits = merged.Hits[:limit]
	}
	merged.Bytes = 0
	for _, hit := range merged.Hits {
		merged.Bytes += hit.Bytes
	}
	return merged, nil
}

func reservePartialTermCandidates(exact, partial []search.IndexHit, limit, rankWindow int) []search.IndexHit {
	if limit < 1 || len(partial) == 0 {
		return exact[:min(len(exact), max(0, limit))]
	}
	limit = min(limit, search.MaxResults)
	rankWindow = min(limit, min(rankWindow, recallCandidates))
	if rankWindow < 1 {
		return exact[:min(len(exact), limit)]
	}
	reserve := min(len(partial), min(4, max(1, rankWindow/4)))
	result := make([]search.IndexHit, 0, min(limit, len(exact)+len(partial)))
	seen := make(map[string]bool, cap(result))
	appendHit := func(hit search.IndexHit) {
		if hit.SourceID == "" || seen[hit.SourceID] || len(result) >= limit {
			return
		}
		seen[hit.SourceID] = true
		result = append(result, hit)
	}
	for _, hit := range exact[:min(len(exact), rankWindow-reserve)] {
		appendHit(hit)
	}
	for _, hit := range partial[:reserve] {
		appendHit(hit)
	}
	for _, hit := range exact {
		appendHit(hit)
	}
	for _, hit := range partial[reserve:] {
		appendHit(hit)
	}
	return result
}

type knowledgeSearcher interface {
	SearchKnowledgeExact(context.Context, string) (knowledge.Entity, bool, error)
}

// Outstanding-work questions need explicit open loops and current open tasks.
// A project login page or other recent capture cannot establish what remains
// to do, so this path never falls back to arbitrary captured page text.
func (c *Client) outstandingEntityEvidence(ctx context.Context, plan assistant.Plan) ([]assistant.Evidence, []string, error) {
	if strings.TrimSpace(plan.Entity) == "" {
		return nil, nil, nil
	}
	var confirmedLines, proposedLines []string
	var evidenceSources, openWorkSources []string
	entityName := plan.Entity
	entityID := syntheticID("outstanding-subject", knowledge.NormalizeExact(plan.Entity))
	if searcher, ok := c.data.(knowledgeSearcher); ok {
		entity, found, err := searcher.SearchKnowledgeExact(ctx, plan.Entity)
		if err != nil && !errors.Is(err, ErrInvalidRequest) {
			return nil, nil, err
		}
		if found && entity.State != knowledge.Dismissed {
			entityName, entityID = entity.Name, entity.ID
			for _, source := range entity.ActivitySourceIDs {
				if source != "" {
					evidenceSources = append(evidenceSources, source)
				}
			}
			for _, fact := range entity.Facts {
				if fact.State == knowledge.Dismissed {
					continue
				}
				for _, source := range fact.Sources {
					if source.ID != "" {
						evidenceSources = append(evidenceSources, source.ID)
					}
				}
			}
			for _, loop := range entity.OpenLoops {
				appendOutstandingLoop(&confirmedLines, &proposedLines, &openWorkSources, loop)
			}
			for _, fact := range entity.Facts {
				appendOpenFact(&confirmedLines, &proposedLines, &openWorkSources, fact)
			}
		}
	}
	if repository, ok := c.inferredRepository(); ok {
		entities, err := repository.FindInferredKnowledge(ctx, plan.Entity)
		if err != nil {
			return nil, nil, err
		}
		for _, entity := range entities {
			app, domain := "", ""
			if entity.Inference != nil {
				app, domain = entity.Inference.AppID, entity.Inference.Domain
			}
			if len(plan.AppIDs) > 0 && !slices.Contains(plan.AppIDs, app) || len(plan.Domains) > 0 && !slices.Contains(plan.Domains, domain) {
				continue
			}
			if entityName == plan.Entity {
				entityName = entity.Name
			}
			for _, source := range entity.ActivitySourceIDs {
				if source != "" {
					evidenceSources = append(evidenceSources, source)
				}
			}
			for _, loop := range entity.OpenLoops {
				appendOutstandingLoop(&confirmedLines, &proposedLines, &openWorkSources, loop)
			}
			for _, fact := range entity.Facts {
				appendOpenFact(&confirmedLines, &proposedLines, &openWorkSources, fact)
			}
		}
	}
	evidenceSources = append(evidenceSources, openWorkSources...)
	openWorkSources = uniqueSorted(openWorkSources)
	confirmedLines, proposedLines = uniqueOutstandingLines(confirmedLines, proposedLines)
	evidenceSources = uniqueSorted(evidenceSources)
	if len(confirmedLines)+len(proposedLines) == 0 {
		return nil, evidenceSources, nil
	}
	var evidence []assistant.Evidence
	usedIDs := map[string]bool{}
	addGroup := func(kind string, lines []string, origin assistant.EvidenceOrigin) {
		if len(lines) == 0 {
			return
		}
		id := syntheticID("outstanding", entityID, kind)
		// A grouped Library summary can cite one capture only when that is
		// its sole support. Otherwise the synthetic ID represents the derived
		// group, avoiding a false claim that its first capture supports every
		// open item in the group.
		if len(openWorkSources) == 1 && !usedIDs[openWorkSources[0]] {
			id = openWorkSources[0]
		}
		usedIDs[id] = true
		evidence = append(evidence, assistant.Evidence{
			SourceID: id, AppName: "Library", Text: entityName + "\n- " + strings.Join(lines, "\n- "),
			Layer: assistant.LayerDigest, Origin: origin,
		})
	}
	addGroup("confirmed", confirmedLines, "")
	addGroup("proposed", proposedLines, assistant.OriginInferred)
	return evidence, evidenceSources, nil
}

func appendOutstandingLoop(confirmedLines, proposedLines *[]string, evidenceSources *[]string, loop knowledge.OpenLoop) {
	text := strings.TrimSpace(loop.Text)
	if text == "" || loop.State == knowledge.Dismissed {
		return
	}
	switch loop.State {
	case knowledge.Confirmed, knowledge.Deterministic:
		*confirmedLines = append(*confirmedLines, "Open: "+text)
	case knowledge.Proposed:
		*proposedLines = append(*proposedLines, "Possible open item (inferred, not confirmed): "+text)
	default:
		return
	}
	for _, source := range loop.Sources {
		if source.ID != "" {
			*evidenceSources = append(*evidenceSources, source.ID)
		}
	}
}

func appendOpenFact(confirmedLines, proposedLines *[]string, sourceIDs *[]string, fact knowledge.Fact) {
	text, ok := strings.CutPrefix(strings.TrimSpace(fact.Text), knowledge.PossibleOpenItemPrefix)
	text = strings.TrimSpace(text)
	if !ok || text == "" {
		return
	}
	switch fact.State {
	case knowledge.Confirmed, knowledge.Deterministic:
		*confirmedLines = append(*confirmedLines, "Open: "+text)
	case knowledge.Proposed:
		*proposedLines = append(*proposedLines, knowledge.PossibleOpenItemPrefix+text)
	default:
		return
	}
	for _, source := range fact.Sources {
		if source.ID != "" {
			*sourceIDs = append(*sourceIDs, source.ID)
		}
	}
}

func uniqueOutstandingLines(confirmed, proposed []string) ([]string, []string) {
	key := func(line string) string {
		if i := strings.Index(line, "): "); i >= 0 {
			return knowledge.NormalizeExact(line[i+3:])
		}
		if i := strings.Index(line, ": "); i >= 0 {
			return knowledge.NormalizeExact(line[i+2:])
		}
		return knowledge.NormalizeExact(line)
	}
	seen := make(map[string]struct{}, len(confirmed)+len(proposed))
	unique := func(lines []string) []string {
		out := make([]string, 0, len(lines))
		for _, line := range lines {
			k := key(line)
			if k == "" {
				continue
			}
			if _, exists := seen[k]; exists {
				continue
			}
			seen[k] = struct{}{}
			out = append(out, line)
		}
		return out
	}
	return unique(confirmed), unique(proposed)
}

func (c *Client) entityEvidence(ctx context.Context, plan assistant.Plan, bounded bool, windowSegs, scoped []activity.Segment, diagnosis ...*assistant.Diagnosis) ([]assistant.Evidence, bool, error) {
	var combined []assistant.Evidence
	if searcher, ok := c.data.(knowledgeSearcher); ok && strings.TrimSpace(plan.Entity) != "" {
		entity, found, err := searcher.SearchKnowledgeExact(ctx, plan.Entity)
		if err != nil && !errors.Is(err, ErrInvalidRequest) {
			if ctx.Err() != nil {
				return nil, false, err
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
			// The Library summary is derived from many observations. Give it a
			// stable Library identity so it cannot collapse onto or masquerade as
			// its first supporting capture.
			id := syntheticID("entity", entity.ID)
			digest := assistant.Evidence{SourceID: id, AppName: "Library", Text: b.String()}
			digest.Layer = assistant.LayerDigest
			out := []assistant.Evidence{digest}
			if len(sourceIDs) > 0 {
				linked, err := c.hydrateSources(ctx, uniqueStrings(sourceIDs), nil, planEvidenceItems/2-1)
				if err != nil {
					return nil, false, err
				}
				for _, ev := range linked {
					if ev.SourceID != id {
						out = append(out, ev)
					}
				}
			}
			combined = append(combined, out...)
		}
	}
	if !bounded {
		inferred, err := c.inferredEntityEvidence(ctx, plan)
		if err != nil {
			return nil, false, err
		}
		combined = append(combined, uniqueEvidence(inferred, planEvidenceItems/2)...)
	}
	if len(combined) > 0 {
		return uniqueEvidence(combined, planEvidenceItems), false, nil
	}
	fallback := plan
	fallback.Kind = assistant.KindLookup
	if len(fallback.Terms) == 0 {
		if terms, err := search.NormalizeTokens(plan.Entity, search.MaxTerms); err == nil {
			fallback.Terms = terms
		}
	}
	if len(fallback.Terms) == 0 {
		return nil, false, nil
	}
	return c.lookupEvidence(ctx, fallback, bounded, windowSegs, scoped, diagnosis...)
}

func uniqueEvidence(items []assistant.Evidence, limit int) []assistant.Evidence {
	if limit < 1 {
		return nil
	}
	seen := make(map[string]struct{}, min(len(items), limit))
	result := make([]assistant.Evidence, 0, min(len(items), limit))
	for _, item := range items {
		if item.SourceID == "" {
			continue
		}
		if _, exists := seen[item.SourceID]; exists {
			continue
		}
		seen[item.SourceID] = struct{}{}
		result = append(result, item)
		if len(result) == limit {
			break
		}
	}
	return result
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
func (c *Client) AnswerDirect(ctx context.Context, plan assistant.Plan) (assistant.DirectAnswer, bool, error) {
	if c == nil || c.data == nil || ctx == nil {
		return assistant.DirectAnswer{}, false, ErrNotConfigured
	}
	if answer, handled, err := c.directTasks(ctx, plan); handled || err != nil {
		return answer, handled, err
	}
	if answer, handled, err := c.directProfile(ctx, plan); handled || err != nil {
		return answer, handled, err
	}
	if answer, handled, err := c.directEntitySummary(ctx, plan); handled || err != nil {
		return answer, handled, err
	}
	switch plan.Kind {
	case assistant.KindTime:
	case assistant.KindCapture:
		if len(plan.Terms) > 0 {
			return assistant.DirectAnswer{}, false, nil
		}
	default:
		return assistant.DirectAnswer{}, false, nil
	}
	start, end, _, zone := c.planWindow(plan)
	var segments []activity.Segment
	if _, ok := c.planActivity(); ok {
		var err error
		segments, err = c.windowSegments(ctx, start, end, MaxActivityReportSegments)
		if err != nil {
			return assistant.DirectAnswer{}, false, err
		}
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
		answer, handled, err := directTime(scoped, start, end, zone, label, subject)
		return assistant.DirectAnswer{Answer: answer}, handled, err
	}
	evidence, err := c.recentPlanEvidence(ctx, plan, start, end, scoped, planEvidenceItems)
	if err != nil || len(evidence) == 0 {
		return assistant.DirectAnswer{}, false, err
	}
	return directCaptureEvidence(evidence, scoped, zone, label, subject), true, nil
}

func (c *Client) directEntitySummary(ctx context.Context, plan assistant.Plan) (assistant.DirectAnswer, bool, error) {
	if plan.Kind != assistant.KindEntity || assistant.IsOutstandingQuestion(plan.Question) || !isEntitySummaryQuestion(plan.Question, plan.Entity) {
		return assistant.DirectAnswer{}, false, nil
	}
	// Apple can refuse a simple identity question even with its exact source in
	// context. Keep the normal generated conversation path for other providers.
	if c.localAI != nil && c.localAI.Enabled() && c.localAI.Settings().Provider != ProviderApple {
		return assistant.DirectAnswer{}, false, nil
	}
	terms, err := search.NormalizeTokens(plan.Entity, search.MaxTerms)
	if err != nil || len(terms) == 0 {
		return assistant.DirectAnswer{}, false, nil
	}
	var start, end time.Time
	if !plan.Scope.IsZero() {
		start, end, _, _ = c.planWindow(plan)
	}
	filters, err := memoryFilters(plan.AppIDs, plan.Domains)
	if err != nil {
		return assistant.DirectAnswer{}, false, err
	}
	page := search.ExactPage{}
	seen := make(map[string]bool)
	for _, filter := range filters {
		remaining := MaxSearchBytes - page.Bytes
		if len(page.Hits) >= 3 || remaining <= 0 {
			break
		}
		matched, err := c.data.SearchExact(ctx, search.ExactQuery{
			Terms: terms, AppID: filter.app, Domain: filter.domain, ExcludeAppIDs: []string{appidentity.BundleID},
			CapturedStart: start, CapturedEnd: end, TopK: 3 - len(page.Hits), MaxRows: 48, MaxBytes: remaining, MinTermMatches: len(terms),
		})
		if err != nil {
			return assistant.DirectAnswer{}, false, err
		}
		page.IndexingPending = page.IndexingPending || matched.IndexingPending
		page.HasMore = page.HasMore || matched.HasMore
		for _, hit := range matched.Hits {
			if seen[hit.SourceID] || hit.Bytes > remaining {
				continue
			}
			seen[hit.SourceID] = true
			page.Hits = append(page.Hits, hit)
			page.Bytes += hit.Bytes
		}
	}
	if len(page.Hits) == 0 {
		return assistant.DirectAnswer{}, false, nil
	}
	hydrated, err := c.hydrateSearchPage(ctx, boundSearchHydration(page))
	if err != nil {
		return assistant.DirectAnswer{}, false, err
	}
	var evidence []assistant.Evidence
	for _, hit := range hydrated {
		begin, finish := search.MatchPassage(hit.Text, terms, 768)
		if finish <= begin {
			continue
		}
		item := newEvidence(hit.SourceID, hit.AppID, hit.Domain, hit.Text)
		item.CapturedAt, item.TimeUnknown = hit.CapturedAt, hit.TimeUnknown
		item.Partial = hit.Partial
		item.MatchStart, item.MatchEnd = begin, finish
		evidence = append(evidence, item)
	}
	if len(evidence) == 0 {
		return assistant.DirectAnswer{}, false, nil
	}
	var answer strings.Builder
	if len(evidence) == 1 {
		answer.WriteString("Your saved source says: ")
	} else {
		answer.WriteString("I found these saved mentions of " + plan.Entity + ":")
	}
	for index, item := range evidence {
		if index > 0 {
			answer.WriteString("\n\n")
		}
		if len(evidence) > 1 {
			answer.WriteString("[" + strconv.Itoa(index+1) + "] ")
		}
		answer.WriteString("“" + item.Text[item.MatchStart:item.MatchEnd] + "”")
	}
	ids := make([]string, len(evidence))
	for i, item := range evidence {
		ids[i] = item.SourceID
	}
	return assistant.DirectAnswer{Answer: assistant.Answer{Text: answer.String(), SourceIDs: ids}, Sources: evidence}, true, nil
}

func isEntitySummaryQuestion(question, entity string) bool {
	question = strings.ToLower(strings.TrimSpace(strings.TrimRight(strings.TrimSpace(question), "?.!")))
	entity = strings.ToLower(strings.TrimSpace(entity))
	if question == "" || entity == "" {
		return false
	}
	for _, prefix := range []string{"who is ", "who's ", "who are ", "what is ", "what's ", "what or who is ", "tell me about ", "what do you know about ", "what do you remember about "} {
		if strings.HasPrefix(question, prefix) && strings.TrimSpace(strings.TrimRight(question[len(prefix):], "?.!")) == entity {
			return true
		}
	}
	return false
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

func directCaptureEvidence(evidence []assistant.Evidence, segments []activity.Segment, zone *time.Location, label, subject string) assistant.DirectAnswer {
	head := "Captures " + label + ":"
	if subject != "" {
		head = subject + " " + label + ":"
	}
	lines := []string{head}
	answer := assistant.DirectAnswer{Sources: evidence}
	for _, item := range evidence {
		line := item.CapturedAt.In(zone).Format("15:04")
		if subject == "" && item.AppID != "" {
			line += " " + displayAppName(item.AppID)
		}
		for _, segment := range segments {
			if segment.WallStart.Equal(item.CapturedAt) && slices.Contains(segment.SourceIDs, item.SourceID) {
				line += " (" + formatReportDuration(segment.Duration) + ")"
				break
			}
		}
		if first := firstLineBytes(item.Text, captureLineBytes); first != "" {
			line += " " + first
		}
		lines = append(lines, line)
		answer.SourceIDs = append(answer.SourceIDs, item.SourceID)
	}
	answer.Text = strings.Join(lines, "\n")
	return answer
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
