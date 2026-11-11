//go:build darwin || linux

package browsercapture

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/hazcod/blur/internal/localapi"
)

const maxBridgeClients = 4
const bridgeReadTimeout = 2 * time.Second

var errServerState = errors.New("browser capture: invalid server state")

// UnixServer accepts one bounded authenticated frame per same-user connection.
// It never queues payloads; each frame is cleared after synchronous handling.
type UnixServer struct {
	mu        sync.Mutex
	transport *localapi.UnixTransport
	endpoint  localapi.EndpointDescriptor
	receiver  *Receiver
	listener  localapi.LocalListener
	cancel    context.CancelFunc
	done      chan struct{}
	workers   sync.WaitGroup
	started   bool
	stopped   bool
}

func NewUnixServer(transport *localapi.UnixTransport, endpoint localapi.EndpointDescriptor, receiver *Receiver) (*UnixServer, error) {
	if transport == nil || receiver == nil || endpoint.Kind != localapi.EndpointUnix || endpoint.Validate() != nil {
		return nil, errServerState
	}
	return &UnixServer{transport: transport, endpoint: endpoint, receiver: receiver, done: make(chan struct{})}, nil
}

func (s *UnixServer) Start(parent context.Context) error {
	if s == nil || parent == nil || parent.Err() != nil {
		return localapi.ErrCanceled
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started || s.stopped {
		return errServerState
	}
	listener, err := s.transport.Listen(parent, s.endpoint)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(parent)
	s.listener, s.cancel, s.started = listener, cancel, true
	go s.serve(ctx)
	return nil
}

func (s *UnixServer) serve(ctx context.Context) {
	defer close(s.done)
	defer s.listener.Close()
	semaphore := make(chan struct{}, maxBridgeClients)
	for {
		conn, err := s.listener.Accept(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			continue
		}
		select {
		case semaphore <- struct{}{}:
			s.workers.Add(1)
			go func() {
				defer s.workers.Done()
				defer func() { <-semaphore }()
				defer conn.Close()
				requestCtx, cancel := context.WithTimeout(ctx, bridgeReadTimeout)
				defer cancel()
				frame, err := conn.Receive(requestCtx)
				if err != nil {
					return
				}
				defer clear(frame)
				_ = s.receiver.Accept(requestCtx, frame)
			}()
		default:
			_ = conn.Close()
		}
	}
}

// Stop closes the receive gate before listener shutdown, then waits until no
// accepted page payload can still be persisted.
func (s *UnixServer) Stop() error {
	if s == nil {
		return nil
	}
	s.receiver.Pause()
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return nil
	}
	s.stopped = true
	listener, cancel, started := s.listener, s.cancel, s.started
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	var err error
	if listener != nil {
		err = listener.Close()
	}
	if started {
		<-s.done
	}
	s.workers.Wait()
	return err
}
