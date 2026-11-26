//go:build linux && (amd64 || arm64)

package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/hazcod/blur/internal/client"
	"github.com/hazcod/blur/internal/localapi"
	"github.com/hazcod/blur/internal/mcpserver"
	platformlinux "github.com/hazcod/blur/internal/platform/linux"
)

func main() {
	os.Exit(runLinuxMain())
}

func runLinuxMain() int {
	if versionRequested(os.Args) {
		printVersion(os.Stdout)
		return 0
	}
	mcpRequested := len(os.Args) == 2 && os.Args[1] == "mcp"
	// Desktop launchers have no terminal, so keep bounded, user-private
	// diagnostics on disk. Claude owns the bridge's stdio; its errors go there.
	if !stderrIsTerminal() && !mcpRequested {
		closeLog, err := configurePersistentLogging()
		if err != nil {
			slog.Error("persistent log setup failed")
		} else {
			defer closeLog()
		}
	}
	// Claude launches `blur mcp` with stdio attached; it never opens the store.
	if mcpRequested {
		if err := runMCPCommand(); err != nil {
			slog.Error("Blur Claude bridge stopped")
			return 1
		}
		return 0
	}
	if err := run(); err != nil {
		if errors.Is(err, client.ErrAlreadyRunning) {
			return 0
		}
		if errors.Is(err, client.ErrMissingDataKey) {
			slog.Error("Blur found encrypted memory but its Secret Service key is missing; it did not replace the key")
		} else {
			slog.Error("Blur could not open encrypted local memory")
		}
		return 1
	}
	return 0
}

func runMCPCommand() error {
	directory, err := client.LocalDataDirectory()
	if err != nil {
		return err
	}
	endpoint := localapi.EndpointDescriptor{Kind: localapi.EndpointUnix, Address: client.MCPSocketAddress(directory)}
	if endpoint.Validate() != nil {
		return client.ErrNotConfigured
	}
	rpc, err := mcpClientFromSecret(context.Background(), platformlinux.NewSecretStore(), localapi.NewUnixTransport(32<<10), endpoint)
	if err != nil {
		return client.ErrNotConfigured
	}
	defer rpc.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return mcpserver.RunStdio(ctx, rpc)
}

func run() (runErr error) {
	app, err := client.OpenLocalApp(context.Background(), platformlinux.NewSecretStore())
	if err != nil {
		if errors.Is(err, client.ErrAlreadyRunning) {
			return err
		}
		// Match macOS and Windows: explain the storage problem in a window with
		// copyable diagnostics instead of exiting without a visible reason.
		slog.Warn("secure local storage unavailable; opening recovery UI", "result", "setup_required")
		return client.RunDesktopWithSetupIssue(nil, err)
	}
	source := platformlinux.New()
	controller := app.CaptureController(source)
	defer func() {
		_ = controller.Pause(context.Background())
		_ = source.Close()
		if err := app.Close(); runErr == nil && err != nil {
			runErr = err
		}
	}()
	return client.RunDesktop(app.Client, controller)
}
