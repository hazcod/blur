package core

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nielsgroen/blur/internal/capture"
	"github.com/nielsgroen/blur/internal/localapi"
	"github.com/nielsgroen/blur/internal/platform"
	"github.com/nielsgroen/blur/internal/search"
	"github.com/nielsgroen/blur/internal/storage"
)

func fixture(t *testing.T, scopes ...localapi.Scope) (*Service, *storage.EphemeralMemoryStore, *localapi.GrantStore, localapi.IssuedGrant) {
	t.Helper()
	grants, err := localapi.NewGrantStore(nil, nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	store := storage.NewEphemeralMemoryStore()
	service, err := NewEphemeralDevelopmentService(grants, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := grants.Issue("test.client", scopes, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return service, store, grants, grant
}

func proof(t *testing.T, grant localapi.IssuedGrant, scope localapi.Scope, digest [32]byte) localapi.Proof {
	t.Helper()
	p, err := localapi.NewProof(grant, scope, digest, time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func sample(t *testing.T) capture.SanitizedObservation {
	t.Helper()
	o, err := capture.SanitizeObservation("demo.app", "example.test", "Café plan", 256)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestIngestRequiresOneUseExactProof(t *testing.T) {
	// Catches writes before authorization, wrong scope, or replay acceptance.
	svc, store, _, grant := fixture(t, localapi.ScopeCaptureIngest, localapi.ScopeUIRead)
	o := sample(t)
	if err := svc.Ingest(context.Background(), o, localapi.Proof{}); err == nil {
		t.Fatal("zero proof accepted")
	}
	if err := svc.Ingest(context.Background(), capture.SanitizedObservation{}, proof(t, grant, localapi.ScopeCaptureIngest, IngestDigest(o))); err == nil {
		t.Fatal("zero observation accepted")
	}
	if err := svc.Ingest(context.Background(), o, proof(t, grant, localapi.ScopeUIRead, IngestDigest(o))); err == nil {
		t.Fatal("read scope accepted")
	}
	wrong, err := capture.SanitizeObservation("demo.app", "example.test", "different", 256)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Ingest(context.Background(), wrong, proof(t, grant, localapi.ScopeCaptureIngest, IngestDigest(o))); err == nil {
		t.Fatal("tampered observation accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := svc.Ingest(ctx, o, proof(t, grant, localapi.ScopeCaptureIngest, IngestDigest(o))); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	page, err := store.ListObservationPage(context.Background(), storage.ObservationPageRequest{Limit: 10, MaxBytes: storage.MaxObservationPageBytes})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Observations) != 0 {
		t.Fatalf("denied requests wrote %d items", len(page.Observations))
	}
	p := proof(t, grant, localapi.ScopeCaptureIngest, IngestDigest(o))
	if err := svc.Ingest(context.Background(), o, p); err != nil {
		t.Fatal(err)
	}
	if err := svc.Ingest(context.Background(), o, p); !errors.Is(err, localapi.ErrReplay) {
		t.Fatalf("replay: %v", err)
	}
	page, err = store.ListObservationPage(context.Background(), storage.ObservationPageRequest{Limit: 10, MaxBytes: storage.MaxObservationPageBytes})
	if err != nil || len(page.Observations) != 1 {
		t.Fatalf("stored items: %d, %v", len(page.Observations), err)
	}
}

func TestSearchProofBindsLimitsAndReadScope(t *testing.T) {
	svc, _, _, grant := fixture(t, localapi.ScopeCaptureIngest, localapi.ScopeUIRead)
	o := sample(t)
	if err := svc.Ingest(context.Background(), o, proof(t, grant, localapi.ScopeCaptureIngest, IngestDigest(o))); err != nil {
		t.Fatal(err)
	}
	r := search.Request{Query: "café", ResultLimit: 1, SnippetBytes: 20, ResponseBytes: 1024, ScanLimit: 10, ScanBytes: search.MaxScanBytes}
	p := proof(t, grant, localapi.ScopeUIRead, SearchDigest(r))
	tampered := r
	tampered.ResultLimit = 2
	if _, err := svc.Search(context.Background(), tampered, p); err == nil {
		t.Fatal("changed limit accepted")
	}
	if _, err := svc.Search(context.Background(), r, proof(t, grant, localapi.ScopeCaptureIngest, SearchDigest(r))); err == nil {
		t.Fatal("ingest scope accepted")
	}
	got, err := svc.Search(context.Background(), r, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Results) != 1 || got.Results[0].SourceID != o.ContentID() {
		t.Fatalf("search: %+v", got)
	}
	if _, err := svc.Search(context.Background(), r, p); !errors.Is(err, localapi.ErrReplay) {
		t.Fatalf("search replay: %v", err)
	}
}

func TestSearchDigestBindsAggregateScanByteLimit(t *testing.T) {
	base := search.Request{Query: "alpha", ResultLimit: 2, SnippetBytes: 32, ResponseBytes: 2048, ScanLimit: 8, ScanBytes: 1024}
	changed := base
	changed.ScanBytes = 2048
	if SearchDigest(base) == SearchDigest(changed) {
		t.Fatal("search proof digest did not bind the aggregate scan-byte limit")
	}
	legacy := base
	legacy.ScanBytes = 0
	defaulted := legacy
	defaulted.ScanBytes = search.MaxScanBytes
	if SearchDigest(legacy) == ([32]byte{}) || SearchDigest(legacy) != SearchDigest(defaulted) {
		t.Fatal("omitted aggregate scan-byte limit did not bind its bounded default semantics")
	}
}

type observedDoneContext struct {
	context.Context
	doneObserved chan struct{}
	once         sync.Once
}

func (c *observedDoneContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.doneObserved) })
	return c.Context.Done()
}

type blockingPageStore struct {
	storage.EncryptedStore
	entered chan struct{}
	release chan struct{}
	mu      sync.Mutex
	active  int
	maximum int
}

func (s *blockingPageStore) ListObservationPage(ctx context.Context, request storage.ObservationPageRequest) (storage.ObservationPage, error) {
	s.mu.Lock()
	s.active++
	if s.active > s.maximum {
		s.maximum = s.active
	}
	s.mu.Unlock()
	s.entered <- struct{}{}
	select {
	case <-ctx.Done():
		s.mu.Lock()
		s.active--
		s.mu.Unlock()
		return storage.ObservationPage{}, ctx.Err()
	case <-s.release:
	}
	s.mu.Lock()
	s.active--
	s.mu.Unlock()
	return s.EncryptedStore.ListObservationPage(ctx, request)
}

func TestSearchSemaphoreBoundsConcurrencyAndReleasesCanceledWaiters(t *testing.T) {
	svc, memory, _, grant := fixture(t, localapi.ScopeUIRead)
	blocked := &blockingPageStore{EncryptedStore: memory, entered: make(chan struct{}, 4), release: make(chan struct{})}
	svc.store, svc.search = blocked, search.New(blocked)
	request := search.Request{Query: "alpha", ResultLimit: 1, SnippetBytes: 8, ResponseBytes: 1024, ScanLimit: 1, ScanBytes: search.MaxScanBytes}
	start := func(ctx context.Context) <-chan error {
		result := make(chan error, 1)
		p := proof(t, grant, localapi.ScopeUIRead, SearchDigest(request))
		go func() {
			_, err := svc.Search(ctx, request, p)
			result <- err
		}()
		return result
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(blocked.release) }) }
	defer release()
	first := start(context.Background())
	second := start(context.Background())
	for i := 0; i < 2; i++ {
		select {
		case <-blocked.entered:
		case <-time.After(2 * time.Second):
			t.Fatal("search did not enter the store")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	thirdContext := &observedDoneContext{Context: ctx, doneObserved: make(chan struct{})}
	third := start(thirdContext)
	select {
	case <-thirdContext.doneObserved:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("search did not wait on a context-aware concurrency slot")
	}
	blocked.mu.Lock()
	active, maximum := blocked.active, blocked.maximum
	blocked.mu.Unlock()
	if active != 2 || maximum != 2 {
		cancel()
		t.Fatalf("search concurrency active=%d maximum=%d", active, maximum)
	}
	select {
	case <-blocked.entered:
		cancel()
		t.Fatal("search exceeded the fixed concurrency bound")
	default:
	}
	cancel()
	if err := <-third; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled waiter returned %v", err)
	}
	release()
	for i, result := range []<-chan error{first, second} {
		if err := <-result; err != nil {
			t.Fatalf("active search %d: %v", i, err)
		}
	}
	fourth := start(context.Background())
	if err := <-fourth; err != nil {
		t.Fatalf("search after canceled waiter leaked a slot: %v", err)
	}
	blocked.mu.Lock()
	defer blocked.mu.Unlock()
	if blocked.maximum != 2 || blocked.active != 0 {
		t.Fatalf("final search concurrency active=%d maximum=%d", blocked.active, blocked.maximum)
	}
}

func TestServiceStatusAndCapabilitiesAreAuthorizedAndStable(t *testing.T) {
	svc, _, _, grant := fixture(t, localapi.ScopeCLIAdmin, localapi.ScopeUIRead)
	if _, err := svc.Status(context.Background(), proof(t, grant, localapi.ScopeUIRead, StatusDigest())); err == nil {
		t.Fatal("read scope got status")
	}
	status, err := svc.Status(context.Background(), proof(t, grant, localapi.ScopeCLIAdmin, StatusDigest()))
	if err != nil {
		t.Fatal(err)
	}
	if status.Mode != "ephemeral_memory" || status.EncryptedAtRest || status.AIEnabled || status.NetworkListening || !status.Ready {
		t.Fatalf("untruthful status: %+v", status)
	}
	reports, err := svc.Capabilities(context.Background(), proof(t, grant, localapi.ScopeCLIAdmin, CapabilitiesDigest()))
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 10 {
		t.Fatalf("report count: %d", len(reports))
	}
	wantIDs := []string{"audio_recorder", "autostart", "capture", "gesture_hotkeys", "local_transport", "private_path", "secret_store", "session_state", "surface_manager", "text_targets"}
	for i, report := range reports {
		if report.ID != wantIDs[i] || report.State != platform.UserDisabled || report.Validate() != nil || i > 0 && reports[i-1].ID >= report.ID {
			t.Fatalf("invalid reports: %+v", reports)
		}
	}
	reports[0].ID = "mutated"
	again, err := svc.Capabilities(context.Background(), proof(t, grant, localapi.ScopeCLIAdmin, CapabilitiesDigest()))
	if err != nil || again[0].ID == "mutated" {
		t.Fatalf("mutable capabilities: %v", err)
	}
}

func TestConstructionRejectsPlainStoreAndBadCapabilities(t *testing.T) {
	grants, err := localapi.NewGrantStore(nil, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	store := storage.NewEphemeralMemoryStore()
	if _, err := NewService(grants, store, nil); err == nil {
		t.Fatal("plain store accepted by production constructor")
	}
	if _, err := NewEphemeralDevelopmentService(grants, store, []platform.CapabilityReport{}); err == nil {
		t.Fatal("explicit empty capability set accepted")
	}
	for _, reports := range [][]platform.CapabilityReport{
		{{ID: "demo", State: platform.UserDisabled, Reason: "disabled", Evidence: "configured"}, {ID: "demo", State: platform.UserDisabled, Reason: "disabled", Evidence: "configured"}},
		{{ID: "demo", State: "future", Reason: "disabled", Evidence: "configured"}},
		{{ID: "demo", State: platform.UserDisabled, Reason: "disabled", Evidence: strings.Repeat("secret", 30)}},
	} {
		if _, err := NewEphemeralDevelopmentService(grants, store, reports); err == nil {
			t.Fatalf("invalid report accepted: %+v", reports)
		}
	}
}

func TestStatusReportsClosedStoreAsNotReady(t *testing.T) {
	svc, store, _, grant := fixture(t, localapi.ScopeCLIAdmin)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	status, err := svc.Status(context.Background(), proof(t, grant, localapi.ScopeCLIAdmin, StatusDigest()))
	if err != nil {
		t.Fatal(err)
	}
	if status.Ready {
		t.Fatal("closed store reported ready")
	}
}

func TestExpiredProofCannotWrite(t *testing.T) {
	now := time.Now()
	grants, err := localapi.NewGrantStore(func() time.Time { return now }, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	store := storage.NewEphemeralMemoryStore()
	svc, err := NewEphemeralDevelopmentService(grants, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := grants.Issue("expired.client", []localapi.Scope{localapi.ScopeCaptureIngest}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	o := sample(t)
	p, err := localapi.NewProof(grant, localapi.ScopeCaptureIngest, IngestDigest(o), now, nil)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	if err := svc.Ingest(context.Background(), o, p); !errors.Is(err, localapi.ErrUnauthorized) {
		t.Fatalf("expired proof: %v", err)
	}
	page, err := store.ListObservationPage(context.Background(), storage.ObservationPageRequest{Limit: 1, MaxBytes: storage.MaxObservationPageBytes})
	if err != nil || len(page.Observations) != 0 {
		t.Fatalf("expired write: %d, %v", len(page.Observations), err)
	}
}

func TestMCPReadProofIsAcceptedButMutateIsDenied(t *testing.T) {
	svc, _, _, grant := fixture(t, localapi.ScopeCaptureIngest, localapi.ScopeMCPBoundedRead, localapi.ScopeUIMutate)
	o := sample(t)
	if err := svc.Ingest(context.Background(), o, proof(t, grant, localapi.ScopeCaptureIngest, IngestDigest(o))); err != nil {
		t.Fatal(err)
	}
	r := search.Request{Query: "café", ResultLimit: 1, SnippetBytes: 20, ResponseBytes: 1024, ScanLimit: 10, ScanBytes: search.MaxScanBytes}
	if _, err := svc.Search(context.Background(), r, proof(t, grant, localapi.ScopeUIMutate, SearchDigest(r))); err == nil {
		t.Fatal("mutate scope accepted")
	}
	got, err := svc.Search(context.Background(), r, proof(t, grant, localapi.ScopeMCPBoundedRead, SearchDigest(r)))
	if err != nil || len(got.Results) != 1 {
		t.Fatalf("MCP read: %+v, %v", got, err)
	}
}

func TestEphemeralConstructorRejectsTypedNilStore(t *testing.T) {
	grants, err := localapi.NewGrantStore(nil, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	var store *storage.EphemeralMemoryStore
	if _, err := NewEphemeralDevelopmentService(grants, store, nil); err == nil {
		t.Fatal("nil store accepted")
	}
}

func TestCapabilityReportsRequireExactlyTheTenPortsAndClosedDiagnostics(t *testing.T) {
	grants, err := localapi.NewGrantStore(nil, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	store := storage.NewEphemeralMemoryStore()
	ids := []string{"capture", "text_targets", "gesture_hotkeys", "surface_manager", "audio_recorder", "secret_store", "local_transport", "private_path", "autostart", "session_state"}
	reports := make([]platform.CapabilityReport, len(ids))
	for i, id := range ids {
		reports[i] = platform.CapabilityReport{ID: id, State: platform.UserDisabled, Reason: "adapter_unimplemented", Evidence: "platform_stub"}
	}
	svc, err := NewEphemeralDevelopmentService(grants, store, reports)
	if err != nil {
		t.Fatalf("valid complete reports rejected: %v", err)
	}
	grant, err := grants.Issue("capability.client", []localapi.Scope{localapi.ScopeCLIAdmin}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.Capabilities(context.Background(), proof(t, grant, localapi.ScopeCLIAdmin, CapabilitiesDigest()))
	if err != nil || len(got) != 10 {
		t.Fatalf("capabilities: %v, %v", got, err)
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].ID >= got[i].ID {
			t.Fatalf("unsorted: %v", got)
		}
	}
	bad := [][]platform.CapabilityReport{
		reports[:9],
		append(append([]platform.CapabilityReport(nil), reports...), platform.CapabilityReport{ID: "extra_port", State: platform.UserDisabled, Reason: "foundation_only", Evidence: "configuration"}),
		append(append([]platform.CapabilityReport(nil), reports[:9]...), reports[0]),
	}
	unknownReason := append([]platform.CapabilityReport(nil), reports...)
	unknownReason[0].Reason = "project_alpha"
	unknownEvidence := append([]platform.CapabilityReport(nil), reports...)
	unknownEvidence[0].Evidence = "alice_secret"
	invalidState := append([]platform.CapabilityReport(nil), reports...)
	invalidState[0].State = "unknown"
	bad = append(bad, unknownReason, unknownEvidence, invalidState)
	for i, set := range bad {
		if _, err := NewEphemeralDevelopmentService(grants, store, set); err == nil {
			t.Fatalf("invalid set %d accepted", i)
		}
	}
}

type cancelAfterListStore struct {
	storage.EncryptedStore
	cancel context.CancelFunc
}

func (s cancelAfterListStore) ListObservationPage(ctx context.Context, request storage.ObservationPageRequest) (storage.ObservationPage, error) {
	page, err := s.EncryptedStore.ListObservationPage(ctx, request)
	s.cancel()
	return page, err
}

type cancelAfterPutStore struct {
	storage.EncryptedStore
	cancel context.CancelFunc
}

func (s cancelAfterPutStore) PutObservation(ctx context.Context, o capture.SanitizedObservation) error {
	err := s.EncryptedStore.PutObservation(ctx, o)
	s.cancel()
	return err
}

type cancelBeforePutStore struct {
	storage.EncryptedStore
	cancel context.CancelFunc
}

func (s cancelBeforePutStore) PutObservation(ctx context.Context, o capture.SanitizedObservation) error {
	s.cancel()
	return s.EncryptedStore.PutObservation(ctx, o)
}

func TestCancellationDuringAuthorizationFailsClosed(t *testing.T) {
	fixed := time.Now()
	var cancelNow context.CancelFunc
	armed := false
	grants, err := localapi.NewGrantStore(func() time.Time {
		if armed {
			cancelNow()
		}
		return fixed
	}, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	store := storage.NewEphemeralMemoryStore()
	svc, err := NewEphemeralDevelopmentService(grants, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := grants.Issue("cancel.client", []localapi.Scope{localapi.ScopeCLIAdmin, localapi.ScopeCaptureIngest, localapi.ScopeUIRead}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	o := sample(t)
	r := search.Request{Query: "café", ResultLimit: 1, SnippetBytes: 20, ResponseBytes: 1024, ScanLimit: 10, ScanBytes: search.MaxScanBytes}
	operations := []struct {
		name   string
		scope  localapi.Scope
		digest [32]byte
		call   func(context.Context, localapi.Proof) error
	}{
		{"status", localapi.ScopeCLIAdmin, StatusDigest(), func(ctx context.Context, p localapi.Proof) error {
			status, err := svc.Status(ctx, p)
			if status.Ready {
				return errors.New("canceled status returned")
			}
			return err
		}},
		{"capabilities", localapi.ScopeCLIAdmin, CapabilitiesDigest(), func(ctx context.Context, p localapi.Proof) error {
			reports, err := svc.Capabilities(ctx, p)
			if len(reports) > 0 {
				return errors.New("canceled reports returned")
			}
			return err
		}},
		{"ingest", localapi.ScopeCaptureIngest, IngestDigest(o), func(ctx context.Context, p localapi.Proof) error { return svc.Ingest(ctx, o, p) }},
		{"search", localapi.ScopeUIRead, SearchDigest(r), func(ctx context.Context, p localapi.Proof) error {
			result, err := svc.Search(ctx, r, p)
			if len(result.Results) > 0 {
				return errors.New("canceled results returned")
			}
			return err
		}},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			p, err := localapi.NewProof(grant, operation.scope, operation.digest, fixed, nil)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancelNow, armed = cancel, true
			err = operation.call(ctx, p)
			armed = false
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled call returned %v", err)
			}
			if err := operation.call(context.Background(), p); !errors.Is(err, localapi.ErrReplay) {
				t.Fatalf("proof consumed during authorization should replay-fail: %v", err)
			}
		})
	}
	page, err := store.ListObservationPage(context.Background(), storage.ObservationPageRequest{Limit: 1, MaxBytes: storage.MaxObservationPageBytes})
	if err != nil || len(page.Observations) != 0 {
		t.Fatalf("canceled authorization wrote: %d, %v", len(page.Observations), err)
	}
}

type deadlineProbeStore struct{ storage.EncryptedStore }

func (deadlineProbeStore) ListObservationPage(context.Context, storage.ObservationPageRequest) (storage.ObservationPage, error) {
	return storage.ObservationPage{}, context.DeadlineExceeded
}

func TestStatusPropagatesStoreDeadline(t *testing.T) {
	svc, memory, _, grant := fixture(t, localapi.ScopeCLIAdmin)
	svc.store = deadlineProbeStore{memory}
	status, err := svc.Status(context.Background(), proof(t, grant, localapi.ScopeCLIAdmin, StatusDigest()))
	if !errors.Is(err, context.DeadlineExceeded) || status.Ready {
		t.Fatalf("deadline converted to status: %+v, %v", status, err)
	}
}

func TestCancellationAfterStoreOperationNeverReturnsSuccess(t *testing.T) {
	for _, operation := range []string{"status", "search"} {
		t.Run(operation, func(t *testing.T) {
			svc, memory, _, grant := fixture(t, localapi.ScopeCLIAdmin, localapi.ScopeUIRead)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch operation {
			case "status", "search":
				wrapped := cancelAfterListStore{EncryptedStore: memory, cancel: cancel}
				svc.store, svc.search = wrapped, search.New(wrapped)
			}
			var err error
			switch operation {
			case "status":
				var status StatusReport
				status, err = svc.Status(ctx, proof(t, grant, localapi.ScopeCLIAdmin, StatusDigest()))
				if status.Ready {
					t.Fatal("canceled status returned")
				}
			case "search":
				r := search.Request{Query: "alpha", ResultLimit: 1, SnippetBytes: 8, ResponseBytes: 1024, ScanLimit: 2, ScanBytes: search.MaxScanBytes}
				var result search.Response
				result, err = svc.Search(ctx, r, proof(t, grant, localapi.ScopeUIRead, SearchDigest(r)))
				if len(result.Results) > 0 {
					t.Fatal("canceled result returned")
				}
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled operation returned %v", err)
			}
		})
	}
}

func TestAlreadyCanceledRequestsDoNotConsumeProofsOrReturnData(t *testing.T) {
	svc, memory, _, grant := fixture(t, localapi.ScopeCLIAdmin, localapi.ScopeCaptureIngest, localapi.ScopeUIRead)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	statusProof := proof(t, grant, localapi.ScopeCLIAdmin, StatusDigest())
	if status, err := svc.Status(ctx, statusProof); !errors.Is(err, context.Canceled) || status.Ready {
		t.Fatalf("canceled status: %+v, %v", status, err)
	}
	if _, err := svc.Status(context.Background(), statusProof); err != nil {
		t.Fatalf("canceled request consumed status proof: %v", err)
	}
	capabilityProof := proof(t, grant, localapi.ScopeCLIAdmin, CapabilitiesDigest())
	if reports, err := svc.Capabilities(ctx, capabilityProof); !errors.Is(err, context.Canceled) || len(reports) != 0 {
		t.Fatalf("canceled capabilities: %d, %v", len(reports), err)
	}
	if _, err := svc.Capabilities(context.Background(), capabilityProof); err != nil {
		t.Fatalf("canceled request consumed capability proof: %v", err)
	}
	o := sample(t)
	ingestProof := proof(t, grant, localapi.ScopeCaptureIngest, IngestDigest(o))
	if err := svc.Ingest(ctx, o, ingestProof); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled ingest: %v", err)
	}
	page, err := memory.ListObservationPage(context.Background(), storage.ObservationPageRequest{Limit: 1, MaxBytes: storage.MaxObservationPageBytes})
	if err != nil || len(page.Observations) != 0 {
		t.Fatalf("canceled ingest wrote: %d, %v", len(page.Observations), err)
	}
	if err := svc.Ingest(context.Background(), o, ingestProof); err != nil {
		t.Fatalf("canceled request consumed ingest proof: %v", err)
	}
	r := search.Request{Query: "café", ResultLimit: 1, SnippetBytes: 20, ResponseBytes: 1024, ScanLimit: 10, ScanBytes: search.MaxScanBytes}
	searchProof := proof(t, grant, localapi.ScopeUIRead, SearchDigest(r))
	if result, err := svc.Search(ctx, r, searchProof); !errors.Is(err, context.Canceled) || len(result.Results) != 0 {
		t.Fatalf("canceled search: %+v, %v", result, err)
	}
	if result, err := svc.Search(context.Background(), r, searchProof); err != nil || len(result.Results) != 1 {
		t.Fatalf("canceled request consumed search proof: %+v, %v", result, err)
	}
}

func TestSuccessfulIngestCommitWinsCancellationRace(t *testing.T) {
	svc, memory, _, grant := fixture(t, localapi.ScopeCaptureIngest)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc.store = cancelAfterPutStore{EncryptedStore: memory, cancel: cancel}
	o := sample(t)
	if err := svc.Ingest(ctx, o, proof(t, grant, localapi.ScopeCaptureIngest, IngestDigest(o))); err != nil {
		t.Fatalf("committed write reported failure: %v", err)
	}
	page, err := memory.ListObservationPage(context.Background(), storage.ObservationPageRequest{Limit: 2, MaxBytes: storage.MaxObservationPageBytes})
	if err != nil || len(page.Observations) != 1 || page.Observations[0].ContentID() != o.ContentID() {
		t.Fatalf("successful commit missing or duplicated: %d, %v", len(page.Observations), err)
	}
}

func TestStoreCancellationErrorLeavesNoIngestWrite(t *testing.T) {
	svc, memory, _, grant := fixture(t, localapi.ScopeCaptureIngest)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc.store = cancelBeforePutStore{EncryptedStore: memory, cancel: cancel}
	o := sample(t)
	if err := svc.Ingest(ctx, o, proof(t, grant, localapi.ScopeCaptureIngest, IngestDigest(o))); !errors.Is(err, context.Canceled) {
		t.Fatalf("store cancellation error lost: %v", err)
	}
	page, err := memory.ListObservationPage(context.Background(), storage.ObservationPageRequest{Limit: 1, MaxBytes: storage.MaxObservationPageBytes})
	if err != nil || len(page.Observations) != 0 {
		t.Fatalf("canceled store write committed: %d, %v", len(page.Observations), err)
	}
}

func TestCapabilityTuplesAreCoherent(t *testing.T) {
	grants, err := localapi.NewGrantStore(nil, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	store := storage.NewEphemeralMemoryStore()
	accepted := []struct {
		id       string
		state    platform.CapabilityState
		reason   string
		evidence string
	}{
		{"capture", platform.UserDisabled, "foundation_only", "adapter_not_connected"},
		{"capture", platform.UserDisabled, "adapter_unimplemented", "axui_element"},
		{"capture", platform.Available, "available", "axui_element"},
		{"capture", platform.PermissionRequired, "permission_required", "axui_element"},
		{"capture", platform.UnsupportedTarget, "unsupported_target", "axui_element"},
		{"capture", platform.SecureTarget, "secure_target", "axui_element"},
		{"capture", platform.TargetElevated, "target_elevated", "axui_element"},
		{"gesture_hotkeys", platform.PortalMissing, "portal_missing", "xdg_portal"},
		{"gesture_hotkeys", platform.TemporarilyUnresponsive, "temporarily_unresponsive", "xdg_portal"},
		{"autostart", platform.UserDisabled, "user_disabled", "configuration"},
	}
	for _, row := range accepted {
		reports := defaultReports()
		for i := range reports {
			if reports[i].ID == row.id {
				reports[i].State, reports[i].Reason, reports[i].Evidence = row.state, row.reason, row.evidence
			}
		}
		if _, err := NewEphemeralDevelopmentService(grants, store, reports); err != nil {
			t.Fatalf("valid tuple %s/%s/%s/%s rejected: %v", row.id, row.state, row.reason, row.evidence, err)
		}
	}
	contradictions := []struct {
		id               string
		state            platform.CapabilityState
		reason, evidence string
	}{
		{"capture", platform.Available, "permission_required", "adapter_not_connected"},
		{"capture", platform.PermissionRequired, "foundation_only", "adapter_not_connected"},
		{"capture", platform.Available, "available", "adapter_not_connected"},
		{"capture", platform.UserDisabled, "user_disabled", "axui_element"},
		{"capture", platform.UserDisabled, "adapter_unimplemented", "configuration"},
		{"gesture_hotkeys", platform.PortalMissing, "portal_missing", "event_tap"},
		{"gesture_hotkeys", platform.TargetElevated, "target_elevated", "xdg_portal"},
		{"capture", platform.SecureTarget, "secure_target", "configuration"},
	}
	for _, row := range contradictions {
		reports := defaultReports()
		for i := range reports {
			if reports[i].ID == row.id {
				reports[i].State, reports[i].Reason, reports[i].Evidence = row.state, row.reason, row.evidence
			}
		}
		if _, err := NewEphemeralDevelopmentService(grants, store, reports); err == nil {
			t.Fatalf("contradictory tuple %s/%s/%s/%s accepted", row.id, row.state, row.reason, row.evidence)
		}
	}
}

func TestCapabilityEvidenceMatchesPort(t *testing.T) {
	grants, err := localapi.NewGrantStore(nil, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	store := storage.NewEphemeralMemoryStore()
	ports := []struct {
		id       string
		evidence []string
	}{
		{"capture", []string{"axui_element", "ui_automation", "at_spi2"}},
		{"text_targets", []string{"axui_element", "ui_automation", "at_spi2"}},
		{"gesture_hotkeys", []string{"event_tap", "register_hotkey", "xdg_portal"}},
		{"surface_manager", []string{"appkit", "win32", "desktop_shell", "xdg_portal"}},
		{"audio_recorder", []string{"core_audio", "wasapi", "pipewire"}},
		{"secret_store", []string{"keychain", "dpapi", "secret_service"}},
		{"local_transport", []string{"af_unix", "named_pipe"}},
		{"private_path", []string{"filesystem_permissions"}},
		{"autostart", []string{"sm_app_service", "startup_task", "systemd_user"}},
		{"session_state", []string{"workspace_session", "win_session", "logind"}},
	}
	check := func(id string, state platform.CapabilityState, reason, evidence string, want bool) {
		t.Helper()
		reports := defaultReports()
		for i := range reports {
			if reports[i].ID == id {
				reports[i].State, reports[i].Reason, reports[i].Evidence = state, reason, evidence
			}
		}
		_, err := NewEphemeralDevelopmentService(grants, store, reports)
		if (err == nil) != want {
			t.Fatalf("port tuple %s/%s/%s/%s accepted=%t, want %t", id, state, reason, evidence, err == nil, want)
		}
	}
	for _, port := range ports {
		check(port.id, platform.UserDisabled, "foundation_only", "adapter_not_connected", true)
		check(port.id, platform.UserDisabled, "adapter_unimplemented", "platform_stub", true)
		check(port.id, platform.UserDisabled, "user_disabled", "configuration", true)
		check(port.id, platform.Available, "available", "platform_stub", false)
		check(port.id, platform.PermissionRequired, "permission_required", "platform_stub", false)
		for _, evidence := range port.evidence {
			check(port.id, platform.UserDisabled, "adapter_unimplemented", evidence, true)
			permissionPort := port.id == "capture" || port.id == "text_targets" || port.id == "gesture_hotkeys" || port.id == "audio_recorder" || port.id == "secret_store" || port.id == "autostart" || port.id == "session_state"
			check(port.id, platform.PermissionRequired, "permission_required", evidence, permissionPort)
			check(port.id, platform.Available, "available", evidence, true)
		}
		allowed := make(map[string]bool, len(port.evidence))
		for _, evidence := range port.evidence {
			allowed[evidence] = true
		}
		for _, other := range ports {
			for _, evidence := range other.evidence {
				if allowed[evidence] {
					continue
				}
				check(port.id, platform.UserDisabled, "adapter_unimplemented", evidence, false)
				check(port.id, platform.Available, "available", evidence, false)
			}
		}
		for _, evidence := range port.evidence {
			targetState := port.id == "capture" || port.id == "text_targets"
			for _, row := range []struct {
				state  platform.CapabilityState
				reason string
			}{
				{platform.UnsupportedTarget, "unsupported_target"},
				{platform.SecureTarget, "secure_target"},
				{platform.TargetElevated, "target_elevated"},
			} {
				check(port.id, row.state, row.reason, evidence, targetState)
			}

			permissionPort := port.id == "capture" || port.id == "text_targets" || port.id == "gesture_hotkeys" || port.id == "audio_recorder" || port.id == "secret_store" || port.id == "autostart" || port.id == "session_state"
			check(port.id, platform.PermissionRequired, "permission_required", evidence, permissionPort)
			check(port.id, platform.TemporarilyUnresponsive, "temporarily_unresponsive", evidence, true)
		}
		check(port.id, platform.TemporarilyUnresponsive, "temporarily_unresponsive", "platform_stub", false)
		check(port.id, platform.TemporarilyUnresponsive, "temporarily_unresponsive", "configuration", false)
		check(port.id, platform.TemporarilyUnresponsive, "temporarily_unresponsive", "adapter_not_connected", false)
	}
	check("gesture_hotkeys", platform.PortalMissing, "portal_missing", "xdg_portal", true)
	check("surface_manager", platform.PortalMissing, "portal_missing", "xdg_portal", true)
	check("capture", platform.PortalMissing, "portal_missing", "xdg_portal", false)
	check("autostart", platform.PortalMissing, "portal_missing", "xdg_portal", false)
}
