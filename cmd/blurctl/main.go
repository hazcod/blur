package main

import (
	"context"
	"encoding/json"
	"io"
	"os"

	"github.com/nielsgroen/blur/internal/core"
	"github.com/nielsgroen/blur/internal/platform"
)

type client interface {
	Status(context.Context) (core.StatusReport, error)
	Capabilities(context.Context) ([]platform.CapabilityReport, error)
}

func validCommand(args []string) bool {
	return len(args) == 1 && (args[0] == "status" || args[0] == "capabilities")
}

// run is the in-process foundation command boundary. No endpoint is opened.
func run(args []string, out, errOut io.Writer, c client) int {
	if !validCommand(args) {
		_, _ = io.WriteString(errOut, "invalid command\n")
		return 2
	}
	if c == nil || out == nil || errOut == nil {
		return 1
	}
	var value any
	var err error
	switch args[0] {
	case "status":
		value, err = c.Status(context.Background())
	case "capabilities":
		value, err = c.Capabilities(context.Background())
	}
	if err != nil {
		_, _ = io.WriteString(errOut, "request failed\n")
		return 1
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded)+1 > 65536 {
		_, _ = io.WriteString(errOut, "response unavailable\n")
		return 1
	}
	encoded = append(encoded, '\n')
	if _, err := out.Write(encoded); err != nil {
		_, _ = io.WriteString(errOut, "output failed\n")
		return 1
	}
	return 0
}

// launch validates the operation before creating the ephemeral CLI grant.
func launch(args []string, out, errOut io.Writer, factory func() (client, error)) int {
	if !validCommand(args) {
		return run(args, out, errOut, nil)
	}
	c, err := factory()
	if err != nil {
		_, _ = io.WriteString(errOut, "startup failed\n")
		return 1
	}
	return run(args, out, errOut, c)
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "fixture" {
		os.Exit(fixtureMain(os.Args[1:]))
	}
	os.Exit(launch(os.Args[1:], os.Stdout, os.Stderr, func() (client, error) { return core.NewEphemeralDemoCLIClient() }))
}
