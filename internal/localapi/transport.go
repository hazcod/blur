package localapi

import (
	"context"
	"errors"
	"path"
	"strings"
	"sync"
)

type EndpointKind string

const (
	EndpointUnix      EndpointKind = "unix"
	EndpointNamedPipe EndpointKind = "named_pipe"
)

var (
	ErrInvalidEndpoint     = errors.New("invalid local endpoint")
	ErrEndpointUnavailable = errors.New("local endpoint unavailable")
	ErrEndpointInUse       = errors.New("local endpoint in use")
	ErrFrameTooLarge       = errors.New("local frame too large")
	ErrClosed              = errors.New("local endpoint closed")
	ErrCanceled            = errors.New("local operation canceled")
)

// EndpointDescriptor describes a native local endpoint. Its requirements are
// declarative: MemoryTransport does not implement native ACL or peer checks.
type EndpointDescriptor struct {
	Kind    EndpointKind
	Address string
}

func (e EndpointDescriptor) Validate() error {
	switch e.Kind {
	case EndpointUnix:
		if len(e.Address) < 2 || len(e.Address) > 103 || !path.IsAbs(e.Address) || path.Clean(e.Address) != e.Address || strings.IndexByte(e.Address, 0) >= 0 {
			return ErrInvalidEndpoint
		}
		for i := 0; i < len(e.Address); i++ {
			c := e.Address[i]
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '/' || c == '-' || c == '_' || c == '.') {
				return ErrInvalidEndpoint
			}
		}
	case EndpointNamedPipe:
		const prefix = `\\.\pipe\blur-user-`
		if !strings.HasPrefix(e.Address, prefix) || len(e.Address) > 256 {
			return ErrInvalidEndpoint
		}
		name := strings.TrimPrefix(e.Address, prefix)
		if len(name) < 3 || len(name) > 128 || strings.Contains(name, "..") || name[0] == '-' || name[len(name)-1] == '-' || name[len(name)-1] == '.' || !strings.Contains(name, "-") {
			return ErrInvalidEndpoint
		}
		for i := 0; i < len(name); i++ {
			c := name[i]
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.') {
				return ErrInvalidEndpoint
			}
		}
	default:
		return ErrInvalidEndpoint
	}
	return nil
}

// Native adapters must restrict endpoint access to the current user.
func (e EndpointDescriptor) RequiresUserOnlyAccess() bool { return e.Validate() == nil }

// Native adapters must validate AF_UNIX peer credentials or named-pipe token.
func (e EndpointDescriptor) RequiresPeerEvidence() bool { return e.Validate() == nil }

type LocalTransport interface {
	Listen(context.Context, EndpointDescriptor) (LocalListener, error)
	Dial(context.Context, EndpointDescriptor) (LocalConn, error)
}

type LocalListener interface {
	Accept(context.Context) (LocalConn, error)
	Close() error
}

type LocalConn interface {
	Send(context.Context, []byte) error
	Receive(context.Context) ([]byte, error)
	Close() error
}

type MemoryTransport struct {
	mu        sync.Mutex
	listeners map[EndpointDescriptor]*memoryListener
	queueSize int
	maxBytes  int
}

// NewMemoryTransport is a bounded, in-process conformance transport.
func NewMemoryTransport(queueSize, maxMessageBytes int) *MemoryTransport {
	return &MemoryTransport{listeners: make(map[EndpointDescriptor]*memoryListener), queueSize: queueSize, maxBytes: maxMessageBytes}
}

type memoryListener struct {
	owner    *MemoryTransport
	endpoint EndpointDescriptor
	incoming chan *memoryConn
	done     chan struct{}
	mu       sync.Mutex
	closed   bool
	links    map[*memoryLink]struct{}
}

type memoryLink struct {
	mu      sync.Mutex
	closed  bool
	done    chan struct{}
	onClose func(*memoryLink)
}

type memoryConn struct {
	link     *memoryLink
	inbound  chan []byte
	slots    chan struct{}
	peer     *memoryConn
	maxBytes int
}

func contextErr(ctx context.Context) error {
	if ctx == nil || ctx.Err() != nil {
		return ErrCanceled
	}
	return nil
}

func (m *MemoryTransport) Listen(ctx context.Context, e EndpointDescriptor) (LocalListener, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if err := e.Validate(); err != nil {
		return nil, err
	}
	if m == nil || m.queueSize <= 0 || m.maxBytes <= 0 {
		return nil, ErrInvalidEndpoint
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.listeners[e]; exists {
		return nil, ErrEndpointInUse
	}
	l := &memoryListener{owner: m, endpoint: e, incoming: make(chan *memoryConn, m.queueSize), done: make(chan struct{}), links: make(map[*memoryLink]struct{})}
	m.listeners[e] = l
	return l, nil
}

func (m *MemoryTransport) Dial(ctx context.Context, e EndpointDescriptor) (LocalConn, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if err := e.Validate(); err != nil {
		return nil, err
	}
	if m == nil {
		return nil, ErrEndpointUnavailable
	}
	m.mu.Lock()
	l := m.listeners[e]
	m.mu.Unlock()
	if l == nil {
		return nil, ErrEndpointUnavailable
	}
	link := &memoryLink{done: make(chan struct{})}
	link.onClose = func(closed *memoryLink) {
		l.mu.Lock()
		delete(l.links, closed)
		l.mu.Unlock()
	}
	client := newMemoryConn(link, m.queueSize, m.maxBytes)
	server := newMemoryConn(link, m.queueSize, m.maxBytes)
	client.peer = server
	server.peer = client
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil, ErrEndpointUnavailable
	}
	l.links[link] = struct{}{}
	l.mu.Unlock()
	select {
	case <-ctx.Done():
		link.close()
		return nil, ErrCanceled
	case <-l.done:
		link.close()
		return nil, ErrEndpointUnavailable
	case l.incoming <- server:
		if err := contextErr(ctx); err != nil {
			link.close()
			return nil, err
		}
		return client, nil
	}
}

func (l *memoryListener) Accept(ctx context.Context) (LocalConn, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	select {
	case <-l.done:
		return nil, ErrClosed
	default:
	}
	select {
	case <-ctx.Done():
		return nil, ErrCanceled
	case <-l.done:
		return nil, ErrClosed
	case c := <-l.incoming:
		return c, nil
	}
}

func (l *memoryListener) Close() error {
	l.owner.mu.Lock()
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		l.owner.mu.Unlock()
		return nil
	}
	l.closed = true
	close(l.done)
	delete(l.owner.listeners, l.endpoint)
	links := make([]*memoryLink, 0, len(l.links))
	for link := range l.links {
		links = append(links, link)
	}
	l.mu.Unlock()
	l.owner.mu.Unlock()
	for _, link := range links {
		link.close()
	}
	return nil
}

func (l *memoryLink) close() {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.closed = true
	close(l.done)
	l.mu.Unlock()
	if l.onClose != nil {
		l.onClose(l)
	}
}

func newMemoryConn(link *memoryLink, queueSize, maxBytes int) *memoryConn {
	c := &memoryConn{link: link, inbound: make(chan []byte, queueSize), slots: make(chan struct{}, queueSize), maxBytes: maxBytes}
	for i := 0; i < queueSize; i++ {
		c.slots <- struct{}{}
	}
	return c
}

func (c *memoryConn) Send(ctx context.Context, message []byte) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	select {
	case <-c.link.done:
		return ErrClosed
	default:
	}
	if len(message) > c.maxBytes {
		return ErrFrameTooLarge
	}
	select {
	case <-ctx.Done():
		return ErrCanceled
	case <-c.link.done:
		return ErrClosed
	case <-c.peer.slots:
	}
	c.link.mu.Lock()
	defer c.link.mu.Unlock()
	if c.link.closed {
		c.peer.slots <- struct{}{}
		return ErrClosed
	}
	if err := contextErr(ctx); err != nil {
		c.peer.slots <- struct{}{}
		return err
	}
	c.peer.inbound <- append([]byte(nil), message...)
	return nil
}

func (c *memoryConn) Receive(ctx context.Context) ([]byte, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	select {
	case <-c.link.done:
		return nil, ErrClosed
	default:
	}
	select {
	case <-ctx.Done():
		return nil, ErrCanceled
	case <-c.link.done:
		return nil, ErrClosed
	case message := <-c.inbound:
		c.slots <- struct{}{}
		return append([]byte(nil), message...), nil
	}
}

func (c *memoryConn) Close() error { c.link.close(); return nil }

var _ LocalTransport = (*MemoryTransport)(nil)
