package client

func (d *desktop) noteProcessingHint() string {
	if d.client == nil {
		return ""
	}
	status := d.client.NoteProcessingStatus()
	return noteProcessingCopy(string(status.State))
}

func noteProcessingCopy(state string) string {
	switch state {
	case "queued", "processing":
		return "Your note is saved. Preparing suggestions in the background…"
	case "ready":
		return "Note processing finished. Review any suggestions in Today."
	case "unavailable":
		return "Your note is saved. Suggestions could not be prepared; review AI settings and try again."
	default:
		return ""
	}
}
