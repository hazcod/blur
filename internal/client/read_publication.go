package client

import (
	"context"
	"github.com/hazcod/blur/internal/storage"
)

// memoryReadPublication binds loaded values to both Client mutations and the
// exact durable commit they were read from. Its callbacks only assign state.
type memoryReadPublication struct {
	memoryEpoch uint64
	fence       storage.ReadPublicationFence
	epoch       uint64
}

func (c *Client) beginMemoryPublication() (memoryReadPublication, bool) {
	var fence storage.ReadPublicationFence
	if c != nil {
		fence, _ = c.data.(storage.ReadPublicationFence)
	}
	return c.beginPublication(fence)
}

func (c *Client) beginVoicePublication() (memoryReadPublication, bool) {
	if c != nil {
		if voice, ok := c.data.(storage.VoiceReadPublicationFence); ok {
			return c.beginPublication(voicePublicationFence{voice})
		}
	}
	return c.beginMemoryPublication()
}

func (c *Client) beginPublication(fence storage.ReadPublicationFence) (memoryReadPublication, bool) {
	if c == nil {
		return memoryReadPublication{}, false
	}
	if !c.lifeMu.TryRLock() {
		return memoryReadPublication{}, false
	}
	defer c.lifeMu.RUnlock()
	if c.lifeClosed {
		return memoryReadPublication{}, false
	}
	if !c.memoryChangeMu.TryRLock() {
		return memoryReadPublication{}, false
	}
	epoch, stable := c.memoryMutationEpoch, c.memoryMutationDepth == 0
	c.memoryChangeMu.RUnlock()
	read := memoryReadPublication{memoryEpoch: epoch}
	if !stable {
		return read, false
	}
	if fence != nil {
		read.fence = fence
		read.epoch, stable = fence.BeginPublicationRead()
	}
	return read, stable
}

// Lock order matches source publication. No storage/Client calls or IO may run
// inside install; failed/active durable writes are rejected without waiting.
func (r memoryReadPublication) publish(c *Client, install func()) bool {
	if install == nil || c == nil {
		return false
	}
	// Close and UI installation linearize here; no queued read survives Close.
	if !c.lifeMu.TryRLock() {
		return false
	}
	defer c.lifeMu.RUnlock()
	if c.lifeClosed {
		return false
	}
	if !c.memoryChangeMu.TryRLock() {
		return false
	}
	defer c.memoryChangeMu.RUnlock()
	if c.memoryMutationDepth != 0 || c.memoryMutationEpoch != r.memoryEpoch {
		return false
	}
	accepted := false
	if r.fence != nil {
		accepted = r.fence.PublishRead(r.epoch, install)
	} else {
		install()
		accepted = true
	}
	return accepted
}

type voicePublicationFence struct {
	storage.VoiceReadPublicationFence
}

func (f voicePublicationFence) BeginPublicationRead() (uint64, bool) {
	return f.BeginVoicePublicationRead()
}
func (f voicePublicationFence) PublishRead(epoch uint64, install func()) bool {
	return f.PublishVoiceRead(epoch, install)
}
func (f voicePublicationFence) AppendQuestionAtRead(ctx context.Context, record storage.QuestionRecord, epoch uint64) (uint64, bool, error) {
	if writer, ok := f.VoiceReadPublicationFence.(storage.VoiceQuestionHistoryReadWriter); ok {
		return writer.AppendQuestionAtVoiceRead(ctx, record, epoch)
	}
	return 0, false, nil // Optional convenience history cannot refresh an unproved read.
}

type memoryAnswerPublication struct {
	client *Client
	read   memoryReadPublication
}

func (p *memoryAnswerPublication) Publish(install func()) bool {
	return p != nil && p.read.publish(p.client, install)
}
