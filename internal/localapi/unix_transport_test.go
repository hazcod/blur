//go:build darwin || linux

package localapi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestUnixTransportPrivatePeerCheckedRoundTrip(t *testing.T) {
	ctx := context.Background()
	dir := shortPrivateTempDir(t)
	endpoint := EndpointDescriptor{Kind: EndpointUnix, Address: filepath.Join(dir, "s")}
	transport := NewUnixTransport(1024)
	listener, err := transport.Listen(ctx, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	info, err := os.Lstat(endpoint.Address)
	if err != nil || info.Mode().Perm() != 0o600 || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("socket mode/type = %v, err=%v", info, err)
	}
	client, err := transport.Dial(ctx, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := listener.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if err := client.Send(ctx, []byte("bounded frame")); err != nil {
		t.Fatal(err)
	}
	got, err := server.Receive(ctx)
	if err != nil || string(got) != "bounded frame" {
		t.Fatalf("received %q, err=%v", got, err)
	}
	if err := client.Send(ctx, make([]byte, 1025)); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("oversized frame error = %v", err)
	}
}

func TestUnixTransportRefusesExistingPathAndNonPrivateParent(t *testing.T) {
	transport := NewUnixTransport(64)
	ctx := context.Background()
	privateDir := shortPrivateTempDir(t)
	endpoint := EndpointDescriptor{Kind: EndpointUnix, Address: filepath.Join(privateDir, "already-there")}
	if err := os.WriteFile(endpoint.Address, []byte("do not remove"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := transport.Listen(ctx, endpoint); !errors.Is(err, ErrEndpointInUse) {
		t.Fatalf("existing filesystem path error=%v, want endpoint-in-use", err)
	}
	if got, err := os.ReadFile(endpoint.Address); err != nil || string(got) != "do not remove" {
		t.Fatalf("existing path changed: %q, %v", got, err)
	}
	publicDir := shortPrivateTempDir(t)
	if err := os.Chmod(publicDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := transport.Listen(ctx, EndpointDescriptor{Kind: EndpointUnix, Address: filepath.Join(publicDir, "api.sock")}); err == nil {
		t.Fatal("missing/non-private parent was accepted")
	}
}

func TestUnixTransportCancellationAndStaleSocketCleanup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	transport := NewUnixTransport(64)
	dir := shortPrivateTempDir(t)
	endpoint := EndpointDescriptor{Kind: EndpointUnix, Address: filepath.Join(dir, "s")}
	if _, err := transport.Listen(ctx, endpoint); !errors.Is(err, ErrCanceled) {
		t.Fatalf("canceled listen error = %v", err)
	}
	listener, err := transport.Listen(context.Background(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(endpoint.Address); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned socket path remained after close: %v", err)
	}
}

func shortPrivateTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "b-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
