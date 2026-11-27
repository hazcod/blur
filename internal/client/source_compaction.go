package client

import (
	"context"
	"errors"
	"strings"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/knowledge/enrich"
)

// Complete source-span compactions are an optional recap projection. If their
// concatenation expands the original, keep the original rather than dropping
// summaries to meet a separate whole-source limit. Exact recall stays raw.
func (c *Client) compactRecapSources(ctx context.Context, evidence []assistant.Evidence) ([]assistant.Evidence, error) {
	reader, ok := c.data.(enrich.CompactionReader)
	repo, hasProgress := c.data.(enrich.Repository)
	if !ok || !hasProgress {
		return evidence, nil
	}
	out := append([]assistant.Evidence(nil), evidence...)
	for i, item := range out {
		if item.Layer != assistant.LayerSource {
			continue
		}
		p, found, err := repo.GetEnrichmentProgress(ctx, item.SourceID)
		if err != nil {
			return nil, err
		}
		if !found || !p.Complete || p.Declined || p.CompactionParts == 0 || p.Next != len(item.Text) {
			continue
		}
		var text strings.Builder
		after, covered := 0, 0
		usable := true
		for {
			page, err := reader.ListSourceCompactions(ctx, item.SourceID, after, 32)
			if err != nil {
				if errors.Is(err, enrich.ErrStale) {
					usable = false
					break
				}
				return nil, err
			}
			for _, block := range page.Blocks {
				if block.SourceID != item.SourceID || block.ModelKey != p.ModelKey || block.Index != after || block.Start < 0 || block.Start > len(item.Text) || block.Start > covered && strings.TrimSpace(item.Text[covered:block.Start]) != "" || block.End <= block.Start || block.End > len(item.Text) {
					usable = false
					break
				}
				if text.Len()+len(block.Text)+1 > len(item.Text) {
					usable = false
					break
				}
				if text.Len() > 0 {
					text.WriteByte('\n')
				}
				text.WriteString(block.Text)
				covered = block.End
				after++
			}
			if !usable || !page.HasMore {
				break
			}
			if page.Next != after || len(page.Blocks) == 0 {
				usable = false
				break
			}
		}
		if !usable || strings.TrimSpace(item.Text[covered:]) != "" || after != p.CompactionParts || text.Len() == 0 {
			continue
		}
		item.Text, item.Layer, item.Origin = text.String(), assistant.LayerCompaction, assistant.OriginInferred
		item.SourceIDs = []string{item.SourceID}
		item.MatchStart, item.MatchEnd = 0, 0
		out[i] = item
	}
	return out, nil
}
