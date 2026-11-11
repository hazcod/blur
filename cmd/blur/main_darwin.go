package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"syscall"

	"github.com/hazcod/blur/internal/client"
	"github.com/hazcod/blur/internal/ingest"
	"github.com/hazcod/blur/internal/localapi"
	"github.com/hazcod/blur/internal/mcpserver"
	"github.com/hazcod/blur/internal/platform"
	"github.com/hazcod/blur/internal/platform/darwin"
)

func main() {
	os.Exit(runMain())
}

func runMain() (exitCode int) {
	if versionRequested(os.Args) {
		printVersion(os.Stdout)
		return 0
	}
	debug.SetPanicOnFault(true)
	closeLog, logErr := configurePersistentLogging()
	if logErr != nil {
		slog.Error("persistent log setup failed", "error", logErr)
	} else {
		defer closeLog()
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Error("unhandled main-thread panic", "panic_type", fmt.Sprintf("%T", recovered), "stack", string(debug.Stack()))
			exitCode = 2
		}
	}()
	if len(os.Args) == 2 && os.Args[1] == "mcp" {
		if err := runMCPCommand(); err != nil {
			// MCP uses stdout for JSON-RPC; keep diagnostics fixed and on stderr.
			slog.Error("Blur Claude bridge stopped")
			return 1
		}
		return 0
	}
	startupTraceEnabled = true
	traceLaunchStage("process_started")
	if err := run(); err != nil {
		if errors.Is(err, client.ErrAlreadyRunning) {
			traceLaunchStage("already_running")
			// The prior instance still holds the lock after the bounded wait;
			// report failure rather than exiting as if the launch succeeded.
			slog.Error("Blur could not start: another instance still holds the local store")
			return 1
		}
		traceLaunchStage("process_returned_error")
		if logDir, logErr := blurLogsDirectory(); logErr == nil {
			_ = writeStartupError(filepath.Join(logDir, "startup.log"), err)
		}
		slog.Error(startupFailureMessage(err))
		return 1
	}
	traceLaunchStage("process_returned_ok")
	return 0
}

func runMCPCommand() error {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return client.ErrNotConfigured
	}
	endpoint := localapi.EndpointDescriptor{Kind: localapi.EndpointUnix, Address: client.MCPSocketAddress(filepath.Join(configDir, "Blur"))}
	if endpoint.Validate() != nil {
		return client.ErrNotConfigured
	}
	secrets := darwin.NewSecretStore()
	rpc, err := mcpClientFromSecret(context.Background(), secrets, localapi.NewUnixTransport(32<<10), endpoint)
	if err != nil {
		return client.ErrNotConfigured
	}
	defer rpc.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return mcpserver.RunStdio(ctx, rpc)
}

var errDesktopRuntime = errors.New("macos client: desktop runtime stopped")

func startupFailureMessage(err error) string {
	switch {
	case errors.Is(err, client.ErrMissingDataKey):
		return "Blur found encrypted memory but its Keychain key is missing; it did not replace the key"
	case errors.Is(err, errDesktopRuntime):
		return "Blur could not continue running its desktop interface"
	default:
		return "Blur could not open or close encrypted local memory"
	}
}

func run() (runErr error) {
	traceLaunchStage("store_open_started")
	app, err := client.OpenLocalApp(context.Background(), darwin.NewSecretStore())
	if err != nil {
		traceLaunchStage("store_open_failed")
	} else {
		traceLaunchStage("store_open_ok")
	}
	return launch(app, err, client.RunDesktop, func(issue error) error {
		return client.RunDesktopWithSetupIssue(nil, issue)
	})
}

func launch(app *client.LocalApp, openErr error, desktop func(*client.Client, ...*ingest.Controller) error, setupDesktop ...func(error) error) (runErr error) {
	if errors.Is(openErr, client.ErrAlreadyRunning) {
		return openErr
	}
	if openErr != nil {
		var secretErr *platform.SecretStoreError
		if errors.As(openErr, &secretErr) && secretErr != nil {
			slog.Warn("Blur could not open encrypted local memory; starting without capture or storage", "keychain_state", secretErr.State, "keychain_operation", safeKeychainOperation(secretErr.Operation), "keychain_osstatus", secretErr.BackendCode)
		} else {
			slog.Warn("Blur could not open encrypted local memory; starting without capture or storage")
		}
		if len(setupDesktop) > 0 && setupDesktop[0] != nil {
			traceLaunchStage("setup_ui_started")
			if err := setupDesktop[0](openErr); err != nil {
				return errors.Join(errDesktopRuntime, err)
			}
			traceLaunchStage("setup_ui_returned")
			return nil
		}
		traceLaunchStage("setup_ui_started")
		if err := desktop(nil); err != nil {
			return errors.Join(errDesktopRuntime, err)
		}
		traceLaunchStage("setup_ui_returned")
		return nil
	}
	if app == nil || desktop == nil {
		return client.ErrNotConfigured
	}
	source := darwin.New()
	controller := app.CaptureController(source)
	defer func() {
		_ = controller.Pause(context.Background())
		_ = source.Close()
		if err := app.Close(); runErr == nil && err != nil {
			runErr = err
		}
	}()
	traceLaunchStage("desktop_ui_started")
	if err := desktop(app.Client, controller); err != nil {
		return errors.Join(errDesktopRuntime, err)
	}
	traceLaunchStage("desktop_ui_returned")
	return nil
}

func safeKeychainOperation(operation string) string {
	switch operation {
	case "read", "create", "verify":
		return operation
	default:
		return "unknown"
	}
}
