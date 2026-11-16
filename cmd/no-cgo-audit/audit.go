// Package main checks repository source and shipped dependency graphs for cgo use.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Package is the subset of go list output needed by the audit.
type Package struct {
	ImportPath     string
	Standard       bool
	Dir            string
	GoFiles        []string
	CgoFiles       []string
	IgnoredGoFiles []string
}

// Target identifies a target whose shipped command dependency graph is audited.
type Target struct {
	GOOS   string
	GOARCH string
}

// SupportedTargets includes the command build matrix maintained by Makefile.
var SupportedTargets = []Target{
	{GOOS: "darwin", GOARCH: "arm64"},
	{GOOS: "darwin", GOARCH: "amd64"},
	{GOOS: "windows", GOARCH: "amd64"},
	{GOOS: "linux", GOARCH: "amd64"},
	{GOOS: "linux", GOARCH: "arm64"},
}

// AuditPackages rejects cgo files and any import of "C" in selected
// non-standard package sources. IgnoredGoFiles are intentionally excluded:
// external dependency files ignored on every shipped target are not shipped.
func AuditPackages(packages []Package, readFile func(string) ([]byte, error)) error {
	for _, pkg := range packages {
		if pkg.Standard {
			continue
		}
		if len(pkg.CgoFiles) != 0 {
			return fmt.Errorf("%s has CgoFiles: %v", pkg.ImportPath, pkg.CgoFiles)
		}

		seen := make(map[string]struct{})
		files := append(append([]string{}, pkg.GoFiles...), pkg.CgoFiles...)
		for _, name := range files {
			path := filepath.Join(pkg.Dir, name)
			if _, exists := seen[path]; exists {
				continue
			}
			seen[path] = struct{}{}

			source, err := readFile(path)
			if err != nil {
				return fmt.Errorf("read %s in %s: %w", name, pkg.ImportPath, err)
			}
			if err := rejectImportC(path, source); err != nil {
				return fmt.Errorf("%s/%s: %w", pkg.ImportPath, name, err)
			}
		}
	}
	return nil
}

// AuditRepository checks every Go source file owned by the current repository,
// regardless of build tags. Vendor trees are dependency sources and are
// checked through the selected per-target package graph instead.
func AuditRepository(root string, readFile func(string) ([]byte, error)) error {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve repository root: %w", err)
	}
	return filepath.WalkDir(absolute, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != absolute && (entry.Name() == ".git" || entry.Name() == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(entry.Name()) != ".go" {
			return nil
		}
		source, err := readFile(path)
		if err != nil {
			return fmt.Errorf("read repository source %s: %w", path, err)
		}
		if err := rejectImportC(path, source); err != nil {
			return fmt.Errorf("repository source %s: %w", path, err)
		}
		return nil
	})
}

func rejectImportC(path string, source []byte) error {
	file, err := parser.ParseFile(token.NewFileSet(), path, source, parser.ImportsOnly)
	if err != nil {
		return fmt.Errorf("parse source: %w", err)
	}
	for _, imported := range file.Imports {
		importPath, err := strconv.Unquote(imported.Path.Value)
		if err != nil {
			return fmt.Errorf("parse import: %w", err)
		}
		if importPath == "C" {
			return errors.New("contains import C")
		}
	}
	return nil
}

func currentModuleRoot(ctx context.Context, target Target) (string, error) {
	cmd := exec.CommandContext(ctx, "go", "env", "GOMOD")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+target.GOOS, "GOARCH="+target.GOARCH)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("go env GOMOD for %s/%s: %w", target.GOOS, target.GOARCH, err)
	}
	modFile := strings.TrimSpace(string(output))
	if modFile == "" || modFile == os.DevNull {
		return "", errors.New("go env GOMOD returned no repository module")
	}
	return filepath.Dir(modFile), nil
}

// AuditTarget obtains the dependency list with cgo disabled, matching shipped
// build policy. It scans all repository Go source, then only target-selected
// GoFiles in non-standard dependencies. Optional cgo-only fallbacks in unused
// dependency source are not part of a cgo-disabled product binary.
func AuditTarget(ctx context.Context, target Target) error {
	root, err := currentModuleRoot(ctx, target)
	if err != nil {
		return err
	}
	if err := AuditRepository(root, os.ReadFile); err != nil {
		return fmt.Errorf("%s/%s repository: %w", target.GOOS, target.GOARCH, err)
	}

	cmd := exec.CommandContext(ctx, "go", "list", "-deps", "-json", "./...")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+target.GOOS, "GOARCH="+target.GOARCH)
	var output bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go list for %s/%s: %w: %s", target.GOOS, target.GOARCH, err, stderr.String())
	}

	decoder := json.NewDecoder(&output)
	var packages []Package
	for {
		var pkg Package
		if err := decoder.Decode(&pkg); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return fmt.Errorf("decode go list output for %s/%s: %w", target.GOOS, target.GOARCH, err)
		}
		packages = append(packages, pkg)
	}
	if err := AuditPackages(packages, os.ReadFile); err != nil {
		return fmt.Errorf("%s/%s: %w", target.GOOS, target.GOARCH, err)
	}
	return nil
}

// AuditSupportedTargets checks every GOOS/GOARCH pair used by the command
// cross-build gate. Any new shipped target must be added both here and to the
// CGO_ENABLED=0 physical/command build matrices in the Makefile.
func AuditSupportedTargets(ctx context.Context) error {
	for _, target := range SupportedTargets {
		if err := AuditTarget(ctx, target); err != nil {
			return err
		}
	}
	return nil
}
