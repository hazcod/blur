package nocgoaudit

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuditPackagesRejectsCgoFilesFromEnabledGraph(t *testing.T) {
	err := AuditPackages([]Package{{
		ImportPath: "example.org/driver", Dir: "/deps/driver",
		CgoFiles: []string{"bridge.go"},
	}}, func(path string) ([]byte, error) {
		return nil, nil
	})
	if err == nil || !strings.Contains(err.Error(), "example.org/driver") || !strings.Contains(err.Error(), "CgoFiles") {
		t.Fatalf("AuditPackages() error = %v, want CgoFiles rejection for dependency", err)
	}
}

func TestAuditPackagesRejectsGroupedAndAliasedImportC(t *testing.T) {
	for _, source := range []string{
		"package probe\nimport (\n\t\"fmt\"\n\t\"C\"\n)\n",
		"package probe\nimport (\n\tc \"C\"\n)\n",
	} {
		err := AuditPackages([]Package{{
			ImportPath: "example.org/probe", Dir: "/deps/probe",
			GoFiles: []string{"probe.go"},
		}}, func(string) ([]byte, error) { return []byte(source), nil })
		if err == nil || !strings.Contains(err.Error(), "example.org/probe") || !strings.Contains(err.Error(), "import C") {
			t.Fatalf("AuditPackages() error = %v, want import C rejection for source %q", err, source)
		}
	}
}

func TestAuditPackagesRejectsSelectedSupportedTargetImportC(t *testing.T) {
	err := AuditPackages([]Package{{
		ImportPath: "example.org/driver", Dir: "/deps/driver",
		GoFiles:        []string{"selected_darwin.go"},
		IgnoredGoFiles: []string{"ignored_hurd.go"},
	}}, func(path string) ([]byte, error) {
		if strings.HasSuffix(path, "selected_darwin.go") {
			return []byte("//go:build darwin && cgo\npackage driver\nimport \"C\"\n"), nil
		}
		return []byte("//go:build hurd && cgo\npackage driver\nimport \"C\"\n"), nil
	})
	if err == nil || !strings.Contains(err.Error(), "example.org/driver/selected_darwin.go") || !strings.Contains(err.Error(), "import C") {
		t.Fatalf("AuditPackages() error = %v, want selected supported-target import C rejection", err)
	}
}

func TestAuditPackagesAllowsUnsupportedTargetIgnoredImportC(t *testing.T) {
	err := AuditPackages([]Package{{
		ImportPath: "example.org/optional", Dir: "/deps/optional",
		GoFiles:        []string{"portable.go"},
		IgnoredGoFiles: []string{"optional_hurd.go"},
	}}, func(path string) ([]byte, error) {
		if strings.HasSuffix(path, "optional_hurd.go") {
			return []byte("//go:build hurd && cgo\npackage optional\nimport \"C\"\n"), nil
		}
		return []byte("package optional\nfunc Value() int { return 1 }\n"), nil
	})
	if err != nil {
		t.Fatalf("AuditPackages() error = %v, want ignored unsupported-target dependency source to be outside the shipped graph", err)
	}
}

func TestAuditTargetAllowsIgnoredCImportInExternalDependency(t *testing.T) {
	project, dependency := makeAuditProject(t)
	writeAuditFile(t, filepath.Join(dependency, "ignored_hurd.go"), "//go:build hurd && cgo\n\npackage dep\n/* int audit_hurd_only(void); */\nimport \"C\"\n")
	t.Chdir(project)
	if err := AuditTarget(context.Background(), Target{GOOS: "darwin", GOARCH: "arm64"}); err != nil {
		t.Fatalf("AuditTarget() error = %v, want ignored unsupported-target dependency source to be allowed", err)
	}
}

func TestAuditTargetRejectsCImportSelectedForSupportedTarget(t *testing.T) {
	project, dependency := makeAuditProject(t)
	writeAuditFile(t, filepath.Join(dependency, "selected_darwin.go"), "//go:build darwin && cgo\n\npackage dep\nimport \"C\"\n")
	t.Chdir(project)
	err := AuditTarget(context.Background(), Target{GOOS: "darwin", GOARCH: "arm64"})
	if err == nil || !strings.Contains(err.Error(), "example.test/dep") || !strings.Contains(err.Error(), "CgoFiles") {
		t.Fatalf("AuditTarget() error = %v, want selected supported-target dependency CgoFiles rejection", err)
	}
}

func TestAuditTargetUsesModuleRootWhenCalledFromNestedDirectory(t *testing.T) {
	project, dependency := makeAuditProject(t)
	writeAuditFile(t, filepath.Join(dependency, "selected_darwin.go"), "//go:build darwin && cgo\n\npackage dep\nimport \"C\"\n")
	nested := filepath.Join(project, "internal", "nested")
	writeAuditFile(t, filepath.Join(nested, "nested.go"), "package nested\nfunc Value() int { return 1 }\n")
	t.Chdir(nested)
	err := AuditTarget(context.Background(), Target{GOOS: "darwin", GOARCH: "arm64"})
	if err == nil || !strings.Contains(err.Error(), "example.test/dep") || !strings.Contains(err.Error(), "CgoFiles") {
		t.Fatalf("AuditTarget() from nested cwd error = %v, want root dependency selected by go list to be rejected", err)
	}
}

func TestAuditTargetStillRejectsIgnoredCImportInBlurSource(t *testing.T) {
	project, _ := makeAuditProject(t)
	writeAuditFile(t, filepath.Join(project, "unsupported", "unsupported_hurd.go"), "//go:build hurd && cgo\n\npackage unsupported\nimport \"C\"\n")
	t.Chdir(project)
	err := AuditTarget(context.Background(), Target{GOOS: "darwin", GOARCH: "arm64"})
	if err == nil || !strings.Contains(err.Error(), "unsupported_hurd.go") || !strings.Contains(err.Error(), "import C") {
		t.Fatalf("AuditTarget() error = %v, want repository-wide ignored-source import C rejection", err)
	}
}

func makeAuditProject(t *testing.T) (project, dependency string) {
	t.Helper()
	base := t.TempDir()
	project = filepath.Join(base, "project")
	dependency = filepath.Join(base, "dependency")
	writeAuditFile(t, filepath.Join(project, "go.mod"), "module example.test/root\n\ngo 1.27.0\n\nrequire example.test/dep v0.0.0\nreplace example.test/dep => ../dependency\n")
	writeAuditFile(t, filepath.Join(project, "root.go"), "package root\nimport _ \"example.test/dep\"\n")
	writeAuditFile(t, filepath.Join(dependency, "go.mod"), "module example.test/dep\n\ngo 1.27.0\n")
	writeAuditFile(t, filepath.Join(dependency, "dep.go"), "package dep\nfunc Value() int { return 1 }\n")
	return project, dependency
}

func writeAuditFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAuditPackagesIgnoresUnselectedDependencySourceAndExemptsStandardLibrary(t *testing.T) {
	err := AuditPackages([]Package{
		{
			ImportPath: "example.org/optional", Dir: "/deps/optional",
			IgnoredGoFiles: []string{"optional.go"},
		},
		{
			ImportPath: "runtime/cgo", Standard: true, Dir: "/go/src/runtime/cgo",
			CgoFiles: []string{"gcc.go"}, GoFiles: []string{"runtime.go"},
		},
	}, func(path string) ([]byte, error) {
		if strings.HasSuffix(path, "optional.go") {
			return []byte("package optional\nimport (\n\t\"C\"\n)\n"), nil
		}
		return []byte("package cgo\nimport \"C\"\n"), nil
	})
	if err != nil {
		t.Fatalf("AuditPackages() error = %v, want ignored dependency source to be outside the selected graph", err)
	}
}

func TestAuditPackagesAllowsPureGoThirdPartyDependency(t *testing.T) {
	err := AuditPackages([]Package{{
		ImportPath: "example.org/pure", Dir: "/deps/pure",
		GoFiles: []string{"pure.go"},
	}}, func(string) ([]byte, error) { return []byte("package pure\n"), nil })
	if err != nil {
		t.Fatalf("AuditPackages() error = %v, want nil", err)
	}
}

func TestAuditPackagesExemptsStandardLibraryCgo(t *testing.T) {
	err := AuditPackages([]Package{{
		ImportPath: "runtime/cgo", Standard: true, Dir: "/go/src/runtime/cgo",
		CgoFiles: []string{"gcc.go"}, GoFiles: []string{"runtime.go"},
	}}, func(string) ([]byte, error) { return []byte("package cgo\nimport \"C\"\n"), nil })
	if err != nil {
		t.Fatalf("AuditPackages() error = %v, want standard library exemption", err)
	}
}
