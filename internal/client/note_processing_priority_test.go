package client

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/model"
	"github.com/hazcod/blur/internal/notes"
)

type priorityNoteExtractor struct {
	calls    atomic.Int32
	entered  chan int
	contexts chan context.Context
	release  chan struct{}
	once     sync.Once
}

func (e *priorityNoteExtractor) Extract(ctx context.Context, _ capture.SanitizedObservation) ([]notes.Proposal, error) {
	call := int(e.calls.Add(1))
	e.contexts <- ctx
	e.entered <- call
	if call == 1 {
		<-e.release
		return nil, model.ErrBackgroundPreempted
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return []notes.Proposal{{Kind: notes.FollowUp, Text: "Send the revised draft"}}, nil
}
func (e *priorityNoteExtractor) unblock() { e.once.Do(func() { close(e.release) }) }
func TestPreemptedBackgroundNoteRetainsSameSourceAndRetiresOnInvalidation(t *testing.T) {
	for _, operation := range []string{"retry", "deletion", "policy", "close"} {
		t.Run(operation, func(t *testing.T) {
			c, store, old, _ := noteProcessingFixture(t)
			old.unblock()
			extractor := &priorityNoteExtractor{entered: make(chan int, 8), contexts: make(chan context.Context, 8), release: make(chan struct{})}
			defer extractor.unblock()
			c.notes.(*notes.Service).SetExtractor(extractor)
			receipt, err := c.AddNote("After the meeting send the revised draft.")
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-extractor.entered:
			case <-time.After(time.Second):
				t.Fatal("initial extraction absent")
			}
			inference := <-extractor.contexts
			changed := make(chan struct{})
			if operation != "retry" {
				go func() {
					switch operation {
					case "deletion":
						_, _ = c.DeleteSource(receipt.SourceID)
					case "policy":
						_ = c.ConfigureLocalAI(context.Background(), LocalAISettings{})
					case "close":
						c.Close()
					}
					close(changed)
				}()
				select {
				case <-inference.Done():
				case <-time.After(time.Second):
					t.Fatal("invalidation did not cancel active request")
				}
			}
			extractor.unblock()
			if operation == "retry" {
				select {
				case n := <-extractor.entered:
					if n != 2 {
						t.Fatal("deferred source duplicated")
					}
				case <-time.After(2 * time.Second):
					t.Fatal("preempted queued note was lost")
				}
				waitNoteProcessing(t, c, func(s NoteProcessingStatus) bool { return s.State == "ready" && s.Pending == 0 })
				if _, err := c.AddNote("After the meeting send the revised draft."); err != nil || extractor.calls.Load() != 2 {
					t.Fatal("completed deferred note lost dedup", err)
				}
				if _, found, err := store.GetObservation(context.Background(), receipt.SourceID); err != nil || !found {
					t.Fatal("deferred note lost source", err)
				}
			} else {
				select {
				case <-changed:
				case <-time.After(2 * time.Second):
					t.Fatal("invalidation did not join extraction")
				}
				c.Close()
				if extractor.calls.Load() != 1 {
					t.Fatal("deferred note retried after invalidation")
				}
			}
		})
	}
}
