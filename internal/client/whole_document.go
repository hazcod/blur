package client

import (
	"context"
	"errors"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/storage"
)

var ErrWholeDocumentUnavailable = errors.New("client: whole-document capture is unavailable")

// streamWholeDocumentParts yields one authenticated source part at a time.
// Publication binding and the capture chain are rechecked on each bounded
// page, so replacement, deletion, or a corrupt cursor cannot mix editions.
func (c *Client) StreamWholeDocumentParts(ctx context.Context, source assistant.Evidence, yield func(assistant.Evidence) error) error {
	if c == nil || c.data == nil || ctx == nil || yield == nil {
		return ErrInvalidRequest
	}
	provenance := source.Document
	if provenance.EditionID == "" || provenance.Publication == "" || provenance.PartCount < 1 || provenance.TotalBytes < 1 {
		return ErrWholeDocumentUnavailable
	}
	reader, ok := c.data.(storage.CapturePartsPageReader)
	if !ok {
		return ErrWholeDocumentUnavailable
	}
	var after int64 = -1
	var parts, bytes int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		page, err := reader.ReadCapturePartsPage(ctx, storage.CapturePartsPageRequest{
			EditionID: provenance.EditionID, Publication: provenance.Publication,
			After: after, Limit: storage.MaxCapturePartsPageRows,
		})
		if err != nil {
			return err
		}
		if len(page.Parts) == 0 && page.HasMore || page.Next < after || page.Next >= provenance.PartCount {
			return ErrInvalidRequest
		}
		for _, part := range page.Parts {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := part.Validate(); err != nil || part.Index() != after+1 {
				return ErrInvalidRequest
			}
			id := capture.CapturePartSourceID(provenance.EditionID, provenance.Publication, part.Index())
			if id == "" {
				return ErrInvalidRequest
			}
			partProvenance := provenance
			partProvenance.PartIndex = part.Index()
			partProvenance.Start = part.Offset()
			partProvenance.End = part.Offset() + int64(len(part.Text()))
			item := assistant.Evidence{
				SourceID: id, AppID: source.AppID, AppName: source.AppName, Domain: source.Domain,
				Text: part.Text(), Partial: source.Partial, CapturedAt: source.CapturedAt,
				TimeUnknown: source.TimeUnknown, Document: partProvenance,
			}
			if err := yield(item); err != nil {
				return err
			}
			after = part.Index()
			parts++
			bytes += int64(len(part.Text()))
		}
		if !page.HasMore {
			break
		}
		if page.Next != after || after < 0 {
			return ErrInvalidRequest
		}
	}
	if parts != provenance.PartCount || bytes != provenance.TotalBytes || after != provenance.PartCount-1 {
		return ErrInvalidRequest
	}
	return ctx.Err()
}
