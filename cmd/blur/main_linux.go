//go:build linux && (amd64 || arm64)

package main

import (
	"context"
	"errors"
	"log/slog"
	"os"

	"github.com/hazcod/blur/internal/client"
	platformlinux "github.com/hazcod/blur/internal/platform/linux"
)

func main() {
	if versionRequested(os.Args) {
		printVersion(os.Stdout)
		return
	}
	if err := run(); err != nil {
		if errors.Is(err, client.ErrAlreadyRunning) {
			return
		}
		if errors.Is(err, client.ErrMissingDataKey) {
			slog.Error("Blur found encrypted memory but its Secret Service key is missing; it did not replace the key")
		} else {
			slog.Error("Blur could not open encrypted local memory")
		}
		os.Exit(1)
	}
}

func run() (runErr error) {
	app, err := client.OpenLocalApp(context.Background(), platformlinux.NewSecretStore())
	if err != nil {
		return err
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
