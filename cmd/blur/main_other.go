//go:build !darwin && !windows && !linux

package main

import (
	"log/slog"
	"os"
)

func main() {
	slog.Error("Blur desktop is not available on this operating system yet")
	os.Exit(1)
}
