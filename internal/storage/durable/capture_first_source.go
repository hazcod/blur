package durable

import "github.com/hazcod/blur/internal/capture"

// Publication returns the first-part identity before optional search work.
// Install exactly that bounded membership receipt and an empty proof root in
// the publication transaction. Format0 leaves token coverage pending; the
// ordinary index worker replaces the empty proof before processing terms.
func (tx *Tx) ensureCaptureFirstSourceLocked(id string, edition captureEditionRecord, summary capture.CaptureSummary) error {
	if summary.PartCount() == 0 {
		return nil
	}
	part, err := tx.store.captureMemberPartReader(tx.writer, edition, summary, 0)
	if err != nil {
		return err
	}
	sourceID := capturePartSourceID(id, edition.Publication, 0)
	mapping, mapped, err := tx.store.captureIndexPartReader(tx.writer, sourceID)
	if err != nil {
		return err
	}
	expected := captureIndexPart{Version: captureIndexVersion, EditionID: id, Publication: edition.Publication, Index: 0, Bytes: len(part.Text()), Format: mapping.Format}
	expected.MetadataIndexed = mapping.MetadataIndexed
	key := tx.capturePartSearch().store.searchReverseKey(sourceID)
	defer clear(key)
	proved, err := tx.writer.Has(key)
	if err != nil {
		return mapStorePhysicalError(err)
	}
	if proved != mapped || mapped && mapping != expected {
		return ErrCorrupt
	}
	if mapped {
		return nil
	}
	expected.Format = 0
	sourceBytes := len(part.Text()) + len(summary.Header().AppID()) + len(summary.Header().Domain())
	return tx.initializeCapturePartIndexLocked(sourceID, sourceBytes, expected)
}
