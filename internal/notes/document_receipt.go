package notes

import "context"

// ProcessSavedNoteReceipt creates only explicit local suggestions after an edit.
// Optional model extraction remains on the client's background queue.
func (s *Service) ProcessSavedNoteReceipt(ctx context.Context, sourceID string) (Result, error) {
	if s == nil || ctx == nil {
		return Result{}, ErrInvalid
	}
	r, ok := s.repository.(SavedNoteReader)
	if !ok {
		return Result{}, ErrSourceUnavailable
	}
	o, at, found, err := r.GetObservationWithCaptureTime(ctx, sourceID)
	if err != nil {
		return Result{}, err
	}
	if !found || o.Validate() != nil || o.AppID() != "sh.blur.notes" || !validNoteDomain(o.Domain()) || o.ContentID() != sourceID {
		return Result{}, ErrSourceUnavailable
	}
	result, _, _ := s.prepare(ctx, o, at, false)
	if len(result.Proposals) > 0 {
		if err := s.repository.SaveProposals(ctx, sourceID, result.Proposals); err != nil {
			return result, err
		}
	}
	return result, nil
}
