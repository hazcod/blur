//go:build darwin || (linux && (amd64 || arm64))

package main

import (
	"errors"
	"github.com/hazcod/blur/internal/acceptance"
	"github.com/hazcod/blur/internal/applog"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

const maxPersistentLogBytes = 4 << 20

var persistentLogDirectory string

type persistentLogWriter struct {
	mu               sync.Mutex
	path             string
	file             *os.File
	size             int64
	stderrRedirected bool
}

func (w *persistentLogWriter) Write(data []byte) (int, error) {
	if w == nil || len(data) == 0 {
		return len(data), nil
	}
	inputBytes := len(data)
	w.mu.Lock()
	defer w.mu.Unlock()
	if int64(len(data)) > maxPersistentLogBytes {
		data = data[:maxPersistentLogBytes]
	}
	if w.file == nil {
		return 0, errors.New("persistent log is closed")
	}
	info, err := w.file.Stat()
	if err != nil {
		return 0, errors.New("persistent log size unavailable")
	}
	if info.Size() > w.size {
		w.size = info.Size()
	}
	if w.size+int64(len(data)) > maxPersistentLogBytes {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.file.Write(data)
	w.size += int64(n)
	if err == nil && n == len(data) {
		return inputBytes, nil
	}
	return n, err
}

func (w *persistentLogWriter) rotate() error {
	if err := w.file.Sync(); err != nil {
		return errors.New("persistent log sync failed")
	}
	if err := os.Rename(w.path, w.path+".1"); err != nil {
		return errors.New("persistent log rotation failed")
	}
	next, err := openLogFile(w.path, true)
	if err != nil {
		_ = os.Rename(w.path+".1", w.path)
		return errors.New("persistent log rotation reopen failed")
	}
	if w.stderrRedirected {
		if err := unix.Dup2(int(next.Fd()), 2); err != nil {
			_ = next.Close()
			_ = os.Remove(w.path)
			_ = os.Rename(w.path+".1", w.path)
			return errors.New("persistent runtime stderr rotation failed")
		}
	}
	previous := w.file
	w.file, w.size = next, 0
	_ = previous.Close()
	return nil
}

func (w *persistentLogWriter) Close() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return
	}
	_ = w.file.Sync()
	_ = w.file.Close()
	w.file = nil
}

// configurePersistentLogging keeps app and runtime stderr diagnostics available
// when Blur is launched from Finder, LaunchServices or a desktop launcher, where
// no terminal exists.
func configurePersistentLogging() (func(), error) {
	directory, err := blurLogsDirectory()
	if err != nil {
		return nil, errors.New("user log directory unavailable")
	}
	path := filepath.Join(directory, "blur.log")
	output, openErr := openPersistentLog(directory, path)
	if openErr != nil && !acceptance.Enabled {
		// Launcher-started apps should keep diagnostics even if the preferred log
		// directory is unavailable. The cache fallback is still private to this user and
		// bounded/rotated exactly like the preferred log location.
		cacheDirectory, cacheErr := os.UserCacheDir()
		if cacheErr == nil && cacheDirectory != "" {
			directory = filepath.Join(cacheDirectory, "Blur", "Logs")
			path = filepath.Join(directory, "blur.log")
			output, openErr = openPersistentLog(directory, path)
		}
	}
	if openErr != nil {
		return nil, errors.New("log file unavailable")
	}
	persistentLogDirectory = directory
	slog.SetDefault(slog.New(slog.NewTextHandler(output, &slog.HandlerOptions{Level: applog.Level()})))
	if err := unix.Dup2(int(output.file.Fd()), 2); err != nil {
		slog.Warn("runtime stderr redirection unavailable", "result", "application logging remains active")
	} else {
		output.stderrRedirected = true
	}
	slog.Info("application process started", "log", path)
	closeOutput := func() {
		slog.Info("application process stopped")
		if output.stderrRedirected {
			_ = syscall.Close(2)
		}
		output.Close()
	}
	return closeOutput, nil
}

func openPersistentLog(directory, path string) (*persistentLogWriter, error) {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, errors.New("log directory unavailable")
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return nil, errors.New("log directory permissions unavailable")
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return nil, errors.New("log file is not a regular file")
		}
		if info.Size() >= maxPersistentLogBytes {
			_ = os.Rename(path, path+".1")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("log file unavailable")
	}
	file, err := openLogFile(path, false)
	if err != nil {
		return nil, errors.New("log file unavailable")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, errors.New("log file is not regular")
	}
	return &persistentLogWriter{path: path, file: file, size: info.Size()}, nil
}

func openLogFile(path string, exclusive bool) (*os.File, error) {
	flags := os.O_CREATE | os.O_APPEND | os.O_WRONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
	if exclusive {
		flags |= os.O_EXCL
	}
	file, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, errors.New("log file permissions unavailable")
	}
	if file.Fd() == 2 {
		fd, err := syscall.Dup(2)
		if err != nil {
			_ = file.Close()
			return nil, errors.New("log file descriptor unavailable")
		}
		name := file.Name()
		_ = file.Close()
		file = os.NewFile(uintptr(fd), name)
		if file == nil {
			_ = syscall.Close(fd)
			return nil, errors.New("log file descriptor unavailable")
		}
	}
	return file, nil
}

func blurLogsDirectory() (string, error) {
	if acceptance.Enabled {
		config, err := acceptance.Load()
		if err != nil {
			return "", err
		}
		// Ignore production logging overrides in this separately built fixture.
		return config.LogDirectory(), nil
	}
	if persistentLogDirectory != "" {
		return persistentLogDirectory, nil
	}
	return defaultLogsDirectory()
}
