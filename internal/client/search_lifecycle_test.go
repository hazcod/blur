package client

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/search"
)

type searchLifecycleCall func(context.Context, *Client) error

func memorySearchLifecycleCalls() map[string]searchLifecycleCall {
	return map[string]searchLifecycleCall{
		"memory": func(ctx context.Context, c *Client) error {
			_, err := c.SearchMemoryPageContext(ctx, "laundry")
			return err
		},
		"mcp": func(ctx context.Context, c *Client) error {
			_, err := c.HandleMCPRead(ctx, "search_memory", json.RawMessage(`{"query":"laundry","limit":8}`))
			return err
		},
	}
}

func TestMemorySearchCloseCancelsAndJoinsActualModelWork(t *testing.T) {
	for name, call := range memorySearchLifecycleCalls() {
		t.Run(name, func(t *testing.T) {
			c, _, _, model := singleSearchFixture(t)
			entered, release := make(chan context.Context, 1), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			model.respond = func(ctx context.Context, _, _ string) (string, error) {
				entered <- ctx
				<-release // A canceled provider still owns its actual operation.
				return "", ctx.Err()
			}
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			finished := make(chan error, 1)
			go func() { finished <- call(parent, c) }()
			var request context.Context
			select {
			case request = <-entered:
			case err := <-finished:
				t.Fatal("fake model was not reached", err)
			case <-time.After(time.Second):
				t.Fatal("fake model did not start")
			}
			closed := make(chan struct{})
			go func() { c.Close(); close(closed) }()
			select {
			case <-request.Done():
			case <-time.After(time.Second):
				cancel()
				unblock()
				if err := <-finished; !errors.Is(err, context.Canceled) {
					t.Error("cleanup Search cancellation was hidden", err)
				}
				<-closed
				t.Fatal("Close did not cancel Search inference")
			}
			select {
			case <-closed:
				t.Error("Close returned before the actual Search inference retired")
			default:
			}
			unblock()
			if err := <-finished; !errors.Is(err, context.Canceled) {
				t.Fatal("Search cancellation was hidden", err)
			}
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("Close did not join retired Search")
			}
		})
	}
}

func TestClosedSearchRejectsNewModelWorkAndKeepsCallerCancellation(t *testing.T) {
	for name, call := range memorySearchLifecycleCalls() {
		t.Run(name, func(t *testing.T) {
			c, _, _, model := singleSearchFixture(t)
			c.Close()
			if err := call(context.Background(), c); !errors.Is(err, ErrClientClosed) {
				t.Fatal("closed Search accepted work", err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := call(ctx, c); !errors.Is(err, context.Canceled) {
				t.Fatal("caller cancellation lost to closed admission", err)
			}
			if model.calls != 0 {
				t.Fatal("closed Search invoked a model")
			}
		})
	}
}

type lifecycleExactData struct {
	Data
	entered chan context.Context
	release chan struct{}
}

func (d *lifecycleExactData) SearchExact(ctx context.Context, _ search.ExactQuery) (search.ExactPage, error) {
	d.entered <- ctx
	<-d.release
	return search.ExactPage{}, ctx.Err()
}

func TestExactSearchUsesClientLifetime(t *testing.T) {
	d := &lifecycleExactData{Data: &testData{}, entered: make(chan context.Context, 1), release: make(chan struct{})}
	c := NewClient(d)
	defer c.Close()
	finished := make(chan error, 1)
	go func() { _, err := c.SearchContext(context.Background(), "synthetic"); finished <- err }()
	ctx := <-d.entered
	closed := make(chan struct{})
	go func() { c.Close(); close(closed) }()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		close(d.release)
		if err := <-finished; !errors.Is(err, context.Canceled) {
			t.Error("cleanup exact Search cancellation was hidden", err)
		}
		<-closed
		t.Fatal("Close did not cancel exact retrieval")
	}
	close(d.release)
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatal("exact Search cancellation was hidden", err)
	}
	<-closed
	if _, err := c.SearchContext(context.Background(), "synthetic"); !errors.Is(err, ErrClientClosed) {
		t.Fatal("exact Search accepted work after Close", err)
	}
}
