package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/nielsgroen/blur/internal/diagnostics"
	"github.com/nielsgroen/blur/internal/knowledge"
	"github.com/nielsgroen/blur/internal/memory"
	"github.com/nielsgroen/blur/internal/securestore"
	"github.com/nielsgroen/blur/internal/storage"
	"github.com/nielsgroen/blur/internal/storage/durable"
)

func parseFixture(args []string) (command, path string, ok bool) {
	if len(args) != 5 || args[0] != "fixture" || (args[1] != "doctor" && args[1] != "today" && args[1] != "knowledge") || args[2] != "--fixture" || args[3] != "--store" || args[4] == "" {
		return "", "", false
	}
	path, err := filepath.Abs(filepath.Clean(args[4]))
	if err != nil {
		return "", "", false
	}
	temp, err := filepath.Abs(os.TempDir())
	if err != nil {
		return "", "", false
	}
	base := filepath.Base(path)
	if len(base) < 17 || base[:13] != "blur-fixture-" || filepath.Ext(base) != ".db" {
		return "", "", false
	}
	resolvedParent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return "", "", false
	}
	if resolvedTemp, resolveErr := filepath.EvalSymlinks(temp); resolveErr == nil {
		temp = resolvedTemp
	}
	rel, err := filepath.Rel(temp, resolvedParent)
	if err != nil || rel == ".." || filepath.IsAbs(rel) || (len(rel) >= 3 && rel[:3] == ".."+string(filepath.Separator)) {
		return "", "", false
	}
	resolvedRel, err := filepath.Rel(temp, resolvedParent)
	if err != nil || resolvedRel == ".." || filepath.IsAbs(resolvedRel) || (len(resolvedRel) >= 3 && resolvedRel[:3] == ".."+string(filepath.Separator)) {
		return "", "", false
	}
	return args[1], path, true
}

// runFixture is deliberately read-only. Its public, fixed key is only for
// synthetic offline fixtures and cannot open a store protected by an OS key.
func runFixture(args []string, out, errOut io.Writer) int {
	command, path, ok := parseFixture(args)
	if !ok || out == nil || errOut == nil {
		if errOut != nil {
			_, _ = io.WriteString(errOut, "invalid fixture command\n")
		}
		return 2
	}
	store, err := durable.OpenDurable(path, securestore.OfflineFixtureKey(), durable.OpenExisting)
	if err != nil {
		_, _ = io.WriteString(errOut, "fixture store unavailable\n")
		return 1
	}
	defer store.Close()
	ctx := context.Background()
	var value any
	switch command {
	case "doctor":
		rows, readErr := store.ListDerivations(ctx, 256, "")
		if readErr != nil {
			_, _ = io.WriteString(errOut, "doctor unavailable\n")
			return 1
		}
		stale, quarantined := 0, 0
		for _, row := range rows {
			if row.State == storage.Stale {
				stale++
			}
			if row.State == storage.Quarantined {
				quarantined++
			}
		}
		date := time.Now().In(time.Local).Format("2006-01-02")
		_, found, readErr := store.GetTodaySnapshot(ctx, date)
		if readErr != nil {
			_, _ = io.WriteString(errOut, "doctor unavailable\n")
			return 1
		}
		freshness := diagnostics.Missing
		if found {
			freshness = diagnostics.Fresh
		}
		value = diagnostics.Build(diagnostics.Inputs{StoreVersion: durable.OpenSchemaVersion, Integrity: diagnostics.Healthy, Queue: memory.Stats{}, StaleDerivations: stale, QuarantinedDerivations: quarantined, DerivationsSampled: len(rows), DerivationCountsCapped: len(rows) == 256, Today: freshness, Capabilities: map[string]diagnostics.CapabilityState{"capture": diagnostics.Unavailable, "ai": diagnostics.Disabled}})
	case "today":
		date := time.Now().In(time.Local).Format("2006-01-02")
		snapshot, found, readErr := store.GetTodaySnapshot(ctx, date)
		if readErr != nil {
			_, _ = io.WriteString(errOut, "today unavailable\n")
			return 1
		}
		if !found {
			_, _ = io.WriteString(errOut, "no today snapshot\n")
			return 1
		}
		value = snapshot
	case "knowledge":
		page, readErr := store.ListKnowledge(ctx, knowledge.PageRequest{Limit: 50})
		if readErr != nil {
			_, _ = io.WriteString(errOut, "knowledge unavailable\n")
			return 1
		}
		value = page
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > 1<<20 {
		_, _ = io.WriteString(errOut, "fixture output unavailable\n")
		return 1
	}
	encoded = append(encoded, '\n')
	if _, err := out.Write(encoded); err != nil {
		_, _ = io.WriteString(errOut, "output failed\n")
		return 1
	}
	return 0
}

func fixtureMain(args []string) int { return runFixture(args, os.Stdout, os.Stderr) }
