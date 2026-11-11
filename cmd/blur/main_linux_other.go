//go:build linux && !amd64 && !arm64

package main

import (
	"log/slog"
	"os"
)

func main() {
	slog.Error("Blur desktop is unavailable on this Linux architecture; the native X11 shell currently supports amd64 and arm64")
	os.Exit(1)
}
