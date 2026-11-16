package client

import (
	"context"
	"sync"

	"github.com/hazcod/blur/internal/semantic"
)

type memoryIndexController struct {
	configMu            sync.Mutex
	mu                  sync.RWMutex
	path                string
	sources             semantic.SourceReader
	repository          semantic.Repository
	options             semantic.Options
	settings            MemoryIndexSettings
	service             *semantic.Service
	needsRepair, closed bool
}

func newMemoryIndexController(path string, sources semantic.SourceReader, repository semantic.Repository, options semantic.Options) *memoryIndexController {
	c := &memoryIndexController{path: path, sources: sources, repository: repository, options: options, settings: DefaultMemoryIndexSettings()}
	settings, err := readMemoryIndexSettings(path)
	if err != nil {
		c.needsRepair = true
		return c
	}
	c.settings = settings
	if settings.Enabled {
		c.service, err = c.createService(settings)
		if err != nil {
			c.needsRepair = true
		} else {
			c.service.Start()
		}
	}
	return c
}

func (c *memoryIndexController) createService(settings MemoryIndexSettings) (*semantic.Service, error) {
	e, err := semantic.NewOllamaEmbedder(settings.Endpoint, settings.Model, settings.Dimensions)
	if err != nil {
		return nil, errMemoryIndexSettings
	}
	s, err := semantic.NewService(c.sources, c.repository, e, c.options)
	if err != nil {
		e.CloseIdleConnections()
		return nil, errMemoryIndexSettings
	}
	return s, nil
}

func (c *memoryIndexController) Configure(ctx context.Context, settings MemoryIndexSettings) error {
	if c == nil || ctx == nil || validateMemoryIndexSettings(settings) != nil {
		return errMemoryIndexSettings
	}
	c.configMu.Lock()
	defer c.configMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.RLock()
	closed := c.closed
	c.mu.RUnlock()
	if closed {
		return errMemoryIndexSettings
	}
	var next *semantic.Service
	if settings.Enabled {
		var err error
		next, err = c.createService(settings)
		if err != nil {
			return err
		}
	}
	if err := writeMemoryIndexSettings(c.path, settings); err != nil {
		if next != nil {
			next.Close()
		}
		return err
	}
	c.mu.Lock()
	old := c.service
	c.service, c.settings, c.needsRepair = next, settings, false
	c.mu.Unlock()
	if old != nil {
		old.Close()
	}
	if next != nil {
		next.Start()
	}
	if c.options.OnChange != nil {
		c.options.OnChange()
	}
	return nil
}

func (c *memoryIndexController) Settings() MemoryIndexSettings {
	if c == nil {
		return MemoryIndexSettings{}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.settings
}

func (c *memoryIndexController) Status() semantic.Status {
	if c == nil {
		return semantic.Status{State: "unavailable"}
	}
	c.mu.RLock()
	s, repair := c.service, c.needsRepair
	c.mu.RUnlock()
	if repair {
		return semantic.Status{State: "unavailable", ErrorCode: "settings_invalid"}
	}
	if s == nil {
		return semantic.Status{State: "disabled"}
	}
	return s.Status()
}

func (c *memoryIndexController) Search(ctx context.Context, text string, query semantic.Query) ([]semantic.Hit, error) {
	if c == nil {
		return nil, semantic.ErrUnavailable
	}
	c.mu.RLock()
	s := c.service
	c.mu.RUnlock()
	if s == nil {
		return nil, semantic.ErrUnavailable
	}
	return s.Search(ctx, text, query)
}

// RankCandidates uses the same local embedding generation as saved-source
// search. Mutable task records are ranked in memory, so old titles and removed
// reminders cannot survive as independently persisted search copies.
func (c *memoryIndexController) RankCandidates(ctx context.Context, question string, texts []string) ([]float32, error) {
	if c == nil {
		return nil, semantic.ErrUnavailable
	}
	c.mu.RLock()
	s := c.service
	c.mu.RUnlock()
	if s == nil {
		return nil, semantic.ErrUnavailable
	}
	return s.RankCandidates(ctx, question, texts)
}

func (c *memoryIndexController) Wake() {
	if c == nil {
		return
	}
	c.mu.RLock()
	s := c.service
	c.mu.RUnlock()
	if s != nil {
		s.Wake()
	}
}

func (c *memoryIndexController) WakeCaptures() {
	if c == nil {
		return
	}
	c.mu.RLock()
	s := c.service
	c.mu.RUnlock()
	if s != nil {
		s.WakeCaptures()
	}
}

func (c *memoryIndexController) NotifySource(sourceID string) {
	if c == nil {
		return
	}
	c.mu.RLock()
	s := c.service
	c.mu.RUnlock()
	if s != nil {
		s.NotifySource(sourceID)
	}
}

func (c *memoryIndexController) Invalidate() {
	if c == nil {
		return
	}
	// A deletion may not pass this hook while configuration is swapping in a
	// fresh cache. No controller lock is held by service callbacks.
	c.configMu.Lock()
	defer c.configMu.Unlock()
	c.mu.RLock()
	s := c.service
	c.mu.RUnlock()
	if s != nil {
		s.Invalidate()
	}
}

func (c *memoryIndexController) Close() {
	if c == nil {
		return
	}
	c.configMu.Lock()
	defer c.configMu.Unlock()
	c.mu.Lock()
	s := c.service
	c.service = nil
	c.closed = true
	c.mu.Unlock()
	if s != nil {
		s.Close()
	}
}

func (c *Client) MemoryIndexSettings() MemoryIndexSettings {
	if c == nil {
		return MemoryIndexSettings{}
	}
	return c.memoryIndex.Settings()
}

func (c *Client) MemoryIndexStatus() semantic.Status {
	if c == nil {
		return semantic.Status{State: "unavailable"}
	}
	return c.memoryIndex.Status()
}

func (c *Client) ConfigureMemoryIndex(ctx context.Context, settings MemoryIndexSettings) error {
	if c == nil || ctx == nil || ctx.Err() != nil {
		return errMemoryIndexSettings
	}
	if c.memoryIndex == nil {
		if !settings.Enabled {
			return nil
		}
		return errMemoryIndexSettings
	}
	return c.memoryIndex.Configure(ctx, settings)
}

func (c *Client) MemoryIndexChanges() <-chan struct{} {
	if c == nil {
		return nil
	}
	return c.memoryIndexChanges
}

func (c *Client) notifyMemoryIndexChanged() {
	if c == nil {
		return
	}
	select {
	case c.memoryIndexChanges <- struct{}{}:
	default:
	}
}
