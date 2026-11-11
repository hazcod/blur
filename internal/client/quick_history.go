package client

import "strings"

const (
	quickHistoryLimit       = 8
	quickHistoryEntryMaxLen = 512
)

// quickEntryHistory is a small process-memory-only convenience buffer. It is
// intentionally bounded and is never persisted or sent to an AI provider.
type quickEntryHistory struct {
	entries []string
	index   int
	draft   string
	active  bool
}

func (h *quickEntryHistory) Record(value string) {
	if h == nil {
		return
	}
	value = strings.TrimSpace(value)
	if value == "" || len(value) > quickHistoryEntryMaxLen {
		return
	}
	if n := len(h.entries); n > 0 && h.entries[n-1] == value {
		h.resetBrowsing()
		return
	}
	h.entries = append(h.entries, value)
	if len(h.entries) > quickHistoryLimit {
		copy(h.entries, h.entries[len(h.entries)-quickHistoryLimit:])
		h.entries = h.entries[:quickHistoryLimit]
	}
	h.resetBrowsing()
}

func (h *quickEntryHistory) Previous(current string) (string, bool) {
	if h == nil || len(h.entries) == 0 {
		return "", false
	}
	if !h.active {
		h.active = true
		h.draft = current
		h.index = len(h.entries)
	}
	if h.index > 0 {
		h.index--
	}
	return h.entries[h.index], true
}

func (h *quickEntryHistory) Next() (string, bool) {
	if h == nil || !h.active {
		return "", false
	}
	if h.index < len(h.entries) {
		h.index++
	}
	if h.index == len(h.entries) {
		value := h.draft
		h.resetBrowsing()
		return value, true
	}
	return h.entries[h.index], true
}

func (h *quickEntryHistory) Complete(prefix string) (string, bool) {
	if h == nil {
		return "", false
	}
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return "", false
	}
	for i := len(h.entries) - 1; i >= 0; i-- {
		entry := h.entries[i]
		if len(entry) > len(prefix) && strings.HasPrefix(strings.ToLower(entry), strings.ToLower(prefix)) {
			return entry, true
		}
	}
	return "", false
}

func (h *quickEntryHistory) CanComplete(prefix string) bool {
	_, ok := h.Complete(prefix)
	return ok
}

func (h *quickEntryHistory) resetBrowsing() {
	h.active = false
	h.draft = ""
	h.index = len(h.entries)
}
