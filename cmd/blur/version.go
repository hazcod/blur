package main

import (
	"fmt"
	"io"
	"runtime"
)

// Release metadata, injected at link time:
//
//	-X main.version=... -X main.commit=... -X main.date=...
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// versionRequested reports whether the arguments are exactly a version query.
// It is deliberately strict so it cannot shadow the `mcp` command or any
// future arguments handed over by the OS.
func versionRequested(args []string) bool {
	if len(args) != 2 {
		return false
	}
	switch args[1] {
	case "version", "--version", "-version":
		return true
	}
	return false
}

func versionString() string {
	return fmt.Sprintf("blur %s (commit %s, built %s, %s/%s)", version, commit, date, runtime.GOOS, runtime.GOARCH)
}

func printVersion(w io.Writer) {
	fmt.Fprintln(w, versionString())
}
