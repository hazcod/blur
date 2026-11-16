package storage

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/hazcod/blur/internal/capture"
)

const MaxCaptureEvidenceCursorBytes = 150
const MaxCapturePartsPageRows = 8

var ErrCaptureCursorChanged = errors.New("durable store: capture results changed")

type CaptureEvidencePageRequest struct {
	After           string
	Limit, MaxBytes int
}
type CaptureEvidencePage struct {
	Sources          []capture.Evidence
	CapturedAt       []time.Time
	Next             string
	HasMore, Pending bool
	Bytes            int
}
type CaptureEvidencePageReader interface {
	ListCaptureEvidencePage(context.Context, CaptureEvidencePageRequest) (CaptureEvidencePage, error)
}

// CapturePartsPageRequest binds a continuation to one immutable publication.
// Every read must revalidate both IDs so a replaced edition cannot be mixed
// into a prior read.
type CapturePartsPageRequest struct {
	EditionID, Publication string
	After                  int64
	Limit                  int
}
type CapturePartsPage struct {
	Parts   []capture.CapturePart
	Next    int64
	HasMore bool
}
type CapturePartsPageReader interface {
	ReadCapturePartsPage(context.Context, CapturePartsPageRequest) (CapturePartsPage, error)
}

func ValidCapturePartsPage(request CapturePartsPageRequest) bool {
	return validHash(request.EditionID) && validHash(request.Publication) && request.After >= -1 && request.Limit > 0 && request.Limit <= MaxCapturePartsPageRows
}

func ValidCaptureEvidenceCursor(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > MaxCaptureEvidenceCursorBytes || len(value) < 131 {
		return false
	}
	parts := strings.Split(value, ":")
	if len(parts) != 3 || !validHash(parts[0]) || !validHash(parts[1]) {
		return false
	}
	index, err := strconv.ParseInt(parts[2], 10, 64)
	return err == nil && index >= 0 && strconv.FormatInt(index, 10) == parts[2]
}
func ValidCaptureEvidencePage(request CaptureEvidencePageRequest) bool {
	return ValidCaptureEvidenceCursor(request.After) && request.Limit > 0 && request.Limit <= MaxObservationPageRows && request.MaxBytes >= capture.MaxCapturePartTextBytes+128+253 && request.MaxBytes <= MaxObservationPageBytes
}
