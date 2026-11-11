//go:build windows

package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"

	platformwindows "github.com/hazcod/blur/internal/platform/windows"
)

const maxPersistentLogBytes = 4 << 20

type windowsPersistentLogWriter struct {
	mu   sync.Mutex
	path string
	file *os.File
	size int64
}

func (w *windowsPersistentLogWriter) Write(data []byte) (int, error) {
	if w == nil || len(data) == 0 {
		return len(data), nil
	}
	inputBytes := len(data)
	if len(data) > maxPersistentLogBytes {
		data = data[:maxPersistentLogBytes]
	}
	w.mu.Lock()
	defer w.mu.Unlock()
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

func (w *windowsPersistentLogWriter) rotate() error {
	if err := w.file.Sync(); err != nil {
		return errors.New("persistent log sync failed")
	}
	backup := w.path + ".1"
	if err := os.Remove(backup); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("persistent log backup rotation failed")
	}
	if err := os.Rename(w.path, backup); err != nil {
		return errors.New("persistent log rotation failed")
	}
	next, err := openWindowsLogFile(w.path, true)
	if err != nil {
		_ = os.Rename(backup, w.path)
		return errors.New("persistent log rotation reopen failed")
	}
	previous := w.file
	w.file, w.size = next, 0
	_ = previous.Close()
	return nil
}

func (w *windowsPersistentLogWriter) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	_ = w.file.Sync()
	err := w.file.Close()
	w.file = nil
	return err
}

func configurePersistentLogging() (func(), error) {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		base, err = os.UserCacheDir()
	}
	if err != nil || base == "" {
		return nil, errors.New("user log directory unavailable")
	}
	directory := filepath.Join(base, "Blur", "Logs")
	writer, err := openWindowsPersistentLogForUser(filepath.Join(directory, "blur.log"))
	if err != nil {
		cache, cacheErr := os.UserCacheDir()
		if cacheErr == nil && cache != "" {
			directory = filepath.Join(cache, "Blur", "Logs")
			writer, err = openWindowsPersistentLogForUser(filepath.Join(directory, "blur.log"))
		}
	}
	if err != nil {
		return nil, errors.New("log file unavailable")
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(writer, &slog.HandlerOptions{Level: slog.LevelDebug})))
	crashLog, crashErr := openWindowsCrashLog(filepath.Join(directory, "crash.log"))
	if crashErr != nil {
		slog.Warn("runtime crash log unavailable", "result", "application log remains active")
	} else if err := debug.SetCrashOutput(crashLog, debug.CrashOptions{}); err != nil {
		slog.Warn("runtime crash log unavailable", "result", "application log remains active")
		_ = crashLog.Close()
		crashLog = nil
	}
	slog.Info("application process started", "log", filepath.Join(directory, "blur.log"))
	var closeOnce sync.Once
	closeOutput := func() {
		closeOnce.Do(func() {
			slog.Info("application process stopped")
			if crashLog != nil {
				_ = debug.SetCrashOutput(nil, debug.CrashOptions{})
				_ = crashLog.Close()
			}
			_ = writer.Close()
		})
	}
	return closeOutput, nil
}

func openWindowsPersistentLog(path string) (*windowsPersistentLogWriter, error) {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, errors.New("log directory unavailable")
	}
	if err := platformwindows.RestrictPathToCurrentUser(directory); err != nil {
		return nil, errors.New("log directory permissions unavailable")
	}
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() {
			return nil, errors.New("log file is not regular")
		}
		if info.Size() >= maxPersistentLogBytes {
			backup := path + ".1"
			if err := os.Remove(backup); err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, errors.New("persistent log backup unavailable")
			}
			if err := os.Rename(path, backup); err != nil {
				return nil, errors.New("persistent log rotation failed")
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("log file unavailable")
	}
	file, err := openWindowsLogFile(path, false)
	if err != nil {
		return nil, errors.New("log file unavailable")
	}
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, errors.New("log file is not regular")
	}
	return &windowsPersistentLogWriter{path: path, file: file, size: info.Size()}, nil
}

func openWindowsPersistentLogForUser(path string) (*windowsPersistentLogWriter, error) {
	directory := filepath.Dir(path)
	parent := filepath.Dir(directory)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return nil, errors.New("log parent directory unavailable")
	}
	if err := platformwindows.RestrictPathToCurrentUser(parent); err != nil {
		return nil, errors.New("log parent directory permissions unavailable")
	}
	return openWindowsPersistentLog(path)
}

func openWindowsLogFile(path string, exclusive bool) (*os.File, error) {
	flags := os.O_CREATE | os.O_APPEND | os.O_WRONLY
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
	if err := platformwindows.RestrictPathToCurrentUser(path); err != nil {
		_ = file.Close()
		return nil, errors.New("log file permissions unavailable")
	}
	return file, nil
}

func openWindowsCrashLog(path string) (*os.File, error) {
	backup := ""
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return nil, errors.New("crash log is not regular")
		}
		if info.Size() > 0 {
			if err := platformwindows.RestrictPathToCurrentUser(path); err != nil {
				return nil, errors.New("previous crash log permissions unavailable")
			}
			backup = path + ".1"
			if err := os.Remove(backup); err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, errors.New("previous crash log rotation failed")
			}
			if err := os.Rename(path, backup); err != nil {
				return nil, errors.New("previous crash log rotation failed")
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("crash log unavailable")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		if backup != "" {
			_ = os.Rename(backup, path)
		}
		return nil, fmt.Errorf("crash log unavailable: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		if backup != "" {
			_ = os.Rename(backup, path)
		}
		return nil, errors.New("crash log permissions unavailable")
	}
	if err := platformwindows.RestrictPathToCurrentUser(path); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		if backup != "" {
			_ = os.Rename(backup, path)
		}
		return nil, errors.New("crash log permissions unavailable")
	}
	return file, nil
}
