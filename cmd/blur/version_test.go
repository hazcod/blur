package main

import (
	"strings"
	"testing"
)

func TestVersionRequested(t *testing.T) {
	for _, args := range [][]string{{"blur", "version"}, {"blur", "--version"}} {
		if !versionRequested(args) {
			t.Fatalf("%v should request the version", args)
		}
	}
	for _, args := range [][]string{{"blur"}, {"blur", "mcp"}, {"blur", "version", "x"}} {
		if versionRequested(args) {
			t.Fatalf("%v should not request the version", args)
		}
	}
}

func TestVersionStringIncludesMetadata(t *testing.T) {
	if got := versionString(); !strings.Contains(got, version) || !strings.Contains(got, commit) {
		t.Fatalf("unexpected version string %q", got)
	}
}
