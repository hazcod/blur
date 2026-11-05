package core

import (
	"context"
	"errors"
	"strings"
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
	page, err := store.ListObservations(context.Background(), 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 0 {
		t.Fatalf("denied requests wrote %d items", len(page))
	}
	p := proof(t, grant, localapi.ScopeCaptureIngest, IngestDigest(o))
	if err := svc.Ingest(context.Background(), o, p); err != nil {
		t.Fatal(err)
	}
	if err := svc.Ingest(context.Background(), o, p); !errors.Is(err, localapi.ErrReplay) {
		t.Fatalf("replay: %v", err)
	}
	page, err = store.ListObservations(context.Background(), 10, "")
	if err != nil || len(page) != 1 {
		t.Fatalf("stored items: %d, %v", len(page), err)
	}
}

func TestSearchProofBindsLimitsAndReadScope(t *testing.T) {
	svc, _, _, grant := fixture(t, localapi.ScopeCaptureIngest, localapi.ScopeUIRead)
	o := sample(t)
	if err := svc.Ingest(context.Background(), o, proof(t, grant, localapi.ScopeCaptureIngest, IngestDigest(o))); err != nil {
		t.Fatal(err)
	}
	r := search.Request{Query: "café", ResultLimit: 1, SnippetBytes: 20, ResponseBytes: 1024, ScanLimit: 10}
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
	for i, report := range reports {
		if report.State != platform.UserDisabled || report.Validate() != nil || i > 0 && reports[i-1].ID >= report.ID {
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
	page, err := store.ListObservations(context.Background(), 1, "")
	if err != nil || len(page) != 0 {
		t.Fatalf("expired write: %d, %v", len(page), err)
	}
}

func TestMCPReadProofIsAcceptedButMutateIsDenied(t *testing.T) {
	svc, _, _, grant := fixture(t, localapi.ScopeCaptureIngest, localapi.ScopeMCPBoundedRead, localapi.ScopeUIMutate)
	o := sample(t)
	if err := svc.Ingest(context.Background(), o, proof(t, grant, localapi.ScopeCaptureIngest, IngestDigest(o))); err != nil {
		t.Fatal(err)
	}
	r := search.Request{Query: "café", ResultLimit: 1, SnippetBytes: 20, ResponseBytes: 1024, ScanLimit: 10}
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
