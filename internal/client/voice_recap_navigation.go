package client

// New input takes precedence over an unfinished recap. Keep any valid partial
// recap and suspended editor, while fencing queued receipts from the old work.
func (d *desktop) cancelVoiceRecapForNavigation() {
	u := &d.memoryUI.recap
	if !u.loading {
		return
	}
	if u.cancel != nil {
		u.cancel()
		u.cancel = nil
	}
	u.request++
	u.loading, u.complete, u.partial = false, false, true
	u.status = "Recap stopped. You can generate it again."
}
