package client

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/golang-gui/goui/layout"
	"github.com/golang-gui/goui/ui"
	"github.com/hazcod/blur/internal/assistant"
)

// Only one recap's source list and one full original remain in view state.
// Preview rows stay bounded even after reading several full originals.
type recapSourceViewState struct {
	anchor      assistant.Evidence
	page        RecapSourcePage
	full        *SearchHit
	loading     string
	error       string
	unavailable bool
	request     uint64
	cancel      context.CancelFunc
}

func (d *desktop) clearRecapSources() {
	if d == nil {
		return
	}
	if d.recapSources.cancel != nil {
		d.recapSources.cancel()
	}
	request := d.recapSources.request + 1
	d.recapSources = recapSourceViewState{request: request}
}

func recapSourceError(err error) string {
	if errors.Is(err, ErrRecapChanged) {
		return "This recap changed or is no longer available. Ask again to see its current sources."
	}
	if errors.Is(err, ErrMemoryChanging) {
		return "Memory changed while these sources were loading. Try again."
	}
	return "Saved sources could not be read. Try again."
}

func (s *recapSourceViewState) fail(err error) {
	s.page = RecapSourcePage{}
	s.full = nil
	s.loading = ""
	s.error = recapSourceError(err)
	s.unavailable = errors.Is(err, ErrRecapChanged)
	s.cancel = nil
}

func (d *desktop) recapSourceRequestCurrent(request, generation uint64, answer *assistant.Result, id string) bool {
	return d != nil && d.page == "Ask" && !d.noteUI.open && d.askResult != nil && d.askResult == answer && d.memoryGeneration == generation && d.recapSources.request == request && d.recapSources.anchor.SourceID == id && slices.ContainsFunc(d.askResult.Sources, func(item assistant.Evidence) bool { return item.SourceID == id })
}

func (d *desktop) loadRecapSources(anchor assistant.Evidence, after string) {
	if d == nil || d.app == nil || d.client == nil {
		return
	}
	if after == "" {
		d.clearRecapSources()
		d.recapSources.anchor = anchor
	}
	if d.recapSources.anchor.SourceID != anchor.SourceID || d.recapSources.loading != "" {
		return
	}
	epoch, stable := d.client.BeginMemoryRead()
	publication, storeStable := d.client.beginRecapPublication()
	if !stable || !storeStable {
		d.recapSources.fail(ErrMemoryChanging)
		d.app.RequestUpdate()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.recapSources.cancel = cancel
	d.recapSources.request++
	request, generation, answer := d.recapSources.request, d.memoryGeneration, d.askResult
	d.recapSources.loading = "Loading saved sources…"
	d.recapSources.error = ""
	d.app.RequestUpdate()
	go func() {
		defer cancel()
		page, err := d.client.RecapSourcesPage(ctx, anchor, after, 8)
		if ctx.Err() != nil {
			return
		}
		d.app.Post(func() {
			if !d.recapSourceRequestCurrent(request, generation, answer, anchor.SourceID) {
				return
			}
			if !d.client.PublishMemoryRead(epoch, func() {
				d.recapSources.cancel = nil
				d.recapSources.loading = ""
				if err != nil {
					d.recapSources.fail(err)
					return
				}
				if !publication.fence.PublishRead(publication.epoch, func() {
					if after != "" {
						if d.recapSources.page.Receipt != page.Receipt {
							d.recapSources.fail(ErrRecapChanged)
							return
						}
						page.Hits = append(d.recapSources.page.Hits, page.Hits...)
					}
					d.recapSources.page = page
				}) {
					d.recapSources.fail(ErrMemoryChanging)
				}
			}) {
				d.recapSources.fail(ErrMemoryChanging)
			}
			d.app.RequestUpdate()
		})
	}()
}

func (d *desktop) loadRecapOriginal(sourceID string) {
	if d == nil || d.app == nil || d.client == nil || d.recapSources.loading != "" {
		return
	}
	anchor, receipt := d.recapSources.anchor, d.recapSources.page.Receipt
	if anchor.SourceID == "" || receipt == "" {
		return
	}
	epoch, stable := d.client.BeginMemoryRead()
	publication, storeStable := d.client.beginRecapPublication()
	if !stable || !storeStable {
		d.recapSources.fail(ErrMemoryChanging)
		d.app.RequestUpdate()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.recapSources.cancel = cancel
	d.recapSources.request++
	request, generation, answer := d.recapSources.request, d.memoryGeneration, d.askResult
	d.recapSources.full = nil
	d.recapSources.loading = "Reading saved source…"
	d.recapSources.error = ""
	d.app.RequestUpdate()
	go func() {
		defer cancel()
		hit, err := d.client.RecapSource(ctx, anchor, receipt, sourceID)
		if ctx.Err() != nil {
			return
		}
		d.app.Post(func() {
			if !d.recapSourceRequestCurrent(request, generation, answer, anchor.SourceID) {
				return
			}
			if !d.client.PublishMemoryRead(epoch, func() {
				d.recapSources.cancel = nil
				d.recapSources.loading = ""
				if err != nil {
					d.recapSources.fail(err)
					return
				}
				if d.recapSources.page.Receipt != receipt {
					d.recapSources.fail(ErrRecapChanged)
					return
				}
				if !publication.fence.PublishRead(publication.epoch, func() {
					d.recapSources.full = &hit
				}) {
					d.recapSources.fail(ErrMemoryChanging)
				}
			}) {
				d.recapSources.fail(ErrMemoryChanging)
			}
			d.app.RequestUpdate()
		})
	}()
}

func (d *desktop) recapSavedSourcesView(anchor assistant.Evidence) ui.View {
	if recapEvidenceName(anchor.Layer) == "" {
		return nil
	}
	if d.recapSources.anchor.SourceID != anchor.SourceID {
		return flatButton("Show saved sources").Enabled(d.client != nil && !d.askLoading).OnClick(func() { d.loadRecapSources(anchor, "") })
	}
	rows := []ui.View{flatButton("Hide saved sources").OnClick(func() { d.clearRecapSources(); d.app.RequestUpdate() })}
	state := &d.recapSources
	if state.error != "" {
		rows = append(rows, wrapLabel(state.error).Style(styleMeta))
		if !state.unavailable {
			rows = append(rows, flatButton("Try sources again").OnClick(func() { d.loadRecapSources(anchor, "") }))
		}
	}
	if state.page.Total > 0 {
		rows = append(rows, wrapLabel(fmt.Sprintf("%d of %d saved supporting sources", len(state.page.Hits), state.page.Total)).Style(styleMeta))
		if state.page.CoverageLimited {
			rows = append(rows, wrapLabel("This recap has limited coverage; its sources may omit saved activity.").Style(styleMeta))
		}
	}
	for _, hit := range state.page.Hits {
		hit := hit
		preview, truncated := searchPreview(hit.Text)
		text := preview
		full := state.full != nil && state.full.SourceID == hit.SourceID
		if full {
			text = state.full.Text
		}
		item := []ui.View{d.memorySourceHeading(hit, sourceKind(hit)), wrapLabel(text), wrapLabel(searchHitCaption(hit)).Style(styleMeta)}
		if hit.Partial {
			item = append(item, wrapLabel("Only part of the original content was captured.").Style(styleMeta))
		}
		if full {
			item = append(item, flatButton("Show source preview").OnClick(func() { d.recapSources.full = nil; d.app.RequestUpdate() }))
		} else if truncated || hit.Truncated {
			item = append(item, flatButton("Read saved source").Enabled(state.loading == "").OnClick(func() { d.loadRecapOriginal(hit.SourceID) }))
		}
		rows = append(rows, ui.VBox(item...).Spacing(6).CrossAlign(layout.CrossStretch))
	}
	if state.loading != "" {
		rows = append(rows, wrapLabel(state.loading).Style(styleMeta))
	}
	if state.page.Next != "" {
		next := state.page.Next
		rows = append(rows, flatButton("Load more saved sources").Enabled(state.loading == "").OnClick(func() { d.loadRecapSources(anchor, next) }))
	}
	return ui.VBox(rows...).Spacing(8).CrossAlign(layout.CrossStretch)
}
