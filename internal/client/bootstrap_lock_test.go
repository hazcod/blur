package client

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestBootstrapLockSerializesAndHonorsCancellation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Blur")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	release, err := acquireBootstrapLock(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if secondRelease, err := acquireBootstrapLock(ctx, dir); err == nil {
		secondRelease()
		t.Fatal("acquired a contended bootstrap lock after cancellation")
	} else if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contended acquisition error = %v, want context.DeadlineExceeded", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".bootstrap.lock")); err != nil {
		t.Fatalf("lock file is missing: %v", err)
	}
}

func TestBootstrapLockExcludesAnotherProcess(t *testing.T) {
	const helperEnv = "BLUR_TEST_BOOTSTRAP_LOCK_DIR"
	if dir := os.Getenv(helperEnv); dir != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		release, err := acquireBootstrapLock(ctx, dir)
		if err == nil {
			release()
			os.Exit(17)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			os.Exit(0)
		}
		os.Exit(18)
	}
	dir := filepath.Join(t.TempDir(), "Blur")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	release, err := acquireBootstrapLock(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	command := exec.Command(os.Args[0], "-test.run=^TestBootstrapLockExcludesAnotherProcess$")
	command.Env = append(os.Environ(), helperEnv+"="+dir)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("child lock attempt = %v, output %q", err, output)
	}
}
