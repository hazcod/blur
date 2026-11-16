package client

import (
	"context"
	"slices"

	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/knowledge/enrich"
)

func (c *Client) ConfirmInferredFact(ctx context.Context, entityID string, expected knowledge.Fact) error {
	if c == nil {
		return ErrNotConfigured
	}
	epoch, stable := c.BeginMemoryRead()
	if !stable {
		return ErrMemoryChanging
	}
	return c.confirmInferredFactAtEpoch(ctx, entityID, expected, epoch)
}

func (c *Client) confirmInferredFactAtEpoch(parent context.Context, entityID string, expected knowledge.Fact, epoch uint64) error {
	if c == nil {
		return ErrNotConfigured
	}
	repository, ok := c.data.(enrich.FactConfirmer)
	if !ok {
		return ErrNotConfigured
	}
	expected.Sources = slices.Clone(expected.Sources)
	ctx, done, err := c.beginOperation(parent, shortOperationTimeout)
	if err != nil {
		return err
	}
	defer done()
	err = c.CommitDerivedMemory(epoch, func() error { return repository.ConfirmInferredFact(ctx, entityID, expected) })
	if err == nil {
		c.NotifyMemoryChanging()
		c.NotifyMemoryStable()
	}
	return err
}
