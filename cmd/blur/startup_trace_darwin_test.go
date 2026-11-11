//go:build darwin

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStartupTraceStoresOnlyPrivateBoundedStages(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "startup.log")
	if err := writeStartupTrace(path, "process_started"); err != nil {
		t.Fatalf("writeStartupTrace() error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("startup trace permissions = %04o, want 0600", got)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(data), " process_started\n") {
		t.Fatalf("startup trace contents = %q", data)
	}
	if err := os.WriteFile(path, make([]byte, maxStartupTraceSize), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeStartupTrace(path, "desktop_ui_returned"); err != nil {
		t.Fatalf("writeStartupTrace() after size bound = %v", err)
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > maxStartupTraceSize {
		t.Fatalf("startup trace size = %d, want <= %d", info.Size(), maxStartupTraceSize)
	}
}

func TestStartupTraceRecordsPrivateSanitizedStartupError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "startup.log")
	err := errors.New("native event loop failed\nwith details")
	if err := writeStartupError(path, err); err != nil {
		t.Fatalf("writeStartupError() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(data), "startup_error=native event loop failed with details\n") {
		t.Fatalf("startup error trace = %q", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("startup trace permissions = %04o, want 0600", got)
	}
}

func TestStartupTraceCanBeDisabledWithoutWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "startup.log")
	if err := traceStartupStage(false, path, "process_started"); err != nil {
		t.Fatalf("disabled trace returned error = %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("disabled startup trace created a file: %v", err)
	}
}

func TestNewStartupTraceReplacesThePreviousRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "startup.log")
	if err := writeStartupTrace(path, "process_returned_error"); err != nil {
		t.Fatal(err)
	}
	if err := writeStartupTrace(path, "process_started"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "process_returned_error") || !strings.HasSuffix(string(data), " process_started\n") {
		t.Fatalf("startup trace still contains an earlier run: %q", data)
	}
}
