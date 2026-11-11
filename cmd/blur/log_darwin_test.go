package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

func TestOpenPersistentLogRejectsSymlink(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "Blur", "Logs")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "outside.log")
	if err := os.WriteFile(target, []byte("private target"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "blur.log")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}

	writer, err := openPersistentLog(directory, path)
	if err == nil {
		writer.Close()
		t.Fatal("openPersistentLog followed a symlink to a file outside the log directory")
	}
	data, readErr := os.ReadFile(target)
	if readErr != nil || string(data) != "private target" {
		t.Fatalf("symlink target changed: data=%q err=%v", data, readErr)
	}
}

func TestPersistentLoggingCapturesRuntimeStderrDirectly(t *testing.T) {
	if directory := os.Getenv("BLUR_LOG_TEST_HELPER_DIR"); directory != "" {
		persistentLogDirectory = directory
		closeLog, err := configurePersistentLogging()
		if err != nil {
			os.Exit(3)
		}
		_ = closeLog
		var stderr syscall.Stat_t
		if err := syscall.Fstat(2, &stderr); err != nil {
			os.Exit(4)
		}
		info, err := os.Stat(filepath.Join(directory, "blur.log"))
		if err != nil {
			os.Exit(5)
		}
		logFile, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stderr.Dev != logFile.Dev || stderr.Ino != logFile.Ino {
			os.Exit(6)
		}
		_, _ = os.Stderr.Write([]byte("runtime crash diagnostic marker\n"))
		os.Exit(0)
	}

	directory := t.TempDir()
	command := exec.Command(os.Args[0], "-test.run=^TestPersistentLoggingCapturesRuntimeStderrDirectly$")
	command.Env = append(os.Environ(), "BLUR_LOG_TEST_HELPER_DIR="+directory)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("logging subprocess failed: %v: %s", err, output)
	}
	data, err := os.ReadFile(filepath.Join(directory, "blur.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("runtime crash diagnostic marker")) {
		t.Fatalf("runtime stderr was lost on immediate process exit: %q", data)
	}
}
