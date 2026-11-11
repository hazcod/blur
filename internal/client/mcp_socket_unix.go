//go:build darwin || linux

package client

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"

	"github.com/hazcod/blur/internal/localapi"
)

const (
	mcpSocketDirName  = "blur-mcp"
	mcpSocketFileName = "claude-mcp.sock"
)

// MCPSocketAddress is the local MCP owner socket path shared by the app (the
// listener) and `blur mcp` (the client). AF_UNIX paths are limited to 103
// usable bytes and the App Sandbox container path
// (~/Library/Containers/<id>/Data/Library/Application Support/Blur) is too
// long, so on macOS the socket lives in a per-user directory under
// confstr(_CS_DARWIN_USER_TEMP_DIR), which the sandbox allows and which is
// private to the user. Elsewhere, or if that directory is unknown, it falls
// back to the data directory.
func MCPSocketAddress(dataDir string) string {
	if base := userTempDir(); base != "" {
		address := mcpSocketPathIn(base)
		endpoint := localapi.EndpointDescriptor{Kind: localapi.EndpointUnix, Address: address}
		if endpoint.Validate() == nil {
			return address
		}
	}
	return filepath.Join(dataDir, mcpSocketFileName)
}

func mcpSocketPathIn(base string) string {
	return filepath.Join(filepath.Clean(base), mcpSocketDirName, mcpSocketFileName)
}

// prepareMCPSocketDir makes the socket's parent directory exist as a 0700
// directory owned by the current user, never following a symlink.
func prepareMCPSocketDir(dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !ok || int(stat.Uid) != os.Getuid() {
		return os.ErrPermission
	}
	if info.Mode().Perm() != 0o700 {
		return os.Chmod(dir, 0o700)
	}
	return nil
}
