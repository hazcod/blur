package main

import (
	"context"
	"encoding/json"
	"io"
	"os"

	"github.com/hazcod/blur/internal/core"
)

// This foundation check is explicitly ephemeral and exits after one document.
func main() {
	client, err := core.NewEphemeralDemoCLIClient()
	if err != nil {
		_, _ = io.WriteString(os.Stderr, "startup failed\n")
		os.Exit(1)
	}
	status, err := client.Status(context.Background())
	if err != nil {
		_, _ = io.WriteString(os.Stderr, "status unavailable\n")
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(status); err != nil {
		_, _ = io.WriteString(os.Stderr, "output failed\n")
		os.Exit(1)
	}
}
