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
	platformwindows "github.com/hazcod/blur/internal/platform/windows"
	"golang.org/x/sys/windows"
)

// mcpOnlyBinary is set on the console-subsystem companion shipped with the
// Windows GUI. Its stdio remains inherited by Claude, and it cannot start a
// second desktop process if invoked without the explicit MCP command.
var mcpOnlyBinary string

func main() {
	os.Exit(runWindowsMain())
}

func runWindowsMain() (exitCode int) {
	if versionRequested(os.Args) {
		printVersion(os.Stdout)
		return 0
	}
	if mcpOnlyBinary != "true" {
		closeLog, err := configurePersistentLogging()
		if err != nil {
			slog.Error("persistent log setup failed")
			showWindowsStartupError("Blur couldn’t create its diagnostic logs, so it stopped without starting capture. Check your Windows user profile permissions and try again.")
			return 2
		}
		defer closeLog()
	}
	if !mcpLaunchAllowed(mcpOnlyBinary == "true", os.Args) {
		slog.Error("MCP companion requires the explicit mcp argument")
		return 2
	}
	if len(os.Args) == 2 && os.Args[1] == "mcp" {
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
			slog.Error("Blur found encrypted memory but its OS key is missing; it did not replace the key")
		} else {
			slog.Error("Blur could not open encrypted local memory")
		}
		return 1
	}
	return 0
}

func showWindowsStartupError(message string) {
	text, textErr := windows.UTF16PtrFromString(message)
	title, titleErr := windows.UTF16PtrFromString("Blur could not start")
	if textErr == nil && titleErr == nil {
		_, _ = windows.MessageBox(0, text, title, windows.MB_OK|windows.MB_ICONERROR)
	}
}

func runMCPCommand() error {
	endpoint, err := localapi.CurrentUserNamedPipeEndpoint()
	if err != nil || endpoint.Validate() != nil {
		return client.ErrNotConfigured
	}
	secrets := platformwindows.NewSecretStore()
	rpc, err := mcpClientFromSecret(context.Background(), secrets, localapi.NewNamedPipeTransport(32<<10), endpoint)
	if err != nil {
		return client.ErrNotConfigured
	}
	defer rpc.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return mcpserver.RunStdio(ctx, rpc)
}

func run() (runErr error) {
	app, err := client.OpenLocalApp(context.Background(), platformwindows.NewSecretStore())
	if err != nil {
		if errors.Is(err, client.ErrAlreadyRunning) {
			return err
		}
		slog.Warn("secure local storage unavailable; opening recovery UI", "result", "setup_required")
		return client.RunDesktopWithSetupIssue(nil, err)
	}
	source := platformwindows.New()
	controller := app.CaptureController(source)
	defer func() {
		if controller != nil {
			_ = controller.Pause(context.Background())
		}
		if err := app.Close(); runErr == nil && err != nil {
			runErr = err
		}
	}()
	if err := client.RunDesktop(app.Client, controller); err != nil {
		return err
	}
	return nil
}
