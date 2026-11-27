package client

import (
	"context"

	"github.com/hazcod/blur/internal/assistant"
)

// beginAIAsk keeps selection and cancellation registration in one policy read
// window. A configuration writer cannot retire a model between those steps.
// The lock is released before retrieval or inference starts.
func (c *Client) beginAIAsk(ctx context.Context) (*assistant.Service, context.Context, func(), error) {
	c.aiPolicyMu.RLock()
	defer c.aiPolicyMu.RUnlock()
	service, ok := c.localAI.askService(c)
	if !ok {
		service = assistant.NewService(c, nil, nil, assistant.ContextTokenLimit)
	}
	askCtx, cancel := context.WithCancel(ctx)
	requestID, registered := c.registerAsk(cancel)
	if !registered {
		cancel()
		return nil, nil, nil, ErrMemoryChanging
	}
	return service, askCtx, func() {
		cancel()
		c.unregisterAsk(requestID)
	}, nil
}

func (c *Client) configureAIPolicy(ctx context.Context, settings LocalAISettings, credential ...string) error {
	opCtx, done, err := c.beginOperation(ctx, noteOperationTimeout)
	if err != nil {
		return err
	}
	defer done()
	c.aiPolicyMu.Lock()
	defer c.aiPolicyMu.Unlock()
	if err := opCtx.Err(); err != nil {
		return err
	}
	c.aiPolicyEpoch++
	c.invalidateNoteProcessing()
	// Do not use NotifyMemoryChanging: model consent is independent of the
	// encrypted memory epoch, source caches, and their mutation notifications.
	cancels := c.askCancellations()
	for _, cancel := range cancels {
		cancel()
	}
	return c.localAI.Configure(opCtx, settings, credential...)
}
