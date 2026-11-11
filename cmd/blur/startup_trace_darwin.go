//go:build darwin

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxStartupTraceSize = 4096

var startupTraceEnabled bool

var startupTraceStages = map[string]struct{}{
	"process_started": {}, "process_returned_ok": {}, "process_returned_error": {},
	"already_running": {}, "store_open_started": {}, "store_open_ok": {},
	"store_open_failed": {}, "setup_ui_started": {}, "setup_ui_returned": {},
	"desktop_ui_started": {}, "desktop_ui_returned": {},
	"startup_error": {},
}

func traceLaunchStage(stage string) {
	if !startupTraceEnabled {
		return
	}
	directory, err := blurLogsDirectory()
	if err != nil {
		return
	}
	if err := traceStartupStage(startupTraceEnabled, filepath.Join(directory, "startup.log"), stage); err != nil {
		return
	}
}

func traceStartupStage(enabled bool, path, stage string) error {
	if !enabled {
		return nil
	}
	return writeStartupTrace(path, stage)
}

func writeStartupTrace(path, stage string) error {
	if path == "" {
		return errors.New("blur startup trace: missing path")
	}
	if _, ok := startupTraceStages[stage]; !ok {
		return errors.New("blur startup trace: invalid stage")
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return err
	}
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if stage == "process_started" {
		flags |= os.O_TRUNC
	}
	file, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	line := fmt.Sprintf("%s %s\n", time.Now().UTC().Format(time.RFC3339Nano), stage)
	if info.Size()+int64(len(line)) > maxStartupTraceSize {
		if err := file.Truncate(0); err != nil {
			return err
		}
		if _, err := file.Seek(0, 0); err != nil {
			return err
		}
	}
	_, err = file.WriteString(line)
	return err
}

func writeStartupError(path string, startupErr error) error {
	if startupErr == nil {
		return errors.New("blur startup trace: missing error")
	}
	detail := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, startupErr.Error())
	if len(detail) > 512 {
		detail = detail[:512]
	}
	if err := writeStartupTrace(path, "startup_error"); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	_, err = fmt.Fprintf(file, "%s startup_error=%s\n", time.Now().UTC().Format(time.RFC3339Nano), detail)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil || info.Size() <= maxStartupTraceSize {
		return err
	}
	if err := file.Truncate(maxStartupTraceSize); err != nil {
		return err
	}
	return nil
}
