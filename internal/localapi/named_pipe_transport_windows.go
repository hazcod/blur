//go:build windows

package localapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"sync"

	winio "github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

type NamedPipeTransport struct{ maxBytes int }

func NewNamedPipeTransport(maxMessageBytes int) *NamedPipeTransport {
	if maxMessageBytes <= 0 || maxMessageBytes > maxStreamFrameBytes {
		return &NamedPipeTransport{}
	}
	return &NamedPipeTransport{maxBytes: maxMessageBytes}
}

// CurrentUserNamedPipeEndpoint returns an opaque, stable pipe name derived
// from the current account SID; the SID itself is never placed in the path.
func CurrentUserNamedPipeEndpoint() (EndpointDescriptor, error) {
	_, sid, err := currentUserSID()
	if err != nil {
		return EndpointDescriptor{}, ErrEndpointUnavailable
	}
	digest := sha256.Sum256([]byte(sid))
	name := hex.EncodeToString(digest[:16])
	endpoint := EndpointDescriptor{Kind: EndpointNamedPipe, Address: `\\.\pipe\blur-user-` + name[:16] + "-" + name[16:]}
	if endpoint.Validate() != nil {
		return EndpointDescriptor{}, ErrEndpointUnavailable
	}
	return endpoint, nil
}

func (t *NamedPipeTransport) Listen(ctx context.Context, endpoint EndpointDescriptor) (LocalListener, error) {
	if contextErr(ctx) != nil {
		return nil, ErrCanceled
	}
	if t == nil || t.maxBytes <= 0 || !isCurrentUserPipe(endpoint) {
		return nil, ErrInvalidEndpoint
	}
	_, sid, err := currentUserSID()
	if err != nil {
		return nil, ErrEndpointUnavailable
	}
	// A protected DACL grants Generic All only to this account. go-winio also
	// sets FILE_PIPE_REJECT_REMOTE_CLIENTS on server handles.
	listener, err := winio.ListenPipe(endpoint.Address, &winio.PipeConfig{
		SecurityDescriptor: "D:P(A;;GA;;;" + sid + ")",
		InputBufferSize:    int32(t.maxBytes + 4),
		OutputBufferSize:   int32(t.maxBytes + 4),
	})
	if err != nil {
		return nil, ErrEndpointUnavailable
	}
	return &namedPipeListener{listener: listener, maxBytes: t.maxBytes}, nil
}

func (t *NamedPipeTransport) Dial(ctx context.Context, endpoint EndpointDescriptor) (LocalConn, error) {
	if contextErr(ctx) != nil {
		return nil, ErrCanceled
	}
	if t == nil || t.maxBytes <= 0 || !isCurrentUserPipe(endpoint) {
		return nil, ErrInvalidEndpoint
	}
	conn, err := winio.DialPipeAccessImpLevel(ctx, endpoint.Address, windows.GENERIC_READ|windows.GENERIC_WRITE, winio.PipeImpLevelIdentification)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ErrCanceled
		}
		return nil, ErrEndpointUnavailable
	}
	if err := verifyPipePeer(conn, false); err != nil {
		_ = conn.Close()
		return nil, ErrEndpointUnavailable
	}
	return &streamConnFramer{conn: conn, maxBytes: t.maxBytes}, nil
}

func isCurrentUserPipe(endpoint EndpointDescriptor) bool {
	if endpoint.Validate() != nil {
		return false
	}
	current, err := CurrentUserNamedPipeEndpoint()
	return err == nil && endpoint == current
}

func currentUserSID() (*windows.SID, string, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		return nil, "", ErrEndpointUnavailable
	}
	sid := user.User.Sid.String()
	if sid == "" {
		return nil, "", ErrEndpointUnavailable
	}
	return user.User.Sid, sid, nil
}

func verifyPipePeer(conn net.Conn, peerIsClient bool) error {
	fd, ok := conn.(interface{ Fd() uintptr })
	if !ok || fd.Fd() == 0 {
		return ErrEndpointUnavailable
	}
	pipe := windows.Handle(fd.Fd())
	var pid uint32
	var err error
	if peerIsClient {
		err = windows.GetNamedPipeClientProcessId(pipe, &pid)
	} else {
		err = windows.GetNamedPipeServerProcessId(pipe, &pid)
	}
	if err != nil || pid == 0 {
		return ErrEndpointUnavailable
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ErrEndpointUnavailable
	}
	defer windows.CloseHandle(process)
	var token windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		return ErrEndpointUnavailable
	}
	defer token.Close()
	peerUser, err := token.GetTokenUser()
	if err != nil || peerUser == nil || peerUser.User.Sid == nil {
		return ErrEndpointUnavailable
	}
	currentUser, _, err := currentUserSID()
	if err != nil || !peerUser.User.Sid.Equals(currentUser) {
		return ErrEndpointUnavailable
	}
	return nil
}

type namedPipeListener struct {
	listener net.Listener
	maxBytes int
	acceptMu sync.Mutex
	closeOne sync.Once
	closeErr error
}

func (l *namedPipeListener) Accept(ctx context.Context) (LocalConn, error) {
	if l == nil || l.listener == nil {
		return nil, ErrCanceled
	}
	l.acceptMu.Lock()
	defer l.acceptMu.Unlock()
	if contextErr(ctx) != nil {
		return nil, ErrCanceled
	}
	type result struct {
		conn net.Conn
		err  error
	}
	accepted := make(chan result, 1)
	go func() {
		conn, err := l.listener.Accept()
		accepted <- result{conn: conn, err: err}
	}()
	select {
	case <-ctx.Done():
		_ = l.Close()
		res := <-accepted
		if res.conn != nil {
			_ = res.conn.Close()
		}
		return nil, ErrCanceled
	case res := <-accepted:
		if res.err != nil || res.conn == nil {
			return nil, ErrClosed
		}
		if err := verifyPipePeer(res.conn, true); err != nil {
			_ = res.conn.Close()
			return nil, ErrEndpointUnavailable
		}
		return &streamConnFramer{conn: res.conn, maxBytes: l.maxBytes}, nil
	}
}

func (l *namedPipeListener) Close() error {
	if l == nil || l.listener == nil {
		return nil
	}
	l.closeOne.Do(func() { l.closeErr = l.listener.Close() })
	return l.closeErr
}
