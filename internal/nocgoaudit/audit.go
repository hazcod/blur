// Package nocgoaudit checks shipped command dependency graphs for cgo use.
package nocgoaudit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

// AuditPackages rejects cgo files and any import of "C" in non-standard
// packages. It scans ignored files too, so build tags do not hide a cgo import.
func AuditPackages(packages []Package, readFile func(string) ([]byte, error)) error {
	for _, pkg := range packages {
		if pkg.Standard {
			continue
		}
		if len(pkg.CgoFiles) != 0 {
			return fmt.Errorf("%s has CgoFiles: %v", pkg.ImportPath, pkg.CgoFiles)
		}

		seen := make(map[string]struct{})
		files := append(append(append([]string{}, pkg.GoFiles...), pkg.CgoFiles...), pkg.IgnoredGoFiles...)
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
			file, err := parser.ParseFile(token.NewFileSet(), path, source, parser.ImportsOnly)
			if err != nil {
				return fmt.Errorf("parse %s in %s: %w", name, pkg.ImportPath, err)
			}
			for _, imported := range file.Imports {
				importPath, err := strconv.Unquote(imported.Path.Value)
				if err != nil {
					return fmt.Errorf("parse import in %s/%s: %w", pkg.ImportPath, name, err)
				}
				if importPath == "C" {
					return fmt.Errorf("%s/%s contains import C", pkg.ImportPath, name)
				}
			}
		}
	}
	return nil
}

// AuditTarget obtains the dependency list with cgo enabled so go list does not
// suppress CgoFiles, then checks all selected and ignored Go source files.
func AuditTarget(ctx context.Context, target Target) error {
	cmd := exec.CommandContext(ctx, "go", "list", "-deps", "-json", "./...")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=1", "GOOS="+target.GOOS, "GOARCH="+target.GOARCH)
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
// cross-build gate.
func AuditSupportedTargets(ctx context.Context) error {
	for _, target := range SupportedTargets {
		if err := AuditTarget(ctx, target); err != nil {
			return err
		}
	}
	return nil
}
