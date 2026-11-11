//go:build darwin || linux

package localapi

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const maxUnixFrameBytes = 8 << 20

// UnixTransport implements bounded AF_UNIX transport. It only accepts socket
// paths inside an existing owner-only directory, sets socket mode 0600, and
// rejects peers whose kernel-reported UID differs from the current user.
type UnixTransport struct{ maxBytes int }

func NewUnixTransport(maxMessageBytes int) *UnixTransport {
	if maxMessageBytes <= 0 || maxMessageBytes > maxUnixFrameBytes {
		return &UnixTransport{}
	}
	return &UnixTransport{maxBytes: maxMessageBytes}
}

func (t *UnixTransport) Listen(ctx context.Context, endpoint EndpointDescriptor) (LocalListener, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if t == nil || t.maxBytes <= 0 || endpoint.Kind != EndpointUnix || endpoint.Validate() != nil || checkPrivateSocketParent(filepath.Dir(endpoint.Address)) != nil {
		return nil, ErrInvalidEndpoint
	}
	if _, err := os.Lstat(endpoint.Address); err == nil {
		return nil, ErrEndpointInUse
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, ErrEndpointUnavailable
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: endpoint.Address, Net: "unix"})
	if err != nil {
		if errors.Is(err, unix.EADDRINUSE) {
			return nil, ErrEndpointInUse
		}
		return nil, ErrEndpointUnavailable
	}
	listener.SetUnlinkOnClose(false)
	if err := os.Chmod(endpoint.Address, 0o600); err != nil {
		_ = listener.Close()
		return nil, ErrEndpointUnavailable
	}
	info, err := os.Lstat(endpoint.Address)
	if err != nil || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o600 || !ownedByCurrentUser(info) {
		_ = listener.Close()
		if err == nil {
			_ = removeOwnedSocket(endpoint.Address, info)
		}
		return nil, ErrEndpointUnavailable
	}
	return &unixListener{listener: listener, path: endpoint.Address, info: info, maxBytes: t.maxBytes}, nil
}

func (t *UnixTransport) Dial(ctx context.Context, endpoint EndpointDescriptor) (LocalConn, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if t == nil || t.maxBytes <= 0 || endpoint.Kind != EndpointUnix || endpoint.Validate() != nil || checkPrivateSocketParent(filepath.Dir(endpoint.Address)) != nil {
		return nil, ErrInvalidEndpoint
	}
	info, err := os.Lstat(endpoint.Address)
	if err != nil || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm()&0o077 != 0 || !ownedByCurrentUser(info) {
		return nil, ErrEndpointUnavailable
	}
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "unix", endpoint.Address)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ErrCanceled
		}
		return nil, ErrEndpointUnavailable
	}
	unixConn, ok := conn.(*net.UnixConn)
	if !ok || peerUID(unixConn) != os.Getuid() {
		_ = conn.Close()
		return nil, ErrEndpointUnavailable
	}
	return &streamConnFramer{conn: unixConn, maxBytes: t.maxBytes}, nil
}

type unixListener struct {
	listener *net.UnixListener
	path     string
	info     os.FileInfo
	maxBytes int
	acceptMu sync.Mutex
	closeMu  sync.Mutex
	closed   bool
}

func (l *unixListener) Accept(ctx context.Context) (LocalConn, error) {
	if contextErr(ctx) != nil || l == nil || l.listener == nil {
		return nil, ErrCanceled
	}
	l.acceptMu.Lock()
	defer l.acceptMu.Unlock()
	stop := context.AfterFunc(ctx, func() { _ = l.listener.SetDeadline(time.Now()) })
	conn, err := l.listener.AcceptUnix()
	stop()
	_ = l.listener.SetDeadline(time.Time{})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ErrCanceled
		}
		return nil, ErrClosed
	}
	if peerUID(conn) != os.Getuid() {
		_ = conn.Close()
		return nil, ErrEndpointUnavailable
	}
	return &streamConnFramer{conn: conn, maxBytes: l.maxBytes}, nil
}

func (l *unixListener) Close() error {
	if l == nil || l.listener == nil {
		return nil
	}
	l.closeMu.Lock()
	if l.closed {
		l.closeMu.Unlock()
		return nil
	}
	l.closed = true
	l.closeMu.Unlock()
	err := l.listener.Close()
	if removeErr := removeOwnedSocket(l.path, l.info); err == nil && removeErr != nil {
		err = removeErr
	}
	return err
}

func (l *unixListener) String() string { return "private Unix listener" }

func checkPrivateSocketParent(path string) error {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 || !ownedByCurrentUser(info) {
		return ErrInvalidEndpoint
	}
	return nil
}

func removeOwnedSocket(path string, expected os.FileInfo) error {
	current, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || expected == nil || current.Mode()&os.ModeSocket == 0 || !os.SameFile(expected, current) {
		return ErrEndpointUnavailable
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return ErrEndpointUnavailable
	}
	return nil
}
