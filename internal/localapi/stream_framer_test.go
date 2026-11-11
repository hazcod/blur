package localapi

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

type deadlineRaceConn struct {
	readStarted    chan struct{}
	cancelDeadline chan struct{}
	releaseCancel  chan struct{}
	restored       chan struct{}
	releaseOnce    sync.Once
	mu             sync.Mutex
	deadlineClears int
	reads          [][]byte
}

func newDeadlineRaceConn() *deadlineRaceConn {
	return &deadlineRaceConn{
		readStarted:    make(chan struct{}),
		cancelDeadline: make(chan struct{}),
		releaseCancel:  make(chan struct{}),
		restored:       make(chan struct{}),
		reads:          [][]byte{{0, 0, 0, 1}, {'x'}},
	}
}

func (c *deadlineRaceConn) Read(p []byte) (int, error) {
	c.mu.Lock()
	if len(c.reads) == 0 {
		c.mu.Unlock()
		return 0, io.EOF
	}
	data := c.reads[0]
	c.reads = c.reads[1:]
	first := len(c.reads) == 1
	c.mu.Unlock()
	if first {
		close(c.readStarted)
		<-c.cancelDeadline
	}
	return copy(p, data), nil
}

func (c *deadlineRaceConn) Write(p []byte) (int, error)      { return len(p), nil }
func (c *deadlineRaceConn) Close() error                     { return nil }
func (c *deadlineRaceConn) LocalAddr() net.Addr              { return testFramerAddr("local") }
func (c *deadlineRaceConn) RemoteAddr() net.Addr             { return testFramerAddr("remote") }
func (c *deadlineRaceConn) SetDeadline(time.Time) error      { return nil }
func (c *deadlineRaceConn) SetWriteDeadline(time.Time) error { return nil }
func (c *deadlineRaceConn) SetReadDeadline(deadline time.Time) error {
	if deadline.IsZero() {
		c.mu.Lock()
		c.deadlineClears++
		clears := c.deadlineClears
		c.mu.Unlock()
		if clears == 2 {
			close(c.restored)
		}
		return nil
	}
	close(c.cancelDeadline)
	<-c.releaseCancel
	return nil
}

func (c *deadlineRaceConn) release() { c.releaseOnce.Do(func() { close(c.releaseCancel) }) }

type testFramerAddr string

func (a testFramerAddr) Network() string { return "test" }
func (a testFramerAddr) String() string  { return string(a) }

func TestReceiveWaitsForCancellationDeadlineCallbackBeforeReset(t *testing.T) {
	conn := newDeadlineRaceConn()
	defer conn.release()
	framer := &streamConnFramer{conn: conn, maxBytes: 16}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := framer.Receive(ctx)
		done <- err
	}()

	<-conn.readStarted
	cancel()
	<-conn.cancelDeadline
	select {
	case err := <-done:
		t.Fatalf("Receive returned while its cancellation callback was still setting a deadline: %v", err)
	case <-conn.restored:
		t.Fatal("Receive reset the deadline before the cancellation callback finished")
	case <-time.After(50 * time.Millisecond):
	}

	conn.release()
	select {
	case err := <-done:
		if !errors.Is(err, ErrCanceled) {
			t.Fatalf("Receive error = %v, want ErrCanceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Receive did not finish after the cancellation callback completed")
	}
	select {
	case <-conn.restored:
	case <-time.After(time.Second):
		t.Fatal("Receive did not restore the read deadline")
	}
}
