// Package core owns in-process authorization and the offline sanitized memory slice.
package core

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"hash"
	"reflect"
	"sort"
	"time"

	"github.com/nielsgroen/blur/internal/buildinfo"
	"github.com/nielsgroen/blur/internal/capture"
	"github.com/nielsgroen/blur/internal/localapi"
	"github.com/nielsgroen/blur/internal/platform"
	"github.com/nielsgroen/blur/internal/search"
	"github.com/nielsgroen/blur/internal/storage"
)

var ErrInvalidService = errors.New("core: invalid service configuration")

type Service struct {
	grants  *localapi.GrantStore
	store   storage.EncryptedStore
	search  *search.Engine
	reports []platform.CapabilityReport
	mode    string
}

type StatusReport struct {
	Version          string `json:"version"`
	Mode             string `json:"mode"`
	EncryptedAtRest  bool   `json:"encrypted_at_rest"`
	AIEnabled        bool   `json:"ai_enabled"`
	NetworkListening bool   `json:"network_listening"`
	Ready            bool   `json:"ready"`
}

var defaultIDs = [...]string{"audio_recorder", "autostart", "capture_source", "gesture_hotkeys", "local_transport", "private_path", "secret_store", "session_state", "surface_manager", "text_targets"}

func defaultReports() []platform.CapabilityReport {
	reports := make([]platform.CapabilityReport, 0, len(defaultIDs))
	for _, id := range defaultIDs {
		reports = append(reports, platform.CapabilityReport{ID: id, State: platform.UserDisabled, Reason: "foundation_only", Evidence: "adapter_not_connected"})
	}
	return reports
}

func safeCode(s string) bool {
	if len(s) == 0 || len(s) > 64 || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 1; i < len(s); i++ {
		b := s[i]
		if !(b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '_') {
			return false
		}
	}
	return true
}

func newService(grants *localapi.GrantStore, store storage.EncryptedStore, reports []platform.CapabilityReport, ephemeral bool) (*Service, error) {
	if grants == nil || store == nil || reflect.ValueOf(store).Kind() == reflect.Ptr && reflect.ValueOf(store).IsNil() || (!store.EncryptedAtRest() && !ephemeral) {
		return nil, ErrInvalidService
	}
	if reports == nil {
		reports = defaultReports()
	}
	copyReports := append([]platform.CapabilityReport(nil), reports...)
	if len(copyReports) == 0 {
		return nil, ErrInvalidService
	}
	seen := make(map[string]bool, len(copyReports))
	for _, report := range copyReports {
		if report.Validate() != nil || !safeCode(report.ID) || !safeCode(report.Reason) || !safeCode(report.Evidence) || seen[report.ID] {
			return nil, ErrInvalidService
		}
		seen[report.ID] = true
	}
	sort.Slice(copyReports, func(i, j int) bool { return copyReports[i].ID < copyReports[j].ID })
	mode := "encrypted_store"
	if ephemeral {
		mode = "ephemeral_memory"
	}
	return &Service{grants: grants, store: store, search: search.New(store), reports: copyReports, mode: mode}, nil
}

// NewService is the production constructor and requires truthful at-rest encryption.
func NewService(grants *localapi.GrantStore, store storage.EncryptedStore, reports []platform.CapabilityReport) (*Service, error) {
	return newService(grants, store, reports, false)
}

// NewEphemeralDevelopmentService explicitly opts into an unencrypted, nonpersistent proof mode.
func NewEphemeralDevelopmentService(grants *localapi.GrantStore, store storage.EncryptedStore, reports []platform.CapabilityReport) (*Service, error) {
	memory, ok := store.(*storage.EphemeralMemoryStore)
	if !ok || memory == nil {
		return nil, ErrInvalidService
	}
	return newService(grants, store, reports, true)
}

func field(h hash.Hash, value []byte) {
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(value)))
	_, _ = h.Write(size[:])
	_, _ = h.Write(value)
}

func operationDigest(operation string, fields ...[]byte) [32]byte {
	h := sha256.New()
	field(h, []byte("blur-core-operation-v1"))
	field(h, []byte(operation))
	for _, part := range fields {
		field(h, part)
	}
	var result [32]byte
	copy(result[:], h.Sum(nil))
	return result
}

func integer(i int) []byte { var b [8]byte; binary.BigEndian.PutUint64(b[:], uint64(i)); return b[:] }

// IngestDigest is the client-side signing helper; Ingest recomputes it after validation.
func IngestDigest(o capture.SanitizedObservation) [32]byte {
	if o.Validate() != nil {
		return [32]byte{}
	}
	return operationDigest("capture:ingest", []byte(o.ContentID()))
}

// SearchDigest binds the normalized terms and every result, scan and byte limit.
func SearchDigest(r search.Request) [32]byte {
	canonical, err := r.Validate()
	if err != nil {
		return [32]byte{}
	}
	return operationDigest("search", []byte(canonical), integer(r.ResultLimit), integer(r.SnippetBytes), integer(r.ResponseBytes), integer(r.ScanLimit))
}

func StatusDigest() [32]byte       { return operationDigest("cli:status") }
func CapabilitiesDigest() [32]byte { return operationDigest("cli:capabilities") }

func (s *Service) authorize(ctx context.Context, proof localapi.Proof, scope localapi.Scope, digest [32]byte) error {
	if ctx == nil {
		return ErrInvalidService
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || proof.Scope != scope || digest == ([32]byte{}) {
		return localapi.ErrUnauthorized
	}
	return s.grants.Authorize(proof, digest)
}

func (s *Service) Ingest(ctx context.Context, o capture.SanitizedObservation, proof localapi.Proof) error {
	if ctx == nil {
		return ErrInvalidService
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := o.Validate(); err != nil {
		return err
	}
	if err := s.authorize(ctx, proof, localapi.ScopeCaptureIngest, IngestDigest(o)); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.store.PutObservation(ctx, o)
}

func (s *Service) Search(ctx context.Context, request search.Request, proof localapi.Proof) (search.Response, error) {
	if ctx == nil {
		return search.Response{}, ErrInvalidService
	}
	if err := ctx.Err(); err != nil {
		return search.Response{}, err
	}
	if _, err := request.Validate(); err != nil {
		return search.Response{}, err
	}
	if proof.Scope != localapi.ScopeUIRead && proof.Scope != localapi.ScopeMCPBoundedRead {
		return search.Response{}, localapi.ErrUnauthorized
	}
	if err := s.authorize(ctx, proof, proof.Scope, SearchDigest(request)); err != nil {
		return search.Response{}, err
	}
	return s.search.Search(ctx, request)
}

func (s *Service) Status(ctx context.Context, proof localapi.Proof) (StatusReport, error) {
	if err := s.authorize(ctx, proof, localapi.ScopeCLIAdmin, StatusDigest()); err != nil {
		return StatusReport{}, err
	}
	_, err := s.store.ListObservations(ctx, 1, "")
	return StatusReport{Version: buildinfo.Version, Mode: s.mode, EncryptedAtRest: s.store.EncryptedAtRest(), AIEnabled: false, NetworkListening: false, Ready: err == nil}, nil
}

func (s *Service) Capabilities(ctx context.Context, proof localapi.Proof) ([]platform.CapabilityReport, error) {
	if err := s.authorize(ctx, proof, localapi.ScopeCLIAdmin, CapabilitiesDigest()); err != nil {
		return nil, err
	}
	return append([]platform.CapabilityReport(nil), s.reports...), nil
}

// CLIClient holds a single in-process grant. Every request receives a fresh proof.
type CLIClient struct {
	service *Service
	grant   localapi.IssuedGrant
}

func NewCLIClient(service *Service, grant localapi.IssuedGrant) (*CLIClient, error) {
	if service == nil || len(grant.Credential) != 32 {
		return nil, ErrInvalidService
	}
	return &CLIClient{service: service, grant: grant}, nil
}

func (c *CLIClient) requestProof(digest [32]byte) (localapi.Proof, error) {
	if c == nil {
		return localapi.Proof{}, ErrInvalidService
	}
	return localapi.NewProof(c.grant, localapi.ScopeCLIAdmin, digest, time.Now(), nil)
}

func (c *CLIClient) Status(ctx context.Context) (StatusReport, error) {
	p, err := c.requestProof(StatusDigest())
	if err != nil {
		return StatusReport{}, err
	}
	return c.service.Status(ctx, p)
}

func (c *CLIClient) Capabilities(ctx context.Context) ([]platform.CapabilityReport, error) {
	p, err := c.requestProof(CapabilitiesDigest())
	if err != nil {
		return nil, err
	}
	return c.service.Capabilities(ctx, p)
}

// NewEphemeralDemoCLIClient is solely for offline foundation command smoke.
func NewEphemeralDemoCLIClient() (*CLIClient, error) {
	grants, err := localapi.NewGrantStore(nil, nil, 16)
	if err != nil {
		return nil, err
	}
	service, err := NewEphemeralDevelopmentService(grants, storage.NewEphemeralMemoryStore(), nil)
	if err != nil {
		return nil, err
	}
	grant, err := grants.Issue("blurctl.demo", []localapi.Scope{localapi.ScopeCLIAdmin}, time.Minute)
	if err != nil {
		return nil, err
	}
	return NewCLIClient(service, grant)
}
