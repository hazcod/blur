package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/today"
)

// materializeTodayActivity derives a small, deterministic day recap from
// encrypted activity metadata. It never re-reads an app or sends captured text
// to a model. User-authored tasks and questions already in the snapshot survive.
func materializeTodayActivity(ctx context.Context, date, zoneName string, existing today.Snapshot, found bool, reader ActivityData, writer TodaySnapshotWriter) (today.Snapshot, bool, error) {
	if ctx == nil || reader == nil || writer == nil || !validDate(date) {
		return today.Snapshot{}, false, ErrInvalidRequest
	}
	zone, err := loadZone(zoneName)
	if err != nil {
		return today.Snapshot{}, false, ErrInvalidRequest
	}
	parsed, _ := time.Parse("2006-01-02", date)
	start := time.Date(parsed.Year(), parsed.Month(), parsed.Day(), 0, 0, 0, 0, zone)
	end := time.Date(parsed.Year(), parsed.Month(), parsed.Day()+1, 0, 0, 0, 0, zone)
	dates := make([]string, 0, 2)
	for day := start.UTC().Truncate(24 * time.Hour); day.Before(end); day = day.Add(24 * time.Hour) {
		dates = append(dates, day.Format("2006-01-02"))
	}
	if len(dates) == 0 || len(dates) > 3 {
		return today.Snapshot{}, false, ErrInvalidRequest
	}
	segments, err := reader.ListActivitySegmentsForDates(ctx, dates, MaxActivityReportSegments)
	if err != nil {
		return today.Snapshot{}, false, err
	}
	if len(segments) > MaxActivityReportSegments {
		return today.Snapshot{}, false, ErrActivityLimit
	}
	sort.Slice(segments, func(i, j int) bool {
		if segments[i].WallStart.Equal(segments[j].WallStart) {
			return segments[i].ID < segments[j].ID
		}
		return segments[i].WallStart.After(segments[j].WallStart)
	})

	type group struct {
		id, label      string
		duration       time.Duration
		sources        map[string]struct{}
		tooManySources bool
	}
	groups := make(map[string]*group)
	type change struct {
		at, sourceTime time.Time
		text, source   string
		subjectID      string
		subjectLabel   string
	}
	changes := make(map[string]change)
	var latest time.Time
	fingerprint := sha256.New()
	var sourceBuffer = make([]byte, 0, 128)
	separator := []byte{0}
	for i := range segments {
		segment := segments[i]
		if segment.Validate() != nil {
			return today.Snapshot{}, false, ErrInvalidRequest
		}
		if len(segment.SourceIDs) == 0 {
			continue
		}
		if !segment.WallEnd.After(start) || !segment.WallStart.Before(end) {
			continue
		}
		if segment.WallStart.Before(start) {
			segment.WallStart = start
		}
		if segment.WallEnd.After(end) {
			segment.WallEnd = end
		}
		segment.Duration = int64(segment.WallEnd.Sub(segment.WallStart))
		if segment.Duration <= 0 {
			continue
		}
		label, groupValue := activityLabel(segment)
		id := projectionID("subject", groupValue)
		fmt.Fprintf(fingerprint, "%s\x00%s\x00%s\x00%d\x00", segment.ID, id, segment.WallStart.UTC().Format(time.RFC3339Nano), segment.Duration)
		for _, sourceID := range segment.SourceIDs {
			sourceBuffer = append(sourceBuffer[:0], sourceID...)
			_, _ = fingerprint.Write(sourceBuffer)
			_, _ = fingerprint.Write(separator)
		}
		entry := groups[id]
		if entry == nil {
			entry = &group{id: id, label: label, sources: make(map[string]struct{})}
			groups[id] = entry
		}
		entry.duration += time.Duration(segment.Duration)
		if segment.WallEnd.After(latest) {
			latest = segment.WallEnd
		}
		for _, sourceID := range segment.SourceIDs {
			if _, exists := entry.sources[sourceID]; !exists {
				if len(entry.sources) < today.MaxSources {
					entry.sources[sourceID] = struct{}{}
				} else {
					entry.tooManySources = true
				}
			}
			current, seen := changes[sourceID]
			if !seen && len(changes) < today.MaxEvidence {
				changes[sourceID] = change{at: segment.WallEnd.Add(-time.Nanosecond), sourceTime: segment.WallEnd, text: "Active in " + label, source: sourceID, subjectID: id, subjectLabel: label}
			} else if seen && segment.WallEnd.After(current.sourceTime) {
				changes[sourceID] = change{at: segment.WallEnd.Add(-time.Nanosecond), sourceTime: segment.WallEnd, text: "Active in " + label, source: sourceID, subjectID: id, subjectLabel: label}
			}
		}
	}
	if len(groups) == 0 {
		return existing, found, nil
	}
	fingerprintHash := fingerprint.Sum(nil)
	generationID := "activity-" + date + "-" + hex.EncodeToString(fingerprintHash[:16])
	if found && existing.GenerationID == generationID {
		return existing, true, nil
	}
	if latest.IsZero() {
		return existing, found, nil
	}
	generatedAt := latest.Add(-time.Nanosecond)
	if generatedAt.In(zone).Format("2006-01-02") != date {
		generatedAt = end.Add(-time.Nanosecond)
	}
	evidence := make([]today.Evidence, 0, min(len(changes), today.MaxEvidence))
	// Keep evidence and aggregate links fully represented by the snapshot's
	// bounded reverse-deletion index. Build sorts this list before applying its
	// MaxSources cap, so select the same deterministic prefix here.
	changeList := make([]change, 0, len(changes))
	for _, item := range changes {
		changeList = append(changeList, item)
	}
	sort.Slice(changeList, func(i, j int) bool { return changeList[i].source < changeList[j].source })
	if len(changeList) > today.MaxSources {
		changeList = changeList[:today.MaxSources]
	}
	allowed := make(map[string]struct{}, len(changeList))
	for _, item := range changeList {
		allowed[item.source] = struct{}{}
	}
	for _, item := range changeList {
		evidence = append(evidence, today.Evidence{SourceID: item.source, At: item.at, SubjectID: item.subjectID, Subject: item.subjectLabel, Change: item.text, ChangeOrigin: today.ChangeOriginActivity})
	}
	splits := make([]today.TimeSplit, 0, len(groups))
	for id, item := range groups {
		// A truncated provenance set would leave an aggregate duration that
		// cannot be corrected when one of its contributing captures is deleted.
		if item.tooManySources {
			continue
		}
		sourceIDs := sortedBoundedSet(item.sources, today.MaxSources)
		if len(sourceIDs) == 0 || !allAllowedSources(sourceIDs, allowed) {
			continue
		}
		splits = append(splits, today.TimeSplit{Key: "time:" + id, Label: item.label, Duration: item.duration, SourceIDs: sourceIDs})
	}
	sort.Slice(splits, func(i, j int) bool { return splits[i].Key < splits[j].Key })
	derived, err := today.Build(today.Input{Date: date, Zone: snapshotZoneName(zoneName), GeneratedAt: generatedAt, GenerationID: generationID, Evidence: evidence, TimeSplit: splits})
	if err != nil {
		return today.Snapshot{}, false, err
	}
	derived.Completeness.EvidenceOmitted += max(0, len(changes)-len(evidence))
	merged := derived
	if found {
		merged.ConfirmedTodos = append(existing.ConfirmedTodos, derived.ConfirmedTodos...)
		merged.TodoCandidates = append(existing.TodoCandidates, derived.TodoCandidates...)
		merged.OpenLoops = append(existing.OpenLoops, derived.OpenLoops...)
		merged.Questions = append(existing.Questions, derived.Questions...)
		previousActivitySources := make(map[string]struct{})
		for _, item := range existing.RecentChanges {
			if item.Origin == today.ChangeOriginActivity {
				for _, sourceID := range item.SourceIDs {
					previousActivitySources[sourceID] = struct{}{}
				}
			}
		}
		activityChanges := append([]today.Change(nil), derived.RecentChanges...)
		merged.RecentChanges = merged.RecentChanges[:0]
		for _, oldChange := range existing.RecentChanges {
			// Preserve explicit user-authored notes separately. Older untyped
			// entries are ambiguous; activity is regenerated from activity data,
			// so dropping them also removes legacy prompt-as-highlight rows.
			if oldChange.Origin == today.ChangeOriginUser {
				merged.RecentChanges = append(merged.RecentChanges, oldChange)
			}
		}
		merged.RecentChanges = append(merged.RecentChanges, activityChanges...)
		retainedSources := make([]string, 0, len(existing.SourceIDs))
		for _, sourceID := range existing.SourceIDs {
			if _, isPreviousActivity := previousActivitySources[sourceID]; !isPreviousActivity {
				retainedSources = append(retainedSources, sourceID)
			}
		}
		merged.SourceIDs = sortedUnion(retainedSources, derived.SourceIDs, today.MaxSources)
		pruneUnindexed(&merged)
		merged.Completeness.SourcesOmitted += existing.Completeness.SourcesOmitted
		merged.Completeness.TodosOmitted += existing.Completeness.TodosOmitted
		merged.Completeness.ChangesOmitted += existing.Completeness.ChangesOmitted
		merged.Completeness.OpenLoopsOmitted += existing.Completeness.OpenLoopsOmitted
		merged.Completeness.EvidenceSeen += existing.Completeness.EvidenceSeen
		merged.Completeness.EvidenceOmitted += existing.Completeness.EvidenceOmitted
		merged.Completeness.DuplicateEvidence += existing.Completeness.DuplicateEvidence
		merged.Completeness.SourcesOmitted += max(0, len(existing.SourceIDs)+len(derived.SourceIDs)-len(merged.SourceIDs))
		merged.Completeness.EvidenceSeen -= min(merged.Completeness.EvidenceSeen, countIntersection(existing.SourceIDs, previousActivitySources))
		capTodayMerged(&merged)
	}
	if merged.Validate() != nil {
		return today.Snapshot{}, false, ErrInvalidRequest
	}
	if err := writer.PutTodaySnapshot(ctx, merged); err != nil {
		return today.Snapshot{}, false, err
	}
	return merged.Clone(), true, nil
}

func activityLabel(segment activity.Segment) (label, key string) {
	parts := make([]string, 0, 3)
	if segment.Project != "" {
		parts = append(parts, segment.Project)
	}
	if segment.Topic != "" {
		parts = append(parts, segment.Topic)
	}
	if len(parts) == 0 && segment.AppID != "" {
		parts = append(parts, displayAppName(segment.AppID))
	}
	if segment.Domain != "" && (len(parts) == 0 || segment.Project == "" && segment.Topic == "") {
		parts = append(parts, segment.Domain)
	}
	if len(parts) == 0 {
		parts = append(parts, "Unclassified app")
	}
	label = strings.Join(parts, ", ")
	key = strings.Join([]string{segment.AppID, segment.Domain, segment.Project, segment.Topic}, "\x00")
	return label, key
}

func allAllowedSources(ids []string, allowed map[string]struct{}) bool {
	for _, id := range ids {
		if _, ok := allowed[id]; !ok {
			return false
		}
	}
	return true
}

// pruneUnindexed ensures every source-linked item can be found and removed via
// the bounded reverse index. Aggregates are dropped whole when their complete
// provenance does not fit, avoiding false precision after source deletion.
func pruneUnindexed(snapshot *today.Snapshot) {
	allowed := make(map[string]struct{}, len(snapshot.SourceIDs))
	for _, id := range snapshot.SourceIDs {
		allowed[id] = struct{}{}
	}
	subjectCount := len(snapshot.ActiveSubjects)
	subjects := snapshot.ActiveSubjects[:0]
	for _, item := range snapshot.ActiveSubjects {
		if allAllowedSources(item.SourceIDs, allowed) {
			subjects = append(subjects, item)
		} else {
			snapshot.Completeness.SubjectsOmitted++
		}
	}
	clear(snapshot.ActiveSubjects[len(subjects):subjectCount])
	snapshot.ActiveSubjects = subjects
	changeCount := len(snapshot.RecentChanges)
	keptChanges := snapshot.RecentChanges[:0]
	for _, item := range snapshot.RecentChanges {
		if allAllowedSources(item.SourceIDs, allowed) {
			keptChanges = append(keptChanges, item)
		} else {
			snapshot.Completeness.ChangesOmitted++
		}
	}
	clear(snapshot.RecentChanges[len(keptChanges):changeCount])
	snapshot.RecentChanges = keptChanges
	filterTodos := func(items []today.Todo) []today.Todo {
		originalLength := len(items)
		out := items[:0]
		for _, item := range items {
			if allAllowedSources(item.SourceIDs, allowed) {
				out = append(out, item)
			} else {
				snapshot.Completeness.TodosOmitted++
			}
		}
		clear(items[len(out):originalLength])
		return out
	}
	snapshot.ConfirmedTodos = filterTodos(snapshot.ConfirmedTodos)
	snapshot.TodoCandidates = filterTodos(snapshot.TodoCandidates)
	loopCount := len(snapshot.OpenLoops)
	loops := snapshot.OpenLoops[:0]
	for _, item := range snapshot.OpenLoops {
		if allAllowedSources(item.SourceIDs, allowed) {
			loops = append(loops, item)
		} else {
			snapshot.Completeness.OpenLoopsOmitted++
		}
	}
	clear(snapshot.OpenLoops[len(loops):loopCount])
	snapshot.OpenLoops = loops
	questionCount := len(snapshot.Questions)
	questions := snapshot.Questions[:0]
	for _, item := range snapshot.Questions {
		if allAllowedSources(item.SourceIDs, allowed) && (item.AnswerSourceID == "" || hasSource(allowed, item.AnswerSourceID)) {
			questions = append(questions, item)
		}
	}
	clear(snapshot.Questions[len(questions):questionCount])
	snapshot.Questions = questions
	splitCount := len(snapshot.TimeSplit)
	splits := snapshot.TimeSplit[:0]
	for _, item := range snapshot.TimeSplit {
		if allAllowedSources(item.SourceIDs, allowed) {
			splits = append(splits, item)
		} else {
			snapshot.Completeness.TimeSplitsOmitted++
		}
	}
	clear(snapshot.TimeSplit[len(splits):splitCount])
	snapshot.TimeSplit = splits
}

func hasSource(set map[string]struct{}, id string) bool {
	_, ok := set[id]
	return ok
}

func displayAppName(appID string) string {
	switch strings.ToLower(appID) {
	case "com.tinyspeck.slackmacgap", "com.slack":
		return "Slack"
	case "com.apple.safari":
		return "Safari"
	case "com.google.chrome":
		return "Chrome"
	case "com.microsoft.vscode":
		return "Visual Studio Code"
	case "com.apple.finder":
		return "Finder"
	case "com.apple.mail":
		return "Mail"
	case "com.apple.notes":
		return "Notes"
	case "com.apple.terminal":
		return "Terminal"
	case "org.mozilla.firefox":
		return "Firefox"
	}
	if index := strings.LastIndexByte(appID, '.'); index >= 0 && index+1 < len(appID) {
		appID = appID[index+1:]
	}
	return strings.ToUpper(appID[:1]) + appID[1:]
}

func projectionID(namespace, value string) string {
	hash := sha256.Sum256([]byte(namespace + "\x00" + value))
	return namespace + ":" + hex.EncodeToString(hash[:16])
}

func sortedBoundedSet(values map[string]struct{}, limit int) []string {
	out := make([]string, 0, min(len(values), limit))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func sortedUnion(a, b []string, limit int) []string {
	// Bound the capacity hint: the inputs are caller-sized lists, and the result
	// is cut to limit anyway, so a hint above that only wastes memory.
	set := make(map[string]struct{}, min(len(a)+len(b), max(limit, 0)))
	for _, value := range a {
		set[value] = struct{}{}
	}
	for _, value := range b {
		set[value] = struct{}{}
	}
	return sortedBoundedSet(set, limit)
}

func countIntersection(ids []string, set map[string]struct{}) int {
	count := 0
	for _, id := range ids {
		if _, ok := set[id]; ok {
			count++
		}
	}
	return count
}

func capTodayMerged(snapshot *today.Snapshot) {
	sort.Slice(snapshot.RecentChanges, func(i, j int) bool {
		if snapshot.RecentChanges[i].At.Equal(snapshot.RecentChanges[j].At) {
			return snapshot.RecentChanges[i].Text < snapshot.RecentChanges[j].Text
		}
		return snapshot.RecentChanges[i].At.After(snapshot.RecentChanges[j].At)
	})
	if len(snapshot.RecentChanges) > today.MaxChanges {
		snapshot.Completeness.ChangesOmitted += len(snapshot.RecentChanges) - today.MaxChanges
		clear(snapshot.RecentChanges[today.MaxChanges:])
		snapshot.RecentChanges = snapshot.RecentChanges[:today.MaxChanges]
	}
	if len(snapshot.TodoCandidates) > today.MaxTodos {
		snapshot.Completeness.TodosOmitted += len(snapshot.TodoCandidates) - today.MaxTodos
		clear(snapshot.TodoCandidates[today.MaxTodos:])
		snapshot.TodoCandidates = snapshot.TodoCandidates[:today.MaxTodos]
	}
	if len(snapshot.ConfirmedTodos) > today.MaxTodos {
		clear(snapshot.ConfirmedTodos[today.MaxTodos:])
		snapshot.ConfirmedTodos = snapshot.ConfirmedTodos[:today.MaxTodos]
	}
	if len(snapshot.Questions) > today.MaxQuestions {
		clear(snapshot.Questions[today.MaxQuestions:])
		snapshot.Questions = snapshot.Questions[:today.MaxQuestions]
	}
	if len(snapshot.OpenLoops) > today.MaxOpenLoops {
		snapshot.Completeness.OpenLoopsOmitted += len(snapshot.OpenLoops) - today.MaxOpenLoops
		clear(snapshot.OpenLoops[today.MaxOpenLoops:])
		snapshot.OpenLoops = snapshot.OpenLoops[:today.MaxOpenLoops]
	}
}

// fencedTodayWriter persists a snapshot only if no memory change started since
// the read began, holding the mutation fence through the write. A skipped write
// is not an error: the caller still returns its fresh in-memory result.
type fencedTodayWriter struct {
	client *Client
	epoch  uint64
	writer TodaySnapshotWriter
}

func (w fencedTodayWriter) PutTodaySnapshot(ctx context.Context, snapshot today.Snapshot) error {
	var err error
	w.client.PublishMemoryRead(w.epoch, func() { err = w.writer.PutTodaySnapshot(ctx, snapshot) })
	return err
}

const defaultLocaltimePath = "/etc/localtime"

var zoneFallbackPattern = regexp.MustCompile(`^([A-Za-z]{1,6})([+-])(\d{2})(\d{2})$`)

// localZoneName resolves the machine zone to a stable name. time.Local.String()
// is the literal "Local", which is meaningless once stored.
func localZoneName() string {
	return resolveZoneName(os.Getenv("TZ"), defaultLocaltimePath, time.Now())
}

func resolveZoneName(tzEnv, localtimePath string, now time.Time) string {
	if name := strings.TrimPrefix(tzEnv, ":"); name != "" && name != "Local" {
		if _, err := time.LoadLocation(name); err == nil {
			return name
		}
	}
	if target, err := os.Readlink(localtimePath); err == nil {
		const marker = "zoneinfo/"
		if i := strings.LastIndex(target, marker); i >= 0 {
			if name := target[i+len(marker):]; name != "" && name != "Local" {
				if _, err := time.LoadLocation(name); err == nil {
					return name
				}
			}
		}
	}
	abbreviation, offset := now.In(time.Local).Zone()
	sign := '+'
	if offset < 0 {
		sign, offset = '-', -offset
	}
	return fmt.Sprintf("%s%c%02d%02d", abbreviation, sign, offset/3600, offset%3600/60)
}

// loadZone accepts IANA names and the "ABBR+HHMM" fallback from localZoneName.
func loadZone(name string) (*time.Location, error) {
	if name == "" {
		return nil, ErrInvalidRequest
	}
	if zone, err := time.LoadLocation(name); err == nil {
		return zone, nil
	}
	m := zoneFallbackPattern.FindStringSubmatch(name)
	if m == nil {
		return nil, ErrInvalidRequest
	}
	hours, _ := strconv.Atoi(m[3])
	minutes, _ := strconv.Atoi(m[4])
	offset := hours*3600 + minutes*60
	if hours > 14 || minutes > 59 {
		return nil, ErrInvalidRequest
	}
	if m[2] == "-" {
		offset = -offset
	}
	return time.FixedZone(m[1], offset), nil
}

// snapshotZoneName returns a name the today package can validate; fixed-offset
// fallbacks are persisted as UTC because that package only knows IANA names.
func snapshotZoneName(name string) string {
	if _, err := time.LoadLocation(name); err == nil {
		return name
	}
	return "UTC"
}
