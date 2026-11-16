package client

import (
	"context"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/storage"
	"strings"
)

// Prefer the complete source timeline when available. The legacy port remains
// an explicit compatibility adapter for old stores and platform fixtures.
func readSourceWindow(ctx context.Context, reader ObservationWindowReader, request storage.ObservationWindowRequest) (storage.SourceEvidenceWindow, error) {
	if sources, ok := reader.(storage.SourceEvidenceWindowReader); ok {
		page, err := sources.ListSourceEvidenceWindow(ctx, request)
		if err != nil {
			return storage.SourceEvidenceWindow{}, err
		}
		if len(page.Sources) != len(page.CapturedAt) || len(page.Sources) > request.Limit {
			return storage.SourceEvidenceWindow{}, ErrInvalidRequest
		}
		used := 0
		for i, source := range page.Sources {
			at := page.CapturedAt[i]
			if source.Validate() != nil || at.Before(request.Start) || !at.Before(request.End) || request.AppID != "" && source.AppID() != request.AppID || request.Domain != "" && source.Domain() != request.Domain {
				return storage.SourceEvidenceWindow{}, ErrInvalidRequest
			}
			for _, excluded := range request.ExcludeAppIDs {
				if strings.EqualFold(source.AppID(), excluded) {
					return storage.SourceEvidenceWindow{}, ErrInvalidRequest
				}
			}
			size := len(source.Text()) + len(source.AppID()) + len(source.Domain())
			if size > request.MaxBytes-used {
				return storage.SourceEvidenceWindow{}, ErrInvalidRequest
			}
			used += size
		}
		return page, nil
	}
	legacy, err := reader.ListObservationWindow(ctx, request)
	if err != nil {
		return storage.SourceEvidenceWindow{}, err
	}
	result := storage.SourceEvidenceWindow{CapturedAt: legacy.CapturedAt, NextCapturedAt: legacy.NextCapturedAt, NextSourceID: legacy.NextSourceID, Bytes: legacy.Bytes, HasMore: legacy.HasMore}
	for _, observation := range legacy.Observations {
		evidence, err := capture.ObservationEvidence(observation)
		if err != nil {
			return storage.SourceEvidenceWindow{}, ErrInvalidRequest
		}
		result.Sources = append(result.Sources, evidence)
	}
	return result, nil
}
