// Package today builds bounded, model-free daily recaps from already captured
// local evidence. It performs no I/O and never controls source applications.
package today

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

var ErrInvalid = errors.New("today: invalid snapshot input")

const (
	MaxEvidence      = 2048
	MaxSubjects      = 32
	MaxChanges       = 64
	MaxTodos         = 64
	MaxOpenLoops     = 64
	MaxTimeSplits    = 32
	MaxSources       = 512
	MaxTextBytes     = 512
	MaxSnapshotBytes = 256 << 10
)

type TodoMarker string

const (
	MarkerNone        TodoMarker = ""
	MarkerTODO        TodoMarker = "TODO"
	MarkerUnchecked   TodoMarker = "unchecked_box"
	MarkerUserCreated TodoMarker = "user_created"
)

type Input struct {
	Date, Zone        string
	GeneratedAt       time.Time
	GenerationID      string
	Evidence          []Evidence
	TimeSplit         []TimeSplit
	DuplicateEvidence int
}

// Evidence is supplied by deterministic profile/rule processing. TodoMarker
// must come from an explicit marker or a user-authored item; prose alone is
// never classified as a task.
type Evidence struct {
	SourceID           string
	At                 time.Time
	SubjectID, Subject string
	Change             string
	Todo               string
	TodoMarker         TodoMarker
	TodoConfirmed      bool
	TodoDismissed      bool
	OpenLoop           string
}

type Subject struct {
	ID        string    `json:"id"`
	Label     string    `json:"label"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	SourceIDs []string  `json:"source_ids"`
}

type Change struct {
	At        time.Time `json:"at"`
	Text      string    `json:"text"`
	SourceIDs []string  `json:"source_ids"`
}

type Todo struct {
	ID        string   `json:"id"`
	Text      string   `json:"text"`
	SourceIDs []string `json:"source_ids"`
	Confirmed bool     `json:"confirmed"`
}

type OpenLoop struct {
	Text      string   `json:"text"`
	SourceIDs []string `json:"source_ids"`
}

type TimeSplit struct {
	Key       string        `json:"key"`
	Label     string        `json:"label"`
	Duration  time.Duration `json:"duration_ns"`
	SourceIDs []string      `json:"source_ids"`
}

type Completeness struct {
	EvidenceSeen      int `json:"evidence_seen"`
	EvidenceOmitted   int `json:"evidence_omitted"`
	DuplicateEvidence int `json:"duplicate_evidence"`
	SubjectsOmitted   int `json:"subjects_omitted"`
	ChangesOmitted    int `json:"changes_omitted"`
	TodosOmitted      int `json:"todos_omitted"`
	OpenLoopsOmitted  int `json:"open_loops_omitted"`
	TimeSplitsOmitted int `json:"time_splits_omitted"`
	SourcesOmitted    int `json:"sources_omitted"`
}

// Snapshot is an immutable-by-contract value. Call Clone at mutable API
// boundaries; Build returns independent slices and strings.
type Snapshot struct {
	Date           string       `json:"date"`
	Zone           string       `json:"zone"`
	GeneratedAt    time.Time    `json:"generated_at"`
	GenerationID   string       `json:"generation_id"`
	ActiveSubjects []Subject    `json:"active_subjects"`
	RecentChanges  []Change     `json:"recent_changes"`
	ConfirmedTodos []Todo       `json:"confirmed_todos"`
	TodoCandidates []Todo       `json:"todo_candidates"`
	OpenLoops      []OpenLoop   `json:"open_loops"`
	TimeSplit      []TimeSplit  `json:"time_split"`
	SourceIDs      []string     `json:"source_ids"`
	Completeness   Completeness `json:"completeness"`
}

type subjectAccum struct{ value Subject }

// Materializer retains only bounded current-day evidence. Upsert replaces a
// source's projection when a late capture or user correction arrives.
type Materializer struct {
	mu         sync.Mutex
	date, zone string
	evidence   map[string]Evidence
	duplicates int
}

func NewMaterializer(date, zone string) (*Materializer, error) {
	if !validDate(date) || !validText(zone, 128) {
		return nil, ErrInvalid
	}
	if _, err := time.LoadLocation(zone); err != nil {
		return nil, ErrInvalid
	}
	return &Materializer{date: strings.Clone(date), zone: strings.Clone(zone), evidence: make(map[string]Evidence)}, nil
}

func (m *Materializer) Upsert(e Evidence) error {
	if m == nil || !validEvidence(e) {
		return ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if old, ok := m.evidence[e.SourceID]; ok {
		if old == e {
			m.duplicates++
		}
	} else if len(m.evidence) >= MaxEvidence {
		return ErrInvalid
	}
	m.evidence[e.SourceID] = cloneEvidence(e)
	return nil
}

func (m *Materializer) DeleteSource(sourceID string) error {
	if m == nil || !validID(sourceID) {
		return ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.evidence, sourceID)
	return nil
}

func (m *Materializer) Snapshot(generatedAt time.Time, generationID string, split []TimeSplit) (Snapshot, error) {
	if m == nil {
		return Snapshot{}, ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	items := make([]Evidence, 0, len(m.evidence))
	for _, e := range m.evidence {
		items = append(items, cloneEvidence(e))
	}
	sort.Slice(items, func(i, j int) bool { return items[i].SourceID < items[j].SourceID })
	return Build(Input{Date: m.date, Zone: m.zone, GeneratedAt: generatedAt, GenerationID: generationID, Evidence: items, TimeSplit: split, DuplicateEvidence: m.duplicates})
}

func Build(in Input) (Snapshot, error) {
	date, err := time.Parse("2006-01-02", in.Date)
	if err != nil || date.Format("2006-01-02") != in.Date || in.GeneratedAt.IsZero() || !validID(in.GenerationID) || !validText(in.Zone, 128) {
		return Snapshot{}, ErrInvalid
	}
	if _, err := time.LoadLocation(in.Zone); err != nil {
		return Snapshot{}, ErrInvalid
	}
	location, _ := time.LoadLocation(in.Zone)
	if in.GeneratedAt.In(location).Format("2006-01-02") != in.Date {
		return Snapshot{}, ErrInvalid
	}
	if len(in.Evidence) > MaxEvidence || len(in.TimeSplit) > MaxTimeSplits*4 {
		return Snapshot{}, ErrInvalid
	}
	out := Snapshot{Date: strings.Clone(in.Date), Zone: strings.Clone(in.Zone), GeneratedAt: in.GeneratedAt, GenerationID: strings.Clone(in.GenerationID)}
	if in.DuplicateEvidence < 0 || in.DuplicateEvidence > MaxEvidence {
		return Snapshot{}, ErrInvalid
	}
	out.Completeness.EvidenceSeen = len(in.Evidence) + in.DuplicateEvidence
	out.Completeness.DuplicateEvidence = in.DuplicateEvidence
	bySubject := make(map[string]*subjectAccum)
	seenSources := make(map[string]struct{})
	evidence := append([]Evidence(nil), in.Evidence...)
	sort.Slice(evidence, func(i, j int) bool {
		a, b := evidence[i], evidence[j]
		if a.SourceID != b.SourceID {
			return a.SourceID < b.SourceID
		}
		if !a.At.Equal(b.At) {
			return a.At.After(b.At)
		}
		if a.SubjectID != b.SubjectID {
			return a.SubjectID < b.SubjectID
		}
		if a.Subject != b.Subject {
			return a.Subject < b.Subject
		}
		if a.Change != b.Change {
			return a.Change < b.Change
		}
		if a.Todo != b.Todo {
			return a.Todo < b.Todo
		}
		if a.OpenLoop != b.OpenLoop {
			return a.OpenLoop < b.OpenLoop
		}
		if a.TodoMarker != b.TodoMarker {
			return a.TodoMarker < b.TodoMarker
		}
		if a.TodoConfirmed != b.TodoConfirmed {
			return !a.TodoConfirmed
		}
		return !a.TodoDismissed && b.TodoDismissed
	})
	defer clear(evidence)
	for _, ev := range evidence {
		if !validEvidence(ev) {
			return Snapshot{}, ErrInvalid
		}
		if _, exists := seenSources[ev.SourceID]; exists {
			out.Completeness.DuplicateEvidence++
			continue
		}
		if ev.At.After(in.GeneratedAt) || ev.At.In(location).Format("2006-01-02") != in.Date {
			continue
		}
		seenSources[ev.SourceID] = struct{}{}
		out.SourceIDs = append(out.SourceIDs, strings.Clone(ev.SourceID))
		if ev.SubjectID != "" {
			a := bySubject[ev.SubjectID]
			if a == nil {
				a = &subjectAccum{value: Subject{ID: strings.Clone(ev.SubjectID), Label: strings.Clone(ev.Subject), FirstSeen: ev.At, LastSeen: ev.At}}
				bySubject[ev.SubjectID] = a
			}
			if ev.At.Before(a.value.FirstSeen) {
				a.value.FirstSeen = ev.At
			}
			if ev.At.After(a.value.LastSeen) {
				a.value.LastSeen = ev.At
			}
			if a.value.Label == "" || ev.Subject != "" && ev.Subject < a.value.Label {
				a.value.Label = strings.Clone(ev.Subject)
			}
			a.value.SourceIDs = append(a.value.SourceIDs, strings.Clone(ev.SourceID))
		}
		if ev.Change != "" {
			out.RecentChanges = append(out.RecentChanges, Change{At: ev.At, Text: strings.Clone(ev.Change), SourceIDs: []string{strings.Clone(ev.SourceID)}})
		}
		if ev.Todo != "" && ev.TodoMarker != MarkerNone && !ev.TodoDismissed {
			item := Todo{ID: strings.Clone(ev.SourceID), Text: strings.Clone(ev.Todo), SourceIDs: []string{strings.Clone(ev.SourceID)}, Confirmed: ev.TodoConfirmed}
			if ev.TodoConfirmed {
				out.ConfirmedTodos = append(out.ConfirmedTodos, item)
			} else {
				out.TodoCandidates = append(out.TodoCandidates, item)
			}
		}
		if ev.OpenLoop != "" {
			out.OpenLoops = append(out.OpenLoops, OpenLoop{Text: strings.Clone(ev.OpenLoop), SourceIDs: []string{strings.Clone(ev.SourceID)}})
		}
	}
	out.Completeness.EvidenceOmitted = len(in.Evidence) - len(seenSources)
	for _, a := range bySubject {
		sort.Strings(a.value.SourceIDs)
		out.ActiveSubjects = append(out.ActiveSubjects, a.value)
	}
	sort.Slice(out.ActiveSubjects, func(i, j int) bool {
		if out.ActiveSubjects[i].LastSeen.Equal(out.ActiveSubjects[j].LastSeen) {
			return out.ActiveSubjects[i].ID < out.ActiveSubjects[j].ID
		}
		return out.ActiveSubjects[i].LastSeen.After(out.ActiveSubjects[j].LastSeen)
	})
	if len(out.ActiveSubjects) > MaxSubjects {
		out.Completeness.SubjectsOmitted = len(out.ActiveSubjects) - MaxSubjects
		out.ActiveSubjects = out.ActiveSubjects[:MaxSubjects]
	}
	sort.Slice(out.RecentChanges, func(i, j int) bool {
		if out.RecentChanges[i].At.Equal(out.RecentChanges[j].At) {
			if out.RecentChanges[i].Text == out.RecentChanges[j].Text {
				return out.RecentChanges[i].SourceIDs[0] < out.RecentChanges[j].SourceIDs[0]
			}
			return out.RecentChanges[i].Text < out.RecentChanges[j].Text
		}
		return out.RecentChanges[i].At.After(out.RecentChanges[j].At)
	})
	capSlice(&out.RecentChanges, MaxChanges, &out.Completeness.ChangesOmitted)
	sortTodos(out.ConfirmedTodos)
	sortTodos(out.TodoCandidates)
	capSlice(&out.ConfirmedTodos, MaxTodos, &out.Completeness.TodosOmitted)
	capSlice(&out.TodoCandidates, MaxTodos-len(out.ConfirmedTodos), &out.Completeness.TodosOmitted)
	sort.Slice(out.OpenLoops, func(i, j int) bool {
		if out.OpenLoops[i].Text == out.OpenLoops[j].Text {
			return out.OpenLoops[i].SourceIDs[0] < out.OpenLoops[j].SourceIDs[0]
		}
		return out.OpenLoops[i].Text < out.OpenLoops[j].Text
	})
	capSlice(&out.OpenLoops, MaxOpenLoops, &out.Completeness.OpenLoopsOmitted)
	out.TimeSplit, err = cloneTimeSplits(in.TimeSplit)
	if err != nil {
		return Snapshot{}, err
	}
	sort.Slice(out.TimeSplit, func(i, j int) bool {
		if out.TimeSplit[i].Duration == out.TimeSplit[j].Duration {
			return out.TimeSplit[i].Key < out.TimeSplit[j].Key
		}
		return out.TimeSplit[i].Duration > out.TimeSplit[j].Duration
	})
	if len(out.TimeSplit) > MaxTimeSplits {
		out.Completeness.TimeSplitsOmitted = len(out.TimeSplit) - MaxTimeSplits
		out.TimeSplit = out.TimeSplit[:MaxTimeSplits]
	}
	sort.Strings(out.SourceIDs)
	if len(out.SourceIDs) > MaxSources {
		out.Completeness.SourcesOmitted = len(out.SourceIDs) - MaxSources
		out.SourceIDs = out.SourceIDs[:MaxSources]
	}
	if err := out.Validate(); err != nil {
		return Snapshot{}, err
	}
	return out, nil
}

func (s Snapshot) Validate() error {
	if !validDate(s.Date) || !validText(s.Zone, 128) || s.GeneratedAt.IsZero() || !validID(s.GenerationID) || len(s.ActiveSubjects) > MaxSubjects || len(s.RecentChanges) > MaxChanges || len(s.ConfirmedTodos) > MaxTodos || len(s.TodoCandidates) > MaxTodos || len(s.OpenLoops) > MaxOpenLoops || len(s.TimeSplit) > MaxTimeSplits || len(s.SourceIDs) > MaxSources {
		return ErrInvalid
	}
	location, err := time.LoadLocation(s.Zone)
	if err != nil || s.GeneratedAt.In(location).Format("2006-01-02") != s.Date {
		return ErrInvalid
	}
	c := s.Completeness
	if c.EvidenceSeen < 0 || c.EvidenceSeen > 2*MaxEvidence || c.EvidenceOmitted < 0 || c.DuplicateEvidence < 0 || c.SubjectsOmitted < 0 || c.ChangesOmitted < 0 || c.TodosOmitted < 0 || c.OpenLoopsOmitted < 0 || c.TimeSplitsOmitted < 0 || c.SourcesOmitted < 0 {
		return ErrInvalid
	}
	if !validSortedIDs(s.SourceIDs) {
		return ErrInvalid
	}
	for i, x := range s.ActiveSubjects {
		if !validID(x.ID) || !validOptionalText(x.Label) || x.FirstSeen.IsZero() || x.LastSeen.Before(x.FirstSeen) || !validSortedIDs(x.SourceIDs) {
			return ErrInvalid
		}
		for j := 0; j < i; j++ {
			if s.ActiveSubjects[j].ID == x.ID {
				return ErrInvalid
			}
		}
	}
	for _, x := range s.RecentChanges {
		if x.At.IsZero() || !validText(x.Text, MaxTextBytes) || !validSortedIDs(x.SourceIDs) {
			return ErrInvalid
		}
	}
	for _, set := range [][]Todo{s.ConfirmedTodos, s.TodoCandidates} {
		for _, x := range set {
			if !validID(x.ID) || !validText(x.Text, MaxTextBytes) || !validSortedIDs(x.SourceIDs) {
				return ErrInvalid
			}
		}
	}
	for _, x := range s.ConfirmedTodos {
		if !x.Confirmed {
			return ErrInvalid
		}
	}
	for _, x := range s.TodoCandidates {
		if x.Confirmed {
			return ErrInvalid
		}
	}
	for _, x := range s.OpenLoops {
		if !validText(x.Text, MaxTextBytes) || !validSortedIDs(x.SourceIDs) {
			return ErrInvalid
		}
	}
	for _, x := range s.TimeSplit {
		if !validID(x.Key) || !validText(x.Label, MaxTextBytes) || x.Duration < 0 || !validSortedIDs(x.SourceIDs) {
			return ErrInvalid
		}
	}
	b, err := json.Marshal(s)
	if err != nil || len(b) > MaxSnapshotBytes {
		clear(b)
		return ErrInvalid
	}
	clear(b)
	return nil
}

func validSortedIDs(ids []string) bool {
	if len(ids) > MaxSources {
		return false
	}
	for i, id := range ids {
		if !validID(id) || i > 0 && ids[i-1] >= id {
			return false
		}
	}
	return true
}

func (s Snapshot) Clone() Snapshot {
	b, err := json.Marshal(s)
	if err != nil {
		return Snapshot{}
	}
	var out Snapshot
	_ = json.Unmarshal(b, &out)
	clear(b)
	return out
}

func Equal(a, b Snapshot) bool {
	x, e1 := json.Marshal(a)
	y, e2 := json.Marshal(b)
	defer clear(x)
	defer clear(y)
	return e1 == nil && e2 == nil && bytes.Equal(x, y)
}

func validID(s string) bool         { return validText(s, 128) && !strings.ContainsRune(s, 0) }
func validOptionalID(s string) bool { return s == "" || validID(s) }
func validText(s string, limit int) bool {
	return s != "" && len(s) <= limit && utf8.ValidString(s) && strings.TrimSpace(s) == s && !strings.ContainsRune(s, 0)
}
func validOptionalText(s string) bool { return s == "" || validText(s, MaxTextBytes) }
func validMarker(m TodoMarker) bool {
	return m == MarkerNone || m == MarkerTODO || m == MarkerUnchecked || m == MarkerUserCreated
}
func validEvidence(e Evidence) bool {
	return validID(e.SourceID) && !e.At.IsZero() && validOptionalID(e.SubjectID) && validOptionalText(e.Subject) && validOptionalText(e.Change) && validOptionalText(e.Todo) && validOptionalText(e.OpenLoop) && validMarker(e.TodoMarker) && !(e.TodoConfirmed && e.TodoDismissed)
}
func cloneEvidence(e Evidence) Evidence {
	e.SourceID = strings.Clone(e.SourceID)
	e.SubjectID = strings.Clone(e.SubjectID)
	e.Subject = strings.Clone(e.Subject)
	e.Change = strings.Clone(e.Change)
	e.Todo = strings.Clone(e.Todo)
	e.OpenLoop = strings.Clone(e.OpenLoop)
	return e
}
func validDate(date string) bool {
	t, err := time.Parse("2006-01-02", date)
	return err == nil && t.Format("2006-01-02") == date
}
func sortTodos(v []Todo) {
	sort.Slice(v, func(i, j int) bool {
		if v[i].Text == v[j].Text {
			return v[i].ID < v[j].ID
		}
		return v[i].Text < v[j].Text
	})
}
func cloneTimeSplits(v []TimeSplit) ([]TimeSplit, error) {
	out := make([]TimeSplit, 0, len(v))
	for _, x := range v {
		if !validID(x.Key) || !validText(x.Label, MaxTextBytes) || x.Duration < 0 || len(x.SourceIDs) > MaxSources {
			return nil, ErrInvalid
		}
		y := x
		y.Key = strings.Clone(x.Key)
		y.Label = strings.Clone(x.Label)
		y.SourceIDs = append([]string(nil), x.SourceIDs...)
		for i := range y.SourceIDs {
			if !validID(y.SourceIDs[i]) {
				return nil, ErrInvalid
			}
			y.SourceIDs[i] = strings.Clone(y.SourceIDs[i])
		}
		sort.Strings(y.SourceIDs)
		out = append(out, y)
	}
	return out, nil
}
func capSlice[T any](v *[]T, max int, omitted *int) {
	if len(*v) > max {
		*omitted += len(*v) - max
		*v = (*v)[:max]
	}
}
