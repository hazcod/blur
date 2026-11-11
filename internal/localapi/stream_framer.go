package localapi

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"sync"
	"time"
)

const maxStreamFrameBytes = 8 << 20

// streamConnFramer applies the same bounded length-prefixed protocol to any
// deadline-capable byte-stream connection (Unix sockets and Windows pipes).
type streamConnFramer struct {
	conn     net.Conn
	maxBytes int
	readMu   sync.Mutex
	writeMu  sync.Mutex
}

func (c *streamConnFramer) Send(ctx context.Context, frame []byte) error {
	if contextErr(ctx) != nil {
		return ErrCanceled
	}
	if c == nil || c.conn == nil {
		return ErrClosed
	}
	if len(frame) == 0 || len(frame) > c.maxBytes {
		return ErrFrameTooLarge
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.conn.SetWriteDeadline(deadline)
	} else {
		_ = c.conn.SetWriteDeadline(time.Time{})
	}
	deadlineCallbackDone := make(chan struct{})
	stopDeadlineCallback := context.AfterFunc(ctx, func() {
		defer close(deadlineCallbackDone)
		_ = c.conn.SetWriteDeadline(time.Now())
	})
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(frame)))
	err := writeAll(c.conn, header[:])
	if err == nil {
		err = writeAll(c.conn, frame)
	}
	finishDeadlineCallback(stopDeadlineCallback, deadlineCallbackDone)
	_ = c.conn.SetWriteDeadline(time.Time{})
	if ctx.Err() != nil {
		return ErrCanceled
	}
	if err != nil {
		return ErrClosed
	}
	return nil
}

func (c *streamConnFramer) Receive(ctx context.Context) ([]byte, error) {
	if contextErr(ctx) != nil {
		return nil, ErrCanceled
	}
	if c == nil || c.conn == nil {
		return nil, ErrClosed
	}
	c.readMu.Lock()
	defer c.readMu.Unlock()
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.conn.SetReadDeadline(deadline)
	} else {
		_ = c.conn.SetReadDeadline(time.Time{})
	}
	deadlineCallbackDone := make(chan struct{})
	stopDeadlineCallback := context.AfterFunc(ctx, func() {
		defer close(deadlineCallbackDone)
		_ = c.conn.SetReadDeadline(time.Now())
	})
	var header [4]byte
	_, err := io.ReadFull(c.conn, header[:])
	if err != nil {
		finishDeadlineCallback(stopDeadlineCallback, deadlineCallbackDone)
		_ = c.conn.SetReadDeadline(time.Time{})
		if ctx.Err() != nil {
			return nil, ErrCanceled
		}
		return nil, ErrClosed
	}
	length := binary.BigEndian.Uint32(header[:])
	if length == 0 || uint64(length) > uint64(c.maxBytes) {
		finishDeadlineCallback(stopDeadlineCallback, deadlineCallbackDone)
		_ = c.conn.SetReadDeadline(time.Time{})
		return nil, ErrFrameTooLarge
	}
	frame := make([]byte, int(length))
	_, err = io.ReadFull(c.conn, frame)
	finishDeadlineCallback(stopDeadlineCallback, deadlineCallbackDone)
	_ = c.conn.SetReadDeadline(time.Time{})
	if ctx.Err() != nil {
		clear(frame)
		return nil, ErrCanceled
	}
	if err != nil {
		clear(frame)
		return nil, ErrClosed
	}
	return frame, nil
}

func finishDeadlineCallback(stop func() bool, callbackDone <-chan struct{}) {
	if !stop() {
		<-callbackDone
	}
}

func (c *streamConnFramer) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}
