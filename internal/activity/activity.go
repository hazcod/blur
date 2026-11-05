// Package activity provides event-driven sessionization and deterministic
// time accounting. It never polls the desktop or controls another app.
package activity

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

var ErrInvalid = errors.New("activity: invalid event or record")

type EventKind uint8

const (
	Seen EventKind = iota + 1
	Idle
	Locked
	Sleeping
	Resumed
)

// Monotonic is elapsed nanoseconds from one process-local monotonic epoch.
// Wall is metadata only and is never used to calculate Duration.
type Event struct {
	Kind                    EventKind
	Monotonic               int64
	Wall                    time.Time
	SourceID, AppID, Domain string
}

type Config struct {
	MaxGap   time.Duration
	RingSize int
}

const (
	maxSegmentDuration = int64(24 * time.Hour)
	maxEventAccounting = int64(7 * 24 * time.Hour)
)

type Segment struct {
	ID          string    `json:"id"`
	WallStart   time.Time `json:"wall_start"`
	WallEnd     time.Time `json:"wall_end"`
	Duration    int64     `json:"duration_ns"`
	AppID       string    `json:"app_id,omitempty"`
	Domain      string    `json:"domain,omitempty"`
	Project     string    `json:"project,omitempty"`
	Topic       string    `json:"topic,omitempty"`
	SourceIDs   []string  `json:"source_ids,omitempty"`
	RuleID      string    `json:"rule_id,omitempty"`
	RuleVersion uint32    `json:"rule_version,omitempty"`
}

type Rule struct {
	ID             string
	Version        uint32
	Priority       int
	AppID, Domain  string
	Project, Topic string
}

type Engine struct {
	mu            sync.Mutex
	cfg           Config
	started       bool
	active        bool
	lastMono      int64
	lastWall      time.Time
	appID, domain string
	sources       []string
	seq           uint64
	omitted       int64
	ring          []Segment
	ringNext      int
}

func New(cfg Config) *Engine {
	if cfg.MaxGap <= 0 {
		cfg.MaxGap = 5 * time.Minute
	}
	if cfg.MaxGap > 24*time.Hour {
		cfg.MaxGap = 24 * time.Hour
	}
	if cfg.RingSize <= 0 {
		cfg.RingSize = 256
	}
	if cfg.RingSize > 4096 {
		cfg.RingSize = 4096
	}
	return &Engine{cfg: cfg, ring: make([]Segment, 0, cfg.RingSize)}
}

// Apply advances the session only when a capture or OS-state event arrives.
// It returns immutable segments ready for durable persistence.
func (e *Engine) Apply(event Event) ([]Segment, error) {
	if e == nil || event.Kind < Seen || event.Kind > Resumed || event.Monotonic < 0 || event.Wall.IsZero() {
		return nil, ErrInvalid
	}
	if event.Kind == Seen && (!validText(event.SourceID, 128) || !validText(event.AppID, 128) || len(event.Domain) > 253) {
		return nil, ErrInvalid
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.started && event.Monotonic < e.lastMono {
		return nil, ErrInvalid
	}
	var emitted []Segment
	if e.started && e.active && event.Monotonic > e.lastMono {
		delta := event.Monotonic - e.lastMono
		accounted := delta
		if accounted > maxEventAccounting {
			e.omitted += accounted - maxEventAccounting
			accounted = maxEventAccounting
		}
		known := int64(e.cfg.MaxGap)
		if accounted < known {
			known = accounted
		}
		wallCursor := e.lastWall
		attributed, wallCursor := e.makeSegments(wallCursor, known, e.appID, e.domain, e.sources)
		emitted = append(emitted, attributed...)
		unknown, _ := e.makeSegments(wallCursor, accounted-known, "", "", nil)
		emitted = append(emitted, unknown...)
	}
	e.started, e.lastMono, e.lastWall = true, event.Monotonic, event.Wall
	switch event.Kind {
	case Seen:
		if e.active && (e.appID != event.AppID || e.domain != event.Domain) {
			e.sources = nil
		}
		e.active, e.appID, e.domain = true, strings.Clone(event.AppID), strings.Clone(event.Domain)
		if !contains(e.sources, event.SourceID) {
			e.sources = append(e.sources, strings.Clone(event.SourceID))
			if len(e.sources) > 32 {
				e.sources = append([]string(nil), e.sources[len(e.sources)-32:]...)
			}
		}
	case Idle, Locked, Sleeping:
		e.active = false
		e.appID = ""
		e.domain = ""
		e.sources = nil
	case Resumed:
		e.active = false
		e.appID = ""
		e.domain = ""
		e.sources = nil
	}
	for _, segment := range emitted {
		e.remember(segment)
	}
	return cloneSegments(emitted), nil
}

// OmittedDuration reports elapsed time beyond the bounded recovery window for
// which session state is too old to classify reliably.
func (e *Engine) OmittedDuration() time.Duration {
	e.mu.Lock()
	defer e.mu.Unlock()
	return time.Duration(e.omitted)
}

func (e *Engine) makeSegments(start time.Time, duration int64, app, domain string, sources []string) ([]Segment, time.Time) {
	var out []Segment
	for duration > 0 {
		chunk := duration
		if chunk > maxSegmentDuration {
			chunk = maxSegmentDuration
		}
		segment := e.makeSegment(start, chunk, app, domain, sources, "", 0)
		out = append(out, segment)
		start = segment.WallEnd
		duration -= chunk
	}
	return out, start
}

func (e *Engine) makeSegment(start time.Time, duration int64, app, domain string, sources []string, rule string, version uint32) Segment {
	e.seq++
	end := start.Add(time.Duration(duration))
	seed := fmt.Sprintf("%d:%d:%s:%s", e.lastMono, e.seq, app, domain)
	h := sha256.Sum256([]byte(seed))
	return Segment{ID: hex.EncodeToString(h[:16]), WallStart: start, WallEnd: end, Duration: duration, AppID: strings.Clone(app), Domain: strings.Clone(domain), SourceIDs: append([]string(nil), sources...), RuleID: rule, RuleVersion: version}
}

func (e *Engine) remember(s Segment) {
	if len(e.ring) < e.cfg.RingSize {
		e.ring = append(e.ring, cloneSegment(s))
		return
	}
	e.ring[e.ringNext] = cloneSegment(s)
	e.ringNext = (e.ringNext + 1) % len(e.ring)
}

func (e *Engine) Segments() []Segment {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Segment, 0, len(e.ring))
	if len(e.ring) < e.cfg.RingSize || e.ringNext == 0 {
		return cloneSegments(e.ring)
	}
	out = append(out, e.ring[e.ringNext:]...)
	out = append(out, e.ring[:e.ringNext]...)
	return cloneSegments(out)
}

func Classify(segments []Segment, rules []Rule) ([]Segment, error) {
	if len(segments) > 4096 || len(rules) > 1024 {
		return nil, ErrInvalid
	}
	ordered := append([]Rule(nil), rules...)
	for _, r := range ordered {
		if !validText(r.ID, 128) || r.Version == 0 || (r.AppID == "" && r.Domain == "") || len(r.AppID) > 128 || len(r.Domain) > 253 || len(r.Project) > 128 || len(r.Topic) > 128 {
			return nil, ErrInvalid
		}
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Priority != ordered[j].Priority {
			return ordered[i].Priority > ordered[j].Priority
		}
		return ordered[i].ID < ordered[j].ID
	})
	out := cloneSegments(segments)
	for i := range out {
		if err := out[i].Validate(); err != nil {
			return nil, err
		}
		for _, r := range ordered {
			if (r.AppID == "" || r.AppID == out[i].AppID) && (r.Domain == "" || r.Domain == out[i].Domain) {
				out[i].Project = r.Project
				out[i].Topic = r.Topic
				out[i].RuleID = r.ID
				out[i].RuleVersion = r.Version
				break
			}
		}
	}
	return out, nil
}

func (s Segment) Validate() error {
	if !validText(s.ID, 128) || s.Duration <= 0 || s.Duration > maxSegmentDuration || s.WallStart.IsZero() || !s.WallEnd.Equal(s.WallStart.Add(time.Duration(s.Duration))) || len(s.AppID) > 128 || len(s.Domain) > 253 || len(s.Project) > 128 || len(s.Topic) > 128 || len(s.SourceIDs) > 32 || len(s.RuleID) > 128 {
		return ErrInvalid
	}
	for _, id := range s.SourceIDs {
		if !validText(id, 128) {
			return ErrInvalid
		}
	}
	return nil
}

type Bucket struct {
	Key      string
	Duration int64
}
type Day struct {
	Date     string
	Duration int64
}
type ReportResult struct {
	Eligible, Unclassified          int64
	Days                            []Day
	Apps, Domains, Projects, Topics []Bucket
}

// Report splits metadata ranges by local calendar date, including DST days.
// Its exact duration is conserved across the total, day, and dimension buckets.
func Report(segments []Segment, zone *time.Location) (ReportResult, error) {
	var result ReportResult
	if zone == nil || len(segments) > 100000 {
		return result, ErrInvalid
	}
	days, apps, domains, projects, topics := map[string]int64{}, map[string]int64{}, map[string]int64{}, map[string]int64{}, map[string]int64{}
	for _, s := range segments {
		if s.Validate() != nil {
			return ReportResult{}, ErrInvalid
		}
		result.Eligible += s.Duration
		if s.Project == "" && s.Topic == "" {
			result.Unclassified += s.Duration
		}
		apps[s.AppID] += s.Duration
		domains[s.Domain] += s.Duration
		projects[s.Project] += s.Duration
		topics[s.Topic] += s.Duration
		cursor := s.WallStart.In(zone)
		end := s.WallEnd.In(zone)
		for cursor.Before(end) {
			y, m, d := cursor.Date()
			next := time.Date(y, m, d+1, 0, 0, 0, 0, zone)
			stop := end
			if next.Before(stop) {
				stop = next
			}
			ns := int64(stop.Sub(cursor))
			days[fmt.Sprintf("%04d-%02d-%02d", y, m, d)] += ns
			cursor = stop
		}
	}
	result.Days = mapDays(days)
	result.Apps = mapBuckets(apps)
	result.Domains = mapBuckets(domains)
	result.Projects = mapBuckets(projects)
	result.Topics = mapBuckets(topics)
	return result, nil
}

func mapDays(values map[string]int64) []Day {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]Day, 0, len(keys))
	for _, k := range keys {
		out = append(out, Day{k, values[k]})
	}
	return out
}
func mapBuckets(values map[string]int64) []Bucket {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]Bucket, 0, len(keys))
	for _, k := range keys {
		out = append(out, Bucket{k, values[k]})
	}
	return out
}
func cloneSegment(s Segment) Segment { s.SourceIDs = append([]string(nil), s.SourceIDs...); return s }
func cloneSegments(in []Segment) []Segment {
	out := make([]Segment, len(in))
	for i := range in {
		out[i] = cloneSegment(in[i])
	}
	return out
}
func validText(s string, max int) bool {
	if s == "" || len(s) > max {
		return false
	}
	for i := range s {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == ':') {
			return false
		}
	}
	return true
}
func contains(values []string, v string) bool {
	for _, x := range values {
		if x == v {
			return true
		}
	}
	return false
}
