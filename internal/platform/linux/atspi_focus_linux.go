//go:build linux

package linux

func (t *focusTracker) current() (focusTarget, bool) {
	if t == nil {
		return focusTarget{}, false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.active, t.active.token != ""
}

func (t *focusTracker) clear() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.active = focusTarget{}
	t.mu.Unlock()
}
