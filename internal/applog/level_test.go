package applog

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

func TestLevelChangesExistingHandlersWithoutReplacingLoggers(t *testing.T) {
	previous := CurrentLevel()
	t.Cleanup(func() { _ = SetLevel(previous) })
	if err := SetLevel("info"); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&output, &slog.HandlerOptions{Level: Level()}))
	logger.Debug("hidden-debug-marker")
	logger.Info("info-marker")
	if strings.Contains(output.String(), "hidden-debug-marker") || !strings.Contains(output.String(), "info-marker") {
		t.Fatal("default info level did not filter debug")
	}
	if err := SetLevel("debug"); err != nil {
		t.Fatal(err)
	}
	logger.Debug("visible-debug-marker")
	if !strings.Contains(output.String(), "visible-debug-marker") {
		t.Fatal("existing handler did not immediately observe debug level")
	}
	output.Reset()
	if err := SetLevel("error"); err != nil {
		t.Fatal(err)
	}
	logger.Info("hidden-info-marker")
	logger.Warn("hidden-warning-marker")
	logger.Error("error-marker")
	if strings.Contains(output.String(), "hidden-") || !strings.Contains(output.String(), "error-marker") {
		t.Fatal("existing handler did not immediately observe error level")
	}
}

func TestLevelValidationIsClosedAndEmptyMeansLegacyInfo(t *testing.T) {
	previous := CurrentLevel()
	t.Cleanup(func() { _ = SetLevel(previous) })
	for _, value := range []string{"debug", "info", "warn", "error", ""} {
		if err := SetLevel(value); err != nil {
			t.Fatal(value, err)
		}
		want := value
		if want == "" {
			want = "info"
		}
		if CurrentLevel() != want {
			t.Fatal("level not normalized", value, CurrentLevel())
		}
	}
	for _, invalid := range []string{"DEBUG", "trace", "warning", " debug", "debug\n", "private-source-text"} {
		if err := SetLevel(invalid); err == nil || CurrentLevel() != "info" || strings.Contains(err.Error(), invalid) {
			t.Fatal("invalid level accepted, applied, or exposed in error")
		}
	}
}

func TestLevelAllowsConcurrentReadsAndUpdates(t *testing.T) {
	previous := CurrentLevel()
	t.Cleanup(func() { _ = SetLevel(previous) })
	var workers sync.WaitGroup
	for _, value := range []string{"debug", "info", "warn", "error"} {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range 100 {
				if err := SetLevel(value); err != nil {
					t.Error(err)
				}
				_ = Level().Level()
				_ = CurrentLevel()
			}
		}()
	}
	workers.Wait()
}
