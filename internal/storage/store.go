// Package storage defines the encrypted persistence contract and an explicitly
// unencrypted in-memory conformance backend. The durable implementation lives
// in the separate internal/storage/durable package.
package storage

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/chronicle"
	"github.com/hazcod/blur/internal/privacy"
)

const MaxObservationTextBytes = capture.MaxObservationTextBytes
const MaxListLimit = 1000
const MaxObservationPageRows = 64
const MaxObservationPageBytes = 2 << 20

// ObservationPageRequest bounds both returned rows and the aggregate bytes
// that search will inspect. MaxBytes measures text, app ID and domain bytes.
type ObservationPageRequest struct {
	Limit    int
	MaxBytes int
	After    string
}

// ObservationPage has a stable ContentID-derived order. A backend may keep
// that order opaque (for example, a keyed physical ordering); After always
// accepts the logical ContentID, not a physical cursor. HasMore is true when
// another observation exists after the returned page, including when it did
// not fit MaxBytes and therefore was not cloned into Observations.
type ObservationPage struct {
	Observations []capture.SanitizedObservation
	// CapturedAt aligns with Observations. A zero value marks a legacy record
	// for which the backend has no trustworthy timestamp.
	CapturedAt []time.Time
	Bytes      int
	HasMore    bool
}

type ErrorCode string

const (
	Invalid    ErrorCode = "invalid"
	Duplicate  ErrorCode = "duplicate"
	Closed     ErrorCode = "closed"
	Incomplete ErrorCode = "incomplete"
)

type StoreError struct{ Code ErrorCode }

func (e *StoreError) Error() string { return "storage: " + string(e.Code) }

// SafeFailureClassifier exposes a bounded, non-sensitive category for logs.
// Implementations must never return data-derived values or error details.
type SafeFailureClassifier interface {
	SafeFailureCode() string
}

func (e *StoreError) SafeFailureCode() string {
	if e == nil {
		return ""
	}
	return string(e.Code)
}

// DerivationState is a closed persisted vocabulary. Models provide content,
// never state transitions; these immutable records are written by core policy.
type DerivationState string

const (
	Building    DerivationState = "building"
	Active      DerivationState = "active"
	Stale       DerivationState = "stale"
	Quarantined DerivationState = "quarantined"
	Failed      DerivationState = "failed"
	Superseded  DerivationState = "superseded"
)

// Derivation is immutable provenance for one generated artifact. SourceIDs are
// stored observation ContentIDs; SourceHashes correspond by index and describe
// the input version used by the processor. Its output is identified by a hash;
// retaining generated content belongs to a later authenticated backend.
type Derivation struct {
	ID, GenerationID                                                 string
	State                                                            DerivationState
	SourceIDs, SourceHashes                                          []string
	ProcessorVersion, SchemaVersion, PromptVersion, RedactionVersion int
	ProviderModelDigest, OutputHash                                  string
	CoveredSources, OmittedSources                                   int
	InputTokens, OutputTokens                                        int
	Latency                                                          time.Duration
}

// Generation is immutable metadata. Active selection is a separate pointer,
// updated only after every referenced derivation is complete and active.
type Generation struct {
	ID                              string
	DerivationIDs                   []string
	SchemaVersion, RedactionVersion int
}

// EncryptedStore is the contract for authenticated-at-rest backends.
// Implementations must truthfully report EncryptedAtRest; a production caller
// must reject false. Every listing is bounded, stable across pages, and accepts
// a logical-ID cursor even if the physical order is opaque.
type EncryptedStore interface {
	EncryptedAtRest() bool
	// PutObservation records the store's current UTC insertion time. PutObservationAt
	// is available when the source has a more precise user-authored creation time.
	PutObservation(context.Context, capture.SanitizedObservation) error
	PutObservationAt(context.Context, capture.SanitizedObservation, time.Time) error
	// DeleteObservationRecord removes one source record; callers must also
	// invalidate any separately materialized derivatives or projections.
	DeleteObservationRecord(context.Context, string) error
	GetObservation(context.Context, string) (capture.SanitizedObservation, bool, error)
	ListObservationPage(context.Context, ObservationPageRequest) (ObservationPage, error)
	PutDerivation(context.Context, Derivation) error
	GetDerivation(context.Context, string) (Derivation, bool, error)
	ListDerivations(context.Context, int, string) ([]Derivation, error)
	PutGeneration(context.Context, Generation) error
	GetGeneration(context.Context, string) (Generation, bool, error)
	ListGenerations(context.Context, int, string) ([]Generation, error)
	ActivateGeneration(context.Context, string) error
	ActiveGeneration(context.Context) (Generation, bool, error)
	Close() error
}

// ChronicleStore persists generated hourly summaries and their provenance.
// Implementations must cascade source deletion and bound every list request.
type ChronicleStore interface {
	PutChronicle(context.Context, chronicle.Summary) error
	GetChronicle(context.Context, string) (chronicle.Summary, bool, error)
	ListChronicles(context.Context, time.Time, time.Time, string, int) ([]chronicle.Summary, string, error)
	DeleteChronicle(context.Context, string) error
	DeleteAllChronicles(context.Context) error
}

// EphemeralMemoryStore is TestOnly. It is never a production default and does
// not encrypt or persist data. It exercises the EncryptedStore contract.
type EphemeralMemoryStore struct {
	mu               sync.RWMutex
	closed           bool
	observations     map[string]capture.SanitizedObservation
	observationBytes map[string]int
	observationTimes map[string]time.Time
	observationIDs   []string
	derivations      map[string]Derivation
	derivationIDs    []string
	generations      map[string]Generation
	generationIDs    []string
	active           string
}

var _ EncryptedStore = (*EphemeralMemoryStore)(nil)

func NewEphemeralMemoryStore() *EphemeralMemoryStore {
	return &EphemeralMemoryStore{observations: make(map[string]capture.SanitizedObservation), observationBytes: make(map[string]int), observationTimes: make(map[string]time.Time), derivations: make(map[string]Derivation), generations: make(map[string]Generation)}
}
func (*EphemeralMemoryStore) EncryptedAtRest() bool { return false }
func (s *EphemeralMemoryStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func ready(ctx context.Context, closed bool) error {
	if ctx == nil {
		return &StoreError{Code: Invalid}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if closed {
		return &StoreError{Code: Closed}
	}
	return nil
}

func insertID(ids []string, id string) []string {
	i := sort.SearchStrings(ids, id)
	ids = append(ids, "")
	copy(ids[i+1:], ids[i:len(ids)-1])
	ids[i] = id
	return ids
}
func pageIDs(ids []string, limit int, after string) []string {
	start := sort.Search(len(ids), func(i int) bool { return ids[i] > after })
	remaining := len(ids) - start
	if limit > remaining {
		limit = remaining
	}
	return ids[start : start+limit]
}
func validHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func validCaptureTime(value time.Time) bool {
	if value.IsZero() {
		return false
	}
	return time.Unix(0, value.UTC().UnixNano()).UTC().Equal(value.UTC())
}
func validID(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
func copyObservation(o capture.SanitizedObservation) capture.SanitizedObservation {
	return o.Clone()
}
func (s *EphemeralMemoryStore) PutObservation(ctx context.Context, o capture.SanitizedObservation) error {
	return s.PutObservationAt(ctx, o, time.Now())
}

func (s *EphemeralMemoryStore) PutObservationAt(ctx context.Context, o capture.SanitizedObservation, capturedAt time.Time) error {
	if ctx == nil {
		return &StoreError{Code: Invalid}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if o.Validate() != nil || !validCaptureTime(capturedAt) {
		return &StoreError{Code: Invalid}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ready(ctx, s.closed); err != nil {
		return err
	}
	id := o.ContentID()
	if _, exists := s.observations[id]; exists {
		return &StoreError{Code: Duplicate}
	}
	searchBytes := len(o.Text()) + len(o.AppID()) + len(o.Domain())
	stored := copyObservation(o)
	storedID := stored.ContentID()
	s.observations[storedID] = stored
	s.observationBytes[storedID] = searchBytes
	s.observationTimes[storedID] = capturedAt.UTC()
	s.observationIDs = insertID(s.observationIDs, storedID)
	return nil
}

func (s *EphemeralMemoryStore) DeleteObservationRecord(ctx context.Context, id string) error {
	if ctx == nil {
		return &StoreError{Code: Invalid}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validHash(id) {
		return &StoreError{Code: Invalid}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ready(ctx, s.closed); err != nil {
		return err
	}
	if _, found := s.observations[id]; !found {
		return nil
	}
	delete(s.observations, id)
	delete(s.observationBytes, id)
	delete(s.observationTimes, id)
	index := sort.SearchStrings(s.observationIDs, id)
	if index < len(s.observationIDs) && s.observationIDs[index] == id {
		copy(s.observationIDs[index:], s.observationIDs[index+1:])
		s.observationIDs = s.observationIDs[:len(s.observationIDs)-1]
	}
	return nil
}

func (s *EphemeralMemoryStore) GetObservation(ctx context.Context, id string) (capture.SanitizedObservation, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ready(ctx, s.closed); err != nil {
		return capture.SanitizedObservation{}, false, err
	}
	if !validHash(id) {
		return capture.SanitizedObservation{}, false, &StoreError{Code: Invalid}
	}
	o, ok := s.observations[id]
	return copyObservation(o), ok, nil
}
func validPage(limit int, after string, hash bool) bool {
	return limit > 0 && limit <= MaxListLimit && (after == "" || (!hash && validID(after)) || (hash && validHash(after)))
}
func validObservationPage(request ObservationPageRequest) bool {
	return request.Limit > 0 && request.Limit <= MaxObservationPageRows && request.MaxBytes > 0 && request.MaxBytes <= MaxObservationPageBytes && (request.After == "" || validHash(request.After))
}
func (s *EphemeralMemoryStore) ListObservationPage(ctx context.Context, request ObservationPageRequest) (ObservationPage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ready(ctx, s.closed); err != nil {
		return ObservationPage{}, err
	}
	if !validObservationPage(request) {
		return ObservationPage{}, &StoreError{Code: Invalid}
	}
	start := sort.Search(len(s.observationIDs), func(i int) bool { return s.observationIDs[i] > request.After })
	out := make([]capture.SanitizedObservation, 0, request.Limit)
	capturedAt := make([]time.Time, 0, request.Limit)
	usedBytes, index := 0, start
	for index < len(s.observationIDs) && len(out) < request.Limit {
		id := s.observationIDs[index]
		observationBytes := s.observationBytes[id]
		if observationBytes > request.MaxBytes-usedBytes {
			break
		}
		out = append(out, copyObservation(s.observations[id]))
		capturedAt = append(capturedAt, s.observationTimes[id])
		usedBytes += observationBytes
		index++
	}
	return ObservationPage{Observations: out, CapturedAt: capturedAt, Bytes: usedBytes, HasMore: index < len(s.observationIDs)}, nil
}
func validDerivation(d Derivation) bool {
	if !validID(d.ID) || !validID(d.GenerationID) || !validHash(d.ProviderModelDigest) || !validHash(d.OutputHash) || d.ProcessorVersion <= 0 || d.SchemaVersion <= 0 || d.PromptVersion <= 0 || d.RedactionVersion != privacy.Version || len(d.SourceIDs) == 0 || len(d.SourceIDs) > MaxListLimit || len(d.SourceIDs) != len(d.SourceHashes) || d.CoveredSources < 0 || d.OmittedSources < 0 || d.CoveredSources+d.OmittedSources != len(d.SourceIDs) || d.InputTokens < 0 || d.OutputTokens < 0 || d.Latency < 0 {
		return false
	}
	switch d.State {
	case Building, Active, Stale, Quarantined, Failed, Superseded:
	default:
		return false
	}
	if d.State == Active && d.CoveredSources == 0 {
		return false
	}
	last := ""
	for i, id := range d.SourceIDs {
		if !validHash(id) || !validHash(d.SourceHashes[i]) || id <= last {
			return false
		}
		last = id
	}
	return true
}
func copyDerivation(d Derivation) Derivation {
	d.ID = strings.Clone(d.ID)
	d.GenerationID = strings.Clone(d.GenerationID)
	d.ProviderModelDigest = strings.Clone(d.ProviderModelDigest)
	d.OutputHash = strings.Clone(d.OutputHash)
	d.SourceIDs = append([]string(nil), d.SourceIDs...)
	d.SourceHashes = append([]string(nil), d.SourceHashes...)
	for i := range d.SourceIDs {
		d.SourceIDs[i] = strings.Clone(d.SourceIDs[i])
		d.SourceHashes[i] = strings.Clone(d.SourceHashes[i])
	}
	return d
}
func (s *EphemeralMemoryStore) PutDerivation(ctx context.Context, d Derivation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ready(ctx, s.closed); err != nil {
		return err
	}
	if !validDerivation(d) {
		return &StoreError{Code: Invalid}
	}
	if _, ok := s.derivations[d.ID]; ok {
		return &StoreError{Code: Duplicate}
	}
	stored := copyDerivation(d)
	s.derivations[stored.ID] = stored
	s.derivationIDs = insertID(s.derivationIDs, stored.ID)
	return nil
}
func (s *EphemeralMemoryStore) GetDerivation(ctx context.Context, id string) (Derivation, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ready(ctx, s.closed); err != nil {
		return Derivation{}, false, err
	}
	if !validID(id) {
		return Derivation{}, false, &StoreError{Code: Invalid}
	}
	d, ok := s.derivations[id]
	return copyDerivation(d), ok, nil
}
func (s *EphemeralMemoryStore) ListDerivations(ctx context.Context, limit int, after string) ([]Derivation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ready(ctx, s.closed); err != nil {
		return nil, err
	}
	if !validPage(limit, after, false) {
		return nil, &StoreError{Code: Invalid}
	}
	ids := pageIDs(s.derivationIDs, limit, after)
	out := make([]Derivation, 0, len(ids))
	for _, id := range ids {
		out = append(out, copyDerivation(s.derivations[id]))
	}
	return out, nil
}
func validGeneration(g Generation) bool {
	if !validID(g.ID) || g.SchemaVersion <= 0 || g.RedactionVersion != privacy.Version || len(g.DerivationIDs) == 0 || len(g.DerivationIDs) > MaxListLimit {
		return false
	}
	last := ""
	for _, id := range g.DerivationIDs {
		if !validID(id) || id <= last {
			return false
		}
		last = id
	}
	return true
}
func copyGeneration(g Generation) Generation {
	g.ID = strings.Clone(g.ID)
	g.DerivationIDs = append([]string(nil), g.DerivationIDs...)
	for i := range g.DerivationIDs {
		g.DerivationIDs[i] = strings.Clone(g.DerivationIDs[i])
	}
	return g
}
func (s *EphemeralMemoryStore) PutGeneration(ctx context.Context, g Generation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ready(ctx, s.closed); err != nil {
		return err
	}
	if !validGeneration(g) {
		return &StoreError{Code: Invalid}
	}
	if _, ok := s.generations[g.ID]; ok {
		return &StoreError{Code: Duplicate}
	}
	stored := copyGeneration(g)
	s.generations[stored.ID] = stored
	s.generationIDs = insertID(s.generationIDs, stored.ID)
	return nil
}
func (s *EphemeralMemoryStore) GetGeneration(ctx context.Context, id string) (Generation, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ready(ctx, s.closed); err != nil {
		return Generation{}, false, err
	}
	if !validID(id) {
		return Generation{}, false, &StoreError{Code: Invalid}
	}
	g, ok := s.generations[id]
	return copyGeneration(g), ok, nil
}
func (s *EphemeralMemoryStore) ListGenerations(ctx context.Context, limit int, after string) ([]Generation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ready(ctx, s.closed); err != nil {
		return nil, err
	}
	if !validPage(limit, after, false) {
		return nil, &StoreError{Code: Invalid}
	}
	ids := pageIDs(s.generationIDs, limit, after)
	out := make([]Generation, 0, len(ids))
	for _, id := range ids {
		out = append(out, copyGeneration(s.generations[id]))
	}
	return out, nil
}
func (s *EphemeralMemoryStore) ActivateGeneration(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ready(ctx, s.closed); err != nil {
		return err
	}
	if !validID(id) {
		return &StoreError{Code: Invalid}
	}
	g, ok := s.generations[id]
	if !ok {
		return &StoreError{Code: Incomplete}
	}
	for _, did := range g.DerivationIDs {
		d, ok := s.derivations[did]
		if !ok || d.GenerationID != id || d.State != Active || d.CoveredSources+d.OmittedSources != len(d.SourceIDs) {
			return &StoreError{Code: Incomplete}
		}
		for _, sourceID := range d.SourceIDs {
			if _, present := s.observations[sourceID]; !present {
				return &StoreError{Code: Incomplete}
			}
		}
	}
	s.active = id
	return nil
}
func (s *EphemeralMemoryStore) ActiveGeneration(ctx context.Context) (Generation, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ready(ctx, s.closed); err != nil {
		return Generation{}, false, err
	}
	if s.active == "" {
		return Generation{}, false, nil
	}
	return copyGeneration(s.generations[s.active]), true, nil
}
