// Package storage defines the encrypted persistence contract. Its only current
// implementation is an ephemeral conformance backend; it provides no encryption.
package storage

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nielsgroen/blur/internal/capture"
	"github.com/nielsgroen/blur/internal/privacy"
)

const MaxObservationTextBytes = 1 << 20
const MaxListLimit = 1000

type ErrorCode string

const (
	Invalid    ErrorCode = "invalid"
	Duplicate  ErrorCode = "duplicate"
	Closed     ErrorCode = "closed"
	Incomplete ErrorCode = "incomplete"
)

type StoreError struct{ Code ErrorCode }

func (e *StoreError) Error() string { return "storage: " + string(e.Code) }

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

// Derivation is immutable provenance for one generated artifact. SourceIDs and
// SourceHashes correspond by index. Its output is identified only by a hash;
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

// EncryptedStore is the contract for a future authenticated-at-rest backend.
// Implementations must truthfully report EncryptedAtRest; a production caller
// must reject false. Every listing is bounded and ordered by stable ID.
type EncryptedStore interface {
	EncryptedAtRest() bool
	PutObservation(context.Context, capture.SanitizedObservation) error
	GetObservation(context.Context, string) (capture.SanitizedObservation, bool, error)
	ListObservations(context.Context, int, string) ([]capture.SanitizedObservation, error)
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

// EphemeralMemoryStore is TestOnly. It is never a production default and does
// not encrypt or persist data. It exercises the EncryptedStore contract.
type EphemeralMemoryStore struct {
	mu           sync.RWMutex
	closed       bool
	observations map[string]capture.SanitizedObservation
	derivations  map[string]Derivation
	generations  map[string]Generation
	active       string
}

var _ EncryptedStore = (*EphemeralMemoryStore)(nil)

func NewEphemeralMemoryStore() *EphemeralMemoryStore {
	return &EphemeralMemoryStore{observations: make(map[string]capture.SanitizedObservation), derivations: make(map[string]Derivation), generations: make(map[string]Generation)}
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
func validLabel(s string, max int) bool {
	return len(s) <= max && utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}
func validObservation(o capture.SanitizedObservation) bool {
	if !validHash(o.ContentID) || !validLabel(o.AppID, 128) || !validLabel(o.Domain, 253) || !validLabel(o.Text, MaxObservationTextBytes) || o.RedactionVersion != privacy.Version || len(o.RedactionCounts) > 32 {
		return false
	}
	for _, s := range []string{o.AppID, o.Domain} {
		for i := 0; i < len(s); i++ {
			c := s[i]
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == '_') {
				return false
			}
		}
	}
	if strings.Contains(o.Domain, "..") || strings.HasPrefix(o.Domain, ".") || strings.HasSuffix(o.Domain, ".") {
		return false
	}
	last := ""
	for _, c := range o.RedactionCounts {
		if c.Count <= 0 || c.Count > MaxObservationTextBytes || c.Rule <= last || len(c.Rule) > 64 || c.Rule == "" {
			return false
		}
		for i := 0; i < len(c.Rule); i++ {
			x := c.Rule[i]
			if !(x >= 'A' && x <= 'Z' || x == '_') {
				return false
			}
		}
		last = c.Rule
	}
	return true
}
func copyObservation(o capture.SanitizedObservation) capture.SanitizedObservation {
	o.ContentID = strings.Clone(o.ContentID)
	o.AppID = strings.Clone(o.AppID)
	o.Domain = strings.Clone(o.Domain)
	o.Text = strings.Clone(o.Text)
	o.RedactionCounts = append([]privacy.Count(nil), o.RedactionCounts...)
	for i := range o.RedactionCounts {
		o.RedactionCounts[i].Rule = strings.Clone(o.RedactionCounts[i].Rule)
	}
	return o
}
func (s *EphemeralMemoryStore) PutObservation(ctx context.Context, o capture.SanitizedObservation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ready(ctx, s.closed); err != nil {
		return err
	}
	if !validObservation(o) {
		return &StoreError{Code: Invalid}
	}
	if _, exists := s.observations[o.ContentID]; exists {
		return &StoreError{Code: Duplicate}
	}
	s.observations[o.ContentID] = copyObservation(o)
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
func (s *EphemeralMemoryStore) ListObservations(ctx context.Context, limit int, after string) ([]capture.SanitizedObservation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ready(ctx, s.closed); err != nil {
		return nil, err
	}
	if !validPage(limit, after, true) {
		return nil, &StoreError{Code: Invalid}
	}
	ids := make([]string, 0, len(s.observations))
	for id := range s.observations {
		if id > after {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	if len(ids) > limit {
		ids = ids[:limit]
	}
	out := make([]capture.SanitizedObservation, 0, len(ids))
	for _, id := range ids {
		out = append(out, copyObservation(s.observations[id]))
	}
	return out, nil
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
	s.derivations[d.ID] = copyDerivation(d)
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
	ids := make([]string, 0, len(s.derivations))
	for id := range s.derivations {
		if id > after {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	if len(ids) > limit {
		ids = ids[:limit]
	}
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
	s.generations[g.ID] = copyGeneration(g)
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
	ids := make([]string, 0, len(s.generations))
	for id := range s.generations {
		if id > after {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	if len(ids) > limit {
		ids = ids[:limit]
	}
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
