package capture

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/hazcod/blur/internal/privacy"
)

// These canonical records contain no admission capability. Authentication must
// precede restoration; restored records are deliberately ineligible for writes.
// The original "blur.document.*" hash domains are format identifiers; keep them
// stable so the capture terminology change preserves existing identities.
type captureHeaderRecord struct {
	Version          int    `json:"version"`
	RedactionVersion int    `json:"redaction_version"`
	AppID            string `json:"app_id"`
	Domain           string `json:"domain"`
	SourceKey        string `json:"source_key"`
	FamilyID         string `json:"family_id"`
	CaptureID        string `json:"capture_id"`
	At               int64  `json:"at"`
}
type capturePartPayload struct {
	Index   int64                `json:"index"`
	Offset  int64                `json:"offset"`
	Text    string               `json:"text"`
	Origins []privacy.OriginSpan `json:"origins"`
}
type capturePartRecord struct {
	Version   int                `json:"version"`
	CaptureID string             `json:"capture_id"`
	Digest    string             `json:"digest"`
	Payload   capturePartPayload `json:"payload"`
}
type captureSummaryPayload struct {
	FamilyID         string          `json:"family_id"`
	RedactionVersion int             `json:"redaction_version"`
	Root             string          `json:"root"`
	Parts            int64           `json:"parts"`
	Bytes            int64           `json:"bytes"`
	RawBytes         int64           `json:"raw_bytes"`
	Coverage         CaptureCoverage `json:"coverage"`
	Counts           []privacy.Count `json:"counts"`
}
type captureSummaryRecord struct {
	Version   int                   `json:"version"`
	Header    captureHeaderRecord   `json:"header"`
	ContentID string                `json:"content_id"`
	Payload   captureSummaryPayload `json:"payload"`
}

func captureHash(namespace string, value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	h := sha256.New()
	h.Write([]byte(namespace))
	h.Write([]byte{0})
	h.Write(encoded)
	return hex.EncodeToString(h.Sum(nil))
}
func captureFamilyID(app, domain, key string) string {
	return captureHash("blur.document.family.v1", [3]string{app, domain, key})
}
func capturePartDigest(p CapturePart) string {
	return captureHash("blur.document.part.v1", capturePartPayload{p.index, p.offset, p.text, p.origins})
}
func summaryPayload(s CaptureSummary) captureSummaryPayload {
	return captureSummaryPayload{s.header.familyID, s.header.redactionVersion, s.root, s.parts, s.bytes, s.rawBytes, s.coverage, s.counts}
}
func captureSummaryID(s CaptureSummary) string {
	return captureHash("blur.document.edition.v1", summaryPayload(s))
}
func headerRecord(h CaptureHeader) captureHeaderRecord {
	return captureHeaderRecord{1, h.redactionVersion, h.appID, h.domain, h.sourceKey, h.familyID, h.captureID, h.at.UnixNano()}
}
func headerFromRecord(r captureHeaderRecord) CaptureHeader {
	return CaptureHeader{appID: r.AppID, domain: r.Domain, sourceKey: r.SourceKey, familyID: r.FamilyID, captureID: r.CaptureID, at: time.Unix(0, r.At).UTC(), redactionVersion: r.RedactionVersion}
}
func captureEncode(value any, maxBytes int) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > maxBytes {
		return nil, ErrCaptureInvalid
	}
	return encoded, nil
}
func captureDecode(encoded []byte, maxBytes int, value any) error {
	if len(encoded) == 0 || len(encoded) > maxBytes {
		return ErrCaptureInvalid
	}
	d := json.NewDecoder(bytes.NewReader(encoded))
	d.DisallowUnknownFields()
	if d.Decode(value) != nil {
		return ErrCaptureInvalid
	}
	canonical, err := json.Marshal(value)
	if err != nil || !bytes.Equal(encoded, canonical) {
		return ErrCaptureInvalid
	}
	return nil
}
func MarshalAuthenticatedCaptureHeader(h CaptureHeader) ([]byte, error) {
	if h.Validate() != nil {
		return nil, ErrCaptureInvalid
	}
	return captureEncode(headerRecord(h), MaxAuthenticatedCaptureHeaderBytes)
}
func RestoreAuthenticatedCaptureHeader(encoded []byte) (CaptureHeader, error) {
	var r captureHeaderRecord
	if captureDecode(encoded, MaxAuthenticatedCaptureHeaderBytes, &r) != nil || r.Version != 1 {
		return CaptureHeader{}, ErrCaptureInvalid
	}
	h := headerFromRecord(r)
	if h.Validate() != nil {
		return CaptureHeader{}, ErrCaptureInvalid
	}
	return h, nil
}
func MarshalAuthenticatedCapturePart(p CapturePart) ([]byte, error) {
	if p.Validate() != nil {
		return nil, ErrCaptureInvalid
	}
	return captureEncode(capturePartRecord{1, p.captureID, p.digest, capturePartPayload{p.index, p.offset, p.text, p.origins}}, MaxAuthenticatedCapturePartBytes)
}
func RestoreAuthenticatedCapturePart(encoded []byte) (CapturePart, error) {
	var r capturePartRecord
	if captureDecode(encoded, MaxAuthenticatedCapturePartBytes, &r) != nil || r.Version != 1 {
		return CapturePart{}, ErrCaptureInvalid
	}
	p := CapturePart{captureID: r.CaptureID, digest: r.Digest, text: r.Payload.Text, index: r.Payload.Index, offset: r.Payload.Offset, origins: r.Payload.Origins}
	if p.Validate() != nil {
		return CapturePart{}, ErrCaptureInvalid
	}
	return p, nil
}
func MarshalAuthenticatedCaptureSummary(s CaptureSummary) ([]byte, error) {
	if s.Validate() != nil {
		return nil, ErrCaptureInvalid
	}
	return captureEncode(captureSummaryRecord{1, headerRecord(s.header), s.contentID, summaryPayload(s)}, MaxAuthenticatedCaptureSummaryBytes)
}
func RestoreAuthenticatedCaptureSummary(encoded []byte) (CaptureSummary, error) {
	var r captureSummaryRecord
	if captureDecode(encoded, MaxAuthenticatedCaptureSummaryBytes, &r) != nil || r.Version != 1 || r.Header.Version != 1 || r.Header.FamilyID != r.Payload.FamilyID || r.Header.RedactionVersion != r.Payload.RedactionVersion {
		return CaptureSummary{}, ErrCaptureInvalid
	}
	s := CaptureSummary{header: headerFromRecord(r.Header), contentID: r.ContentID, root: r.Payload.Root, parts: r.Payload.Parts, bytes: r.Payload.Bytes, rawBytes: r.Payload.RawBytes, coverage: r.Payload.Coverage, counts: r.Payload.Counts}
	if s.Validate() != nil {
		return CaptureSummary{}, ErrCaptureInvalid
	}
	return s, nil
}
