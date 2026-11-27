package capture

// AdmitAuthenticatedReplicaPart is the narrow bridge from the authenticated
// replica transport into the private capture staging API. It revalidates all
// source, summary, digest, origin-span, and part-index invariants before marking
// the immutable remote records as admitted for durable staging.
func AdmitAuthenticatedReplicaPart(summary CaptureSummary, part CapturePart) (CaptureHeader, CaptureSummary, CapturePart, error) {
	if summary.Validate() != nil || part.Validate() != nil || part.CaptureID() != summary.Header().CaptureID() || part.Index() >= summary.PartCount() {
		return CaptureHeader{}, CaptureSummary{}, CapturePart{}, ErrCaptureInvalid
	}
	header := summary.header
	header.admission = true
	summary.header = header
	summary.admission = true
	part.admission = true
	if header.ValidateForAdmission() != nil || summary.ValidateForAdmission() != nil || part.ValidateForAdmission() != nil {
		return CaptureHeader{}, CaptureSummary{}, CapturePart{}, ErrCaptureInvalid
	}
	return header, summary, part, nil
}
