package localapi

import (
	"bytes"
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
)

func TestEndpointDescriptors(t *testing.T) {
	valid := []EndpointDescriptor{{Kind: EndpointUnix, Address: "/tmp/blur-user/socket"}, {Kind: EndpointNamedPipe, Address: `\\.\pipe\blur-user-alice-main`}}
	for _, e := range valid {
		if err := e.Validate(); err != nil {
			t.Fatalf("%+v: %v", e, err)
		}
		if !e.RequiresUserOnlyAccess() || !e.RequiresPeerEvidence() {
			t.Fatal("security requirements absent")
		}
	}
	invalid := []EndpointDescriptor{{}, {Kind: "tcp", Address: "127.0.0.1:1"}, {Kind: EndpointUnix, Address: "relative"}, {Kind: EndpointUnix, Address: "/tmp/../socket"}, {Kind: EndpointUnix, Address: "/tmp/blur\n/socket"}, {Kind: EndpointUnix, Address: "/"}, {Kind: EndpointNamedPipe, Address: `\\.\pipe\other`}, {Kind: EndpointNamedPipe, Address: `\\.\pipe\blur-user-alice-..`}, {Kind: EndpointNamedPipe, Address: `\\.\pipe\blur-user-alice/name`}}
	for _, e := range invalid {
		if err := e.Validate(); !errors.Is(err, ErrInvalidEndpoint) {
			t.Fatalf("accepted %+v: %v", e, err)
		}
	}
}

func TestMemoryTransportRoundTripCopiesAndBounds(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryTransport(1, 4)
	e := EndpointDescriptor{Kind: EndpointUnix, Address: "/tmp/blur-user/socket"}
	l, err := m.Listen(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	client, err := m.Dial(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := l.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	message := []byte("ping")
	if err := client.Send(ctx, message); err != nil {
		t.Fatal(err)
	}
	message[0] = 'x'
	got, err := server.Receive(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "ping" {
		t.Fatalf("got %q", got)
	}
	got[0] = 'x'
	if err := server.Send(ctx, []byte("pong")); err != nil {
		t.Fatal(err)
	}
	got, err = client.Receive(ctx)
	if err != nil || string(got) != "pong" {
		t.Fatalf("got %q, %v", got, err)
	}
	if err := client.Send(ctx, bytes.Repeat([]byte{'a'}, 5)); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("oversize: %v", err)
	}
}

func TestMemoryTransportRejectsDuplicateUnknownCancelAndClose(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryTransport(1, 4)
	e := EndpointDescriptor{Kind: EndpointUnix, Address: "/tmp/blur-user/socket"}
	if _, err := m.Dial(ctx, e); !errors.Is(err, ErrEndpointUnavailable) {
		t.Fatalf("unknown: %v", err)
	}
	l, err := m.Listen(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Listen(ctx, e); !errors.Is(err, ErrEndpointInUse) {
		t.Fatalf("duplicate: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := m.Dial(cancelled, e); !errors.Is(err, ErrCanceled) {
		t.Fatalf("dial cancellation: %v", err)
	}
	if _, err := l.Accept(cancelled); !errors.Is(err, ErrCanceled) {
		t.Fatalf("accept cancellation: %v", err)
	}
	c, err := m.Dial(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	s, err := l.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Send(cancelled, []byte("x")); !errors.Is(err, ErrCanceled) {
		t.Fatalf("send cancellation: %v", err)
	}
	if _, err := s.Receive(cancelled); !errors.Is(err, ErrCanceled) {
		t.Fatalf("receive cancellation: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.Send(ctx, []byte("x")); !errors.Is(err, ErrClosed) {
		t.Fatalf("send after close: %v", err)
	}
	if err := c.Send(ctx, []byte("oversized")); !errors.Is(err, ErrClosed) {
		t.Fatalf("send after close oversized: %v", err)
	}
	if _, err := l.Accept(ctx); !errors.Is(err, ErrClosed) {
		t.Fatalf("accept after close: %v", err)
	}
	if _, err := m.Dial(ctx, e); !errors.Is(err, ErrEndpointUnavailable) {
		t.Fatalf("dial after close: %v", err)
	}
	_ = s.Close()
	_ = c.Close()
}

func TestMemoryTransportConcurrentClientsNoLeak(t *testing.T) {
	ctx := context.Background()
	before := runtime.NumGoroutine()
	m := NewMemoryTransport(8, 32)
	e := EndpointDescriptor{Kind: EndpointNamedPipe, Address: `\\.\pipe\blur-user-alice-main`}
	l, err := m.Listen(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	servers := make([]LocalConn, 0, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := m.Dial(ctx, e)
			if err != nil {
				t.Error(err)
				return
			}
			defer c.Close()
			if err := c.Send(ctx, []byte("x")); err != nil {
				t.Error(err)
			}
			if _, err := c.Receive(ctx); err != nil {
				t.Error(err)
			}
		}()
	}
	for i := 0; i < 16; i++ {
		s, err := l.Accept(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Receive(ctx); err != nil {
			t.Fatal(err)
		}
		if err := s.Send(ctx, []byte("ack")); err != nil {
			t.Fatal(err)
		}
		servers = append(servers, s)
	}
	wg.Wait()
	for _, s := range servers {
		_ = s.Close()
	}
	_ = l.Close()
	after := runtime.NumGoroutine()
	if after > before+2 {
		t.Fatalf("goroutines before=%d after=%d", before, after)
	}
}

func TestMemoryTransportNilContextIsRejected(t *testing.T) {
	m := NewMemoryTransport(1, 4)
	e := EndpointDescriptor{Kind: EndpointUnix, Address: "/tmp/blur-user/socket"}
	if _, err := m.Listen(nil, e); !errors.Is(err, ErrCanceled) {
		t.Fatalf("nil listen: %v", err)
	}
	if _, err := m.Dial(nil, e); !errors.Is(err, ErrCanceled) {
		t.Fatalf("nil dial: %v", err)
	}
	l, err := m.Listen(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if _, err := l.Accept(nil); !errors.Is(err, ErrCanceled) {
		t.Fatalf("nil accept: %v", err)
	}
}

func TestMemoryTransportReleasesClosedConnections(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryTransport(1, 4)
	e := EndpointDescriptor{Kind: EndpointUnix, Address: "/tmp/blur-user/socket"}
	listener, err := m.Listen(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	l := listener.(*memoryListener)
	for i := 0; i < 20; i++ {
		client, err := m.Dial(ctx, e)
		if err != nil {
			t.Fatal(err)
		}
		server, err := l.Accept(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_ = client.Close()
		_ = server.Close()
	}
	l.mu.Lock()
	count := len(l.links)
	l.mu.Unlock()
	if count != 0 {
		t.Fatalf("retained %d closed connections", count)
	}
}

func TestMemoryTransportBlockedOperationsUnblock(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryTransport(1, 4)
	e := EndpointDescriptor{Kind: EndpointUnix, Address: "/tmp/blur-user/socket"}
	l, err := m.Listen(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	client, err := m.Dial(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	server, err := l.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Send(ctx, []byte("one")); err != nil {
		t.Fatal(err)
	}
	blockedCtx, cancel := context.WithCancel(ctx)
	sent := make(chan error, 1)
	go func() { sent <- client.Send(blockedCtx, []byte("two")) }()
	cancel()
	if err := <-sent; !errors.Is(err, ErrCanceled) {
		t.Fatalf("blocked send: %v", err)
	}
	if got, err := server.Receive(ctx); err != nil || string(got) != "one" {
		t.Fatalf("queued frame %q: %v", got, err)
	}
	_ = client.Close()
	_ = server.Close()
	accepted := make(chan error, 1)
	go func() { _, err := l.Accept(ctx); accepted <- err }()
	_ = l.Close()
	if err := <-accepted; !errors.Is(err, ErrClosed) {
		t.Fatalf("blocked accept: %v", err)
	}

	l2, err := m.Listen(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	queued, err := m.Dial(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	dialed := make(chan error, 1)
	go func() { _, err := m.Dial(ctx, e); dialed <- err }()
	_ = l2.Close()
	if err := <-dialed; !errors.Is(err, ErrEndpointUnavailable) {
		t.Fatalf("blocked dial: %v", err)
	}
	_ = queued.Close()
}
