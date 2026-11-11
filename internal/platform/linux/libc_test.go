//go:build linux

package linux

import (
	"testing"

	"github.com/golang-gui/goui/platform/linux/libs/libc"
)

func TestNativeEventPipePollsReadableAfterWrite(t *testing.T) {
	var pipe [2]int32
	if err := libc.Pipe(&pipe); err != nil {
		t.Fatalf("create event pipe: %v", err)
	}
	t.Cleanup(func() {
		libc.Close(pipe[0])
		libc.Close(pipe[1])
	})

	if n, errno := libc.Write(pipe[1], []byte{0x5a}); n != 1 || errno != 0 {
		t.Fatalf("write event pipe = (%d, %v), want (1, 0)", n, errno)
	}

	fds := []libc.PollFd{{Fd: pipe[0], Events: libc.POLLIN}}
	n, errno := libc.Poll(fds, 1000)
	if errno != 0 || n != 1 {
		t.Fatalf("poll event pipe = (%d, %v), want (1, 0)", n, errno)
	}
	if fds[0].Revents&libc.POLLIN == 0 {
		t.Fatalf("poll revents = %#x, want readable", fds[0].Revents)
	}

	var read [1]byte
	if n, errno := libc.Read(pipe[0], read[:]); n != 1 || errno != 0 || read[0] != 0x5a {
		t.Fatalf("read event pipe = (%d, %v, %#x), want (1, 0, 0x5a)", n, errno, read[0])
	}
}
