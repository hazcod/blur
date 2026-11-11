//go:build darwin || linux

package client

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hazcod/blur/internal/localapi"
)

func TestMCPSocketPathFitsAFUnix(t *testing.T) {
	// Typical macOS per-user temp dir, plus a deliberately long variant.
	for _, base := range []string{"/var/folders/zz/abcdefghijklmnopqrstuvwxyz0123456789/T/", "/private/var/folders/zz/abcdefghijklmnopqrstuvwxyz0123456789/T"} {
		address := mcpSocketPathIn(base)
		if len(address) > 103 {
			t.Errorf("%q is %d bytes", address, len(address))
		}
		if (localapi.EndpointDescriptor{Kind: localapi.EndpointUnix, Address: address}).Validate() != nil {
			t.Errorf("%q is not a valid endpoint", address)
		}
	}
}

func TestMCPSocketAddressIsUsableAndShort(t *testing.T) {
	address := MCPSocketAddress(strings.Repeat("/x", 60))
	if len(address) > 103 || !strings.HasSuffix(address, mcpSocketFileName) {
		t.Fatalf("address %q", address)
	}
}

func TestPrepareMCPSocketDir(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, mcpSocketDirName)
	if err := prepareMCPSocketDir(dir); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(dir); info.Mode().Perm() != 0o700 {
		t.Errorf("mode %v", info.Mode().Perm())
	}
	// A pre-existing loose directory owned by us is tightened.
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := prepareMCPSocketDir(dir); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(dir); info.Mode().Perm() != 0o700 {
		t.Errorf("mode not tightened: %v", info.Mode().Perm())
	}
	// A symlink in its place is refused.
	link := filepath.Join(root, "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if err := prepareMCPSocketDir(link); err == nil {
		t.Error("symlinked socket directory must be rejected")
	}
}

func TestMCPSocketAddressUsesUserTempDir(t *testing.T) {
	base := userTempDir()
	if base == "" {
		t.Skip("no per-user temp dir on this platform")
	}
	if got, want := MCPSocketAddress("/unused"), mcpSocketPathIn(base); got != want {
		t.Errorf("got %q want %q", got, want)
	}
}
