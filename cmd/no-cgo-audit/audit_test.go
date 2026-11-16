package main

import (
	"context"
	"fmt"
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

func TestAuditTargetAllowsOptionalCgoFallbackExcludedByProductBuild(t *testing.T) {
	project, dependency := makeAuditProject(t)
	writeAuditFile(t, filepath.Join(dependency, "selected_darwin.go"), "//go:build darwin && cgo\n\npackage dep\nimport \"C\"\n")
	writeAuditFile(t, filepath.Join(dependency, "purego_darwin.go"), "//go:build darwin && !cgo\n\npackage dep\nfunc Native() int { return 1 }\n")
	t.Chdir(project)
	if err := AuditTarget(context.Background(), Target{GOOS: "darwin", GOARCH: "arm64"}); err != nil {
		t.Fatalf("AuditTarget() error = %v, want the actual CGO_ENABLED=0 dependency graph audited", err)
	}
}

func TestAuditTargetIgnoresOptionalCgoDependencyFallback(t *testing.T) {
	project, dependency := makeAuditProject(t)
	writeAuditFile(t, filepath.Join(dependency, "selected_darwin.go"), "//go:build darwin && cgo\n\npackage dep\nimport \"C\"\n")
	t.Chdir(project)
	err := AuditTarget(context.Background(), Target{GOOS: "darwin", GOARCH: "arm64"})
	if err != nil {
		t.Fatalf("AuditTarget() error = %v, want optional cgo-only source excluded by cgo-disabled product graph", err)
	}
}

func TestAuditTargetUsesModuleRootWhenCalledFromNestedDirectory(t *testing.T) {
	project, dependency := makeAuditProject(t)
	writeAuditFile(t, filepath.Join(dependency, "selected_darwin.go"), "//go:build darwin && cgo\n\npackage dep\nimport \"C\"\n")
	nested := filepath.Join(project, "internal", "nested")
	writeAuditFile(t, filepath.Join(nested, "nested.go"), "package nested\nfunc Value() int { return 1 }\n")
	t.Chdir(nested)
	err := AuditTarget(context.Background(), Target{GOOS: "darwin", GOARCH: "arm64"})
	if err != nil {
		t.Fatalf("AuditTarget() from nested cwd error = %v, want optional cgo-only source excluded by cgo-disabled product graph", err)
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

func TestMacOSAppTargetBuildsAndPackagesWithCGODisabled(t *testing.T) {
	makefile, err := os.ReadFile(filepath.Join("..", "..", "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	if err := checkMacOSCGORecipes(string(makefile)); err != nil {
		t.Fatal(err)
	}
}

func TestMacOSAppRecipeAuditRejectsCGOOrMissingRecursiveBuildStages(t *testing.T) {
	makefile, err := os.ReadFile(filepath.Join("..", "..", "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	original := string(makefile)
	for _, test := range []struct{ name, before, after string }{
		{"tool cgo", "CGO_ENABLED=0 GOOS=darwin GOARCH=$$(go env GOHOSTARCH) go build", "CGO_ENABLED=1 GOOS=darwin GOARCH=$$(go env GOHOSTARCH) go build"},
		{"arm64 cgo", `CGO_ENABLED=0 "$(MACOS_GOUI)" build`, `CGO_ENABLED=1 "$(MACOS_GOUI)" build`},
		{"amd64 cgo", "CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build", "CGO_ENABLED=1 GOOS=darwin GOARCH=amd64 go build"},
		{"package cgo", `CGO_ENABLED=0 "$(MACOS_GOUI)" package`, `CGO_ENABLED=1 "$(MACOS_GOUI)" package`},
		{"arm64 target", `build -target darwin/arm64 -ldflags`, `build -target darwin/amd64 -ldflags`},
		{"tools dependency", "macos-app-arm64: macos-app-build-tools", "macos-app-arm64:"},
		{"amd64 dependency", "macos-app-compile: macos-app-arm64 macos-app-amd64 macos-app-native", "macos-app-compile: macos-app-arm64 macos-app-native"},
		{"recursive invocation", `$(MAKE) --no-print-directory -j "$(MACOS_BUILD_JOBS)" macos-app-compile`, `echo macos-app-compile`},
		{"universal merge", `lipo -create "$(MACOS_BINARY_ARM64)" "$(MACOS_BINARY_AMD64)"`, `lipo -create "$(MACOS_BINARY_ARM64)"`},
		{"universal install", `install -m 755 "$(MACOS_BINARY)" "$(MACOS_APP_STAGING)/Contents/MacOS/blur"`, `install -m 755 "$(MACOS_BINARY_ARM64)" "$(MACOS_APP_STAGING)/Contents/MacOS/blur"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if !strings.Contains(original, test.before) {
				t.Fatalf("mutation no longer matches actual Make recipe: %q", test.before)
			}
			mutated := strings.Replace(original, test.before, test.after, 1)
			if err := checkMacOSCGORecipes(mutated); err == nil {
				t.Fatal("accepted unsafe/incomplete macOS build mutation")
			}
		})
	}
}

// Follow the actual recursive stage graph rather than requiring the old serial
// recipe to contain every command. The goui CLI is compiled once with cgo off;
// its build and package invocations must also keep cgo disabled.
func checkMacOSCGORecipes(makefile string) error {
	type target struct {
		dependencies string
		recipes      []string
	}
	targets := make(map[string]*target)
	var current *target
	for _, line := range strings.Split(makefile, "\n") {
		if strings.HasPrefix(line, "	") {
			if current != nil {
				current.recipes = append(current.recipes, strings.TrimSpace(line))
			}
			continue
		}
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		current = nil
		name, dependencies, ok := strings.Cut(line, ":")
		if !ok || name == "" || strings.ContainsAny(name, " 	=$") || strings.HasPrefix(dependencies, "=") {
			continue
		}
		current = &target{dependencies: dependencies}
		targets[name] = current
	}
	requireRecipe := func(name, prefix string, fragments ...string) error {
		item := targets[name]
		if item != nil {
			for _, recipe := range item.recipes {
				if !strings.HasPrefix(recipe, prefix) {
					continue
				}
				valid := true
				for _, fragment := range fragments {
					valid = valid && strings.Contains(recipe, fragment)
				}
				if valid {
					return nil
				}
			}
		}
		return fmt.Errorf("macOS target %s is missing its cgo-disabled build/package contract: %s", name, prefix)
	}
	for name, dependencies := range map[string][]string{
		"macos-app-compile": {"macos-app-arm64", "macos-app-amd64", "macos-app-native"},
		"macos-app-arm64":   {"macos-app-build-tools"},
	} {
		item := targets[name]
		if item == nil {
			return fmt.Errorf("missing macOS build target %s", name)
		}
		for _, dependency := range dependencies {
			found := false
			for _, field := range strings.Fields(item.dependencies) {
				found = found || field == dependency
			}
			if !found {
				return fmt.Errorf("macOS target %s does not build dependency %s", name, dependency)
			}
		}
	}
	for _, check := range []struct {
		target, prefix string
		fragments      []string
	}{
		{"macos-app-build-tools", "CGO_ENABLED=0 GOOS=darwin GOARCH=$$(go env GOHOSTARCH) go build ", []string{`-o "$(MACOS_GOUI)"`, "github.com/golang-gui/goui/cmd/goui"}},
		{"macos-app-arm64", `CGO_ENABLED=0 "$(MACOS_GOUI)" build `, []string{"-target darwin/arm64", `-o "$(MACOS_BINARY_ARM64)"`}},
		{"macos-app-amd64", "CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build ", []string{`-o "$(MACOS_BINARY_AMD64)"`, "./cmd/blur"}},
		{"macos-app", `$(MAKE) --no-print-directory -j "$(MACOS_BUILD_JOBS)" macos-app-compile `, nil},
		{"macos-app", `CGO_ENABLED=0 "$(MACOS_GOUI)" package `, []string{"-target darwin/arm64", "-format app", `-o "$(MACOS_APP_STAGING)"`}},
		{"macos-app", `lipo -create "$(MACOS_BINARY_ARM64)" "$(MACOS_BINARY_AMD64)" -output "$(MACOS_BINARY)"`, nil},
		{"macos-app", `install -m 755 "$(MACOS_BINARY)" "$(MACOS_APP_STAGING)/Contents/MacOS/blur"`, nil},
	} {
		if err := requireRecipe(check.target, check.prefix, check.fragments...); err != nil {
			return err
		}
	}
	return nil
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
