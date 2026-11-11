//go:build darwin

package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/client"
	"github.com/hazcod/blur/internal/ingest"
	"github.com/hazcod/blur/internal/localapi"
	"github.com/hazcod/blur/internal/mcpserver"
	"github.com/hazcod/blur/internal/platform"
)

type commandSecrets struct{ values map[string][]byte }

func (s *commandSecrets) Capability(context.Context) (platform.CapabilityReport, error) {
	return platform.CapabilityReport{ID: "secret_store", State: platform.Available}, nil
}
func (s *commandSecrets) Get(_ context.Context, key string, max int) ([]byte, error) {
	value := s.values[key]
	if len(value) == 0 || len(value) > max {
		return nil, platform.ErrReadLimit
	}
	return append([]byte(nil), value...), nil
}
func (s *commandSecrets) Put(_ context.Context, key string, value []byte) error {
	if s.values == nil {
		s.values = make(map[string][]byte)
	}
	s.values[key] = append([]byte(nil), value...)
	return nil
}
func (s *commandSecrets) Delete(_ context.Context, key string) error {
	delete(s.values, key)
	return nil
}

func TestStartupFailureShowsSafeSetupWindow(t *testing.T) {
	called := false
	wantErr := errors.New("storage unavailable")
	err := launch(nil, wantErr, func(app *client.Client, controllers ...*ingest.Controller) error {
		called = true
		if app != nil || len(controllers) != 0 {
			t.Fatalf("setup launch received client=%v controllers=%d", app, len(controllers))
		}
		return nil
	})
	if err != nil || !called {
		t.Fatalf("launch() = %v, called=%t", err, called)
	}
}

func TestStartupMissingKeyShowsRecoveryWindowWithoutOpeningStore(t *testing.T) {
	desktopCalled := false
	setupCalled := false
	err := launch(nil, client.ErrMissingDataKey, func(*client.Client, ...*ingest.Controller) error {
		desktopCalled = true
		return nil
	}, func(issue error) error {
		setupCalled = true
		if !errors.Is(issue, client.ErrMissingDataKey) {
			t.Fatalf("setup issue = %v, want missing key", issue)
		}
		return nil
	})
	if err != nil || desktopCalled || !setupCalled {
		t.Fatalf("launch() = %v, regular_desktop=%t, setup_desktop=%t", err, desktopCalled, setupCalled)
	}
}

func TestDuplicateInstanceDoesNotOpenSecondDesktop(t *testing.T) {
	called := false
	err := launch(nil, client.ErrAlreadyRunning, func(*client.Client, ...*ingest.Controller) error {
		called = true
		return nil
	})
	if !errors.Is(err, client.ErrAlreadyRunning) || called {
		t.Fatalf("launch() = %v, called=%t; want duplicate-instance error and no second desktop", err, called)
	}
}

func TestStartupFailureMessageDistinguishesDesktopRuntimeFromStorage(t *testing.T) {
	if got := startupFailureMessage(errDesktopRuntime); got != "Blur could not continue running its desktop interface" {
		t.Fatalf("desktop failure message = %q", got)
	}
	if got := startupFailureMessage(client.ErrMissingDataKey); got != "Blur found encrypted memory but its Keychain key is missing; it did not replace the key" {
		t.Fatalf("missing-key message = %q", got)
	}
}

func TestDesktopRuntimeErrorsAreClassifiedForBothStartupWindows(t *testing.T) {
	wantCause := errors.New("native event loop failed")
	setupErr := launch(nil, errors.New("store unavailable"), func(*client.Client, ...*ingest.Controller) error {
		t.Fatal("regular desktop was used after storage startup failure")
		return nil
	}, func(error) error { return wantCause })
	if !errors.Is(setupErr, errDesktopRuntime) || !errors.Is(setupErr, wantCause) {
		t.Fatalf("setup desktop error = %v, want runtime and original cause", setupErr)
	}
	if got := startupFailureMessage(setupErr); got != "Blur could not continue running its desktop interface" {
		t.Fatalf("setup desktop failure message = %q", got)
	}

	regularErr := launch(&client.LocalApp{}, nil, func(*client.Client, ...*ingest.Controller) error { return wantCause })
	if !errors.Is(regularErr, errDesktopRuntime) || !errors.Is(regularErr, wantCause) {
		t.Fatalf("regular desktop error = %v, want runtime and original cause", regularErr)
	}
}

func TestMCPCommandClientUsesOnlyTheStoredReadOnlyGrant(t *testing.T) {
	ctx := context.Background()
	transport := localapi.NewMemoryTransport(4, 32<<10)
	endpoint := localapi.EndpointDescriptor{Kind: localapi.EndpointUnix, Address: "/tmp/blur-mcp-command"}
	secrets := &commandSecrets{}
	grants, err := localapi.NewGrantStore(nil, nil, 8)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := grants.Issue("claude.mcp", []localapi.Scope{localapi.ScopeMCPBoundedRead}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := localapi.EncodeIssuedGrant(grant)
	if err != nil {
		t.Fatal(err)
	}
	if err := secrets.Put(ctx, mcpserver.GrantSecretName, encoded); err != nil {
		t.Fatal(err)
	}
	clear(encoded)
	owner := localapi.NewRPCOwner(transport, endpoint, func(_ context.Context, method string, _ json.RawMessage) (json.RawMessage, error) {
		if method != localapi.RPCSearchMemory {
			return nil, localapi.ErrRPCInvalid
		}
		return json.RawMessage(`{"results":[],"truncated":false}`), nil
	})
	if err := owner.StartWithGrant(ctx, grant); err != nil {
		t.Fatal(err)
	}
	defer owner.Stop()
	rpc, err := mcpClientFromSecret(ctx, secrets, transport, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer rpc.Close()
	if _, err := rpc.Call(ctx, localapi.RPCSearchMemory, json.RawMessage(`{"query":"launch"}`)); err != nil {
		t.Fatalf("MCP command credential did not reach the read-only owner: %v", err)
	}
	if _, err := rpc.Call(ctx, "delete_all", json.RawMessage(`{}`)); !errors.Is(err, localapi.ErrRPCInvalid) {
		t.Fatalf("MCP command client accepted a mutation method: %v", err)
	}
}
