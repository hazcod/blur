//go:build darwin

package localapi

import (
	"net"

	"golang.org/x/sys/unix"
)

func peerUID(conn *net.UnixConn) int {
	if conn == nil {
		return -1
	}
	raw, err := conn.SyscallConn()
	if err != nil {
		return -1
	}
	uid := -1
	if err := raw.Control(func(fd uintptr) {
		credential, err := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if err == nil && credential != nil {
			uid = int(credential.Uid)
		}
	}); err != nil {
		return -1
	}
	return uid
}
