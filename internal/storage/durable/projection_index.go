package durable

import (
	"bytes"
)

const (
	projectionIndexNamespace = "storage-meta"
	projectionIndexMarkerID  = "projection-index-v1"
)

var projectionIndexMarkerValue = []byte("today-source-date+activity-source-segment:v1")

func (tx *Tx) requireProjectionIndexes() error {
	value, found, err := tx.readIndexValue(projectionIndexNamespace, projectionIndexMarkerID, 128)
	if err != nil {
		return err
	}
	defer clear(value)
	if !found {
		return ErrProjectionIndexUnavailable
	}
	if !bytes.Equal(value, projectionIndexMarkerValue) {
		return ErrCorrupt
	}
	return nil
}
