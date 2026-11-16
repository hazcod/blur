package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The integrated checkout must build without a linked worktree. Run the real
// Makefile in a disposable fixture and trap tools so these tests cannot build,
// sign, launch, or replace the user's app.
func TestMacOSRootTargetsUseIntegratedCheckout(t *testing.T) {
	for _, target := range []string{
		"macos-app", "macos-app-stable", "run-macos",
		"macos-app-sandbox-check", "macos-app-safari-unsigned", "macos-app-safari-signed",
	} {
		t.Run(target, func(t *testing.T) {
			root, makePath := macOSRootMakeFixture(t)
			output, err := runMacOSRootMake(t, root, makePath, target, true)
			if err != nil {
				t.Fatalf("make %s requires a removed worktree: %v\n%s", target, err, output)
			}
			if strings.Contains(string(output), ".worktrees") || strings.Contains(string(output), "FORBIDDEN_BUILD_TOOL") {
				t.Fatalf("make %s must use integrated source without executing build tools:\n%s", target, output)
			}
			if target == "macos-app" || target == "macos-app-stable" || target == "run-macos" {
				for _, want := range []string{"macos-app-compile", "lipo -create", "fixture-signing-identity", "macos-app-identity-check"} {
					if !strings.Contains(string(output), want) {
						t.Fatalf("make %s is missing %q:\n%s", target, want, output)
					}
				}
			}
		})
	}
}

func TestMacOSRootBuildFailurePreventsSigningAndLaunching(t *testing.T) {
	for _, target := range []string{"macos-app", "macos-app-stable", "run-macos"} {
		t.Run(target, func(t *testing.T) {
			root, makePath := macOSRootMakeFixture(t)
			output, err := runMacOSRootMake(t, root, makePath, target, false)
			if err == nil {
				t.Fatalf("make %s ignored the failing compiler", target)
			}
			if !strings.Contains(string(output), "FORBIDDEN_BUILD_TOOL:go") || strings.Contains(string(output), "FORBIDDEN_BUILD_TOOL:codesign") || strings.Contains(string(output), "FORBIDDEN_BUILD_TOOL:open") {
				t.Fatalf("make %s must stop before signing or launching after compilation fails:\n%s", target, output)
			}
		})
	}
}

func macOSRootMakeFixture(t *testing.T) (string, string) {
	t.Helper()
	makePath, err := exec.LookPath("make")
	if err != nil {
		t.Skip("make is unavailable")
	}
	contents, err := os.ReadFile(filepath.Join("..", "..", "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	// Exercise quoted paths too, including recursive make arguments.
	root := filepath.Join(t.TempDir(), "checkout with spaces")
	writeAuditFile(t, filepath.Join(root, "Makefile"), string(contents))
	bin := filepath.Join(root, "test-bin")
	for _, tool := range []string{"go", "codesign", "open", "xcodebuild", "xcrun", "node", "python3", "lipo"} {
		path := filepath.Join(bin, tool)
		script := "#!/bin/sh\n"
		if tool == "go" {
			script += "if [ \"$1\" = env ] && [ \"$2\" = GOHOSTARCH ]; then echo arm64; exit 0; fi\n"
		}
		writeAuditFile(t, path, script+"echo FORBIDDEN_BUILD_TOOL:"+tool+" >&2\nexit 97\n")
		if err := os.Chmod(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return root, makePath
}

func runMacOSRootMake(t *testing.T, root, makePath, target string, dryRun bool) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	args := []string{"--no-print-directory", target, "MACOS_CODESIGN_IDENTITY=fixture-signing-identity", "VERSION=fixture", "COMMIT=fixture", "BUILD_DATE=fixture", "XCODE_DEVELOPER_DIR=/fixture"}
	if dryRun {
		args = append(args, "-n")
	}
	cmd := exec.CommandContext(ctx, makePath, args...)
	cmd.Dir = root
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		switch name {
		case "HOME", "PATH", "MAKEFLAGS", "MAKEOVERRIDES", "MFLAGS", "GNUMAKEFLAGS":
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	cmd.Env = append(cmd.Env, "HOME="+root, "PATH="+filepath.Join(root, "test-bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
	return cmd.CombinedOutput()
}
