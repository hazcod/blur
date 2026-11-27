package semantic

import (
	"context"
	"encoding/binary"

	"github.com/hazcod/blur/internal/capture"
)

const MaxProgressBytes = MaxDocumentBytes + 12

// Progress retains a successfully embedded prefix without publishing an
// incomplete Document. Document.TextBytes still identifies the complete source;
// its chunks cover only the prefix committed so far.
type Progress struct {
	Document Document
}

type ProgressRepository interface {
	GetSemanticProgress(context.Context, string, string) (Progress, bool, error)
	PutSemanticProgress(context.Context, Progress) error
}

func (p Progress) Validate() error {
	document := p.Document
	if document.TextBytes < 1 || document.TextBytes > capture.MaxObservationTextBytes || document.SkipReason != "" || len(document.Chunks) == 0 || document.Chunks[len(document.Chunks)-1].End > document.TextBytes {
		return ErrInvalid
	}
	document.TextBytes = document.Chunks[len(document.Chunks)-1].End
	return document.Validate()
}

// EncodeProgress reuses the bounded vector codec for the covered prefix and
// authenticates the original full-source size in an enclosing record.
func EncodeProgress(progress Progress) ([]byte, error) {
	if progress.Validate() != nil {
		return nil, ErrInvalid
	}
	document := progress.Document
	document.TextBytes = document.Chunks[len(document.Chunks)-1].End
	prefix, err := EncodeDocument(document)
	if err != nil {
		return nil, err
	}
	defer clear(prefix)
	encoded := make([]byte, 12+len(prefix))
	copy(encoded, "BLRSEP01")
	binary.BigEndian.PutUint32(encoded[8:12], uint32(progress.Document.TextBytes))
	copy(encoded[12:], prefix)
	return encoded, nil
}

func DecodeProgress(encoded []byte) (Progress, error) {
	if len(encoded) < 12 || len(encoded) > MaxProgressBytes || string(encoded[:8]) != "BLRSEP01" {
		return Progress{}, ErrInvalid
	}
	document, err := DecodeDocument(encoded[12:])
	if err != nil {
		return Progress{}, err
	}
	document.TextBytes = int(binary.BigEndian.Uint32(encoded[8:12]))
	progress := Progress{Document: document}
	if progress.Validate() != nil {
		clearDocument(document)
		return Progress{}, ErrInvalid
	}
	return progress, nil
}
