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

// Building from the root checkout must never compile its older storage reader.
// Run the real Makefile against a disposable worktree and trap build tools so
// a regression cannot build, sign, launch, or replace the user's app.
func TestMacOSRootTargetsUseActiveWorktree(t *testing.T) {
	for _, target := range []string{
		"macos-app", "macos-app-stable", "run-macos",
		"macos-app-sandbox-check", "macos-app-safari-unsigned", "macos-app-safari-signed",
	} {
		t.Run(target, func(t *testing.T) {
			root, makePath := macOSRootMakeFixture(t, true)
			output, err := runMacOSRootMake(t, root, makePath, target)
			if err != nil {
				t.Fatalf("make %s did not delegate to the active worktree: %v\n%s", target, err, output)
			}
			got, err := os.ReadFile(filepath.Join(root, ".worktrees", "durable-memory-core", "invocation"))
			if err != nil {
				t.Fatalf("active worktree was not invoked: %v\n%s", err, output)
			}
			wantTarget := target
			if target == "macos-app" {
				wantTarget = "macos-app-stable"
			}
			want := wantTarget + "\nfixture-signing-identity\n"
			if string(got) != want {
				t.Fatalf("active invocation = %q, want %q", got, want)
			}
		})
	}
}

func TestMacOSRootBuildRefusesMissingActiveWorktree(t *testing.T) {
	for _, target := range []string{"macos-app", "macos-app-stable"} {
		t.Run(target, func(t *testing.T) {
			root, makePath := macOSRootMakeFixture(t, false)
			output, err := runMacOSRootMake(t, root, makePath, target)
			if err == nil {
				t.Fatalf("make %s accepted a missing active worktree", target)
			}
			if !strings.Contains(string(output), "active macOS worktree is unavailable") || strings.Contains(string(output), "FORBIDDEN_BUILD_TOOL") {
				t.Fatalf("make %s must explain the missing worktree before invoking build tools:\n%s", target, output)
			}
		})
	}
}

func macOSRootMakeFixture(t *testing.T, active bool) (string, string) {
	t.Helper()
	makePath, err := exec.LookPath("make")
	if err != nil {
		t.Skip("make is unavailable")
	}
	contents, err := os.ReadFile(filepath.Join("..", "..", "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	// Exercise quoted paths too, including the recursive make -C argument.
	root := filepath.Join(t.TempDir(), "checkout with spaces")
	writeAuditFile(t, filepath.Join(root, "Makefile"), string(contents))
	bin := filepath.Join(root, "test-bin")
	for _, tool := range []string{"go", "codesign", "open", "xcodebuild", "xcrun", "node"} {
		path := filepath.Join(bin, tool)
		writeAuditFile(t, path, "#!/bin/sh\necho FORBIDDEN_BUILD_TOOL >&2\nexit 97\n")
		if err := os.Chmod(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if active {
		writeAuditFile(t, filepath.Join(root, ".worktrees", "durable-memory-core", "Makefile"), `.PHONY: macos-app-stable run-macos macos-app-sandbox-check macos-app-safari-unsigned macos-app-safari-signed safari-extension-build safari-extension-test
macos-app-stable run-macos macos-app-sandbox-check macos-app-safari-unsigned macos-app-safari-signed safari-extension-build safari-extension-test:
	@printf '%s\n' '$@' '$(MACOS_CODESIGN_IDENTITY)' > invocation
`)
	}
	return root, makePath
}

func runMacOSRootMake(t *testing.T, root, makePath, target string) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, makePath, "--no-print-directory", target, "MACOS_CODESIGN_IDENTITY=fixture-signing-identity")
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
