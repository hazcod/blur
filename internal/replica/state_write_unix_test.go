//go:build darwin || linux

package replica

import (
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
)

func TestStateWriteFailureDoesNotPublishPartialJSON(t *testing.T) {
	if path := os.Getenv("BLUR_TEST_STATE_WRITE_LIMIT_PATH"); path != "" {
		// Limit only this child process; ignore the signal so Write returns the
		// real short-write error instead of terminating the helper.
		signal.Ignore(syscall.SIGXFSZ)
		if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &syscall.Rlimit{Cur: 1, Max: 1}); err != nil {
			t.Fatal(err)
		}
		if err := writeStateFile(path, State{Version: 1, DeviceID: "fixture"}); err == nil {
			t.Fatal("state write failure was discarded")
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("partial state was published: %v", err)
		}
		return
	}
	path := filepath.Join(t.TempDir(), "state.json")
	command := exec.Command(os.Args[0], "-test.run=^TestStateWriteFailureDoesNotPublishPartialJSON$")
	command.Env = append(os.Environ(), "BLUR_TEST_STATE_WRITE_LIMIT_PATH="+path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("state write failure regression: %v\n%s", err, output)
	}
}
