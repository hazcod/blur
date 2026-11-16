package client

import (
	"encoding/json"

	"github.com/hazcod/blur/internal/capture"
)

// mcpFocusFields is the focused-element breadcrumb exposed on get_snapshots,
// get_working_memory and read_session. It is embedded (and so flattened) into
// each item so this block stays separate from other per-item fields. Values
// are labels only, redacted and bounded at capture time; empty when the
// capture recorded none.
type mcpFocusFields struct {
	FocusedRole string `json:"focused_role,omitempty"`
	FocusedName string `json:"focused_name,omitempty"`
	FocusedPath string `json:"focused_path,omitempty"`
}

func focusFieldsOf(evidence capture.Evidence) mcpFocusFields {
	return mcpFocusFields{FocusedRole: evidence.FocusedRole(), FocusedName: evidence.FocusedName(), FocusedPath: evidence.FocusedPath()}
}

// trimWorkingMemoryFocus drops breadcrumbs, last item first, only when the
// response would otherwise exceed its byte budget. The breadcrumb is context;
// it never displaces content or fails the read.
func trimWorkingMemoryFocus(response *mcpWorkingMemoryResponse) {
	for i := len(response.Items) - 1; i >= 0; i-- {
		encoded, err := json.Marshal(response)
		fits := err == nil && len(encoded) <= maxMCPReadOutputBytes
		clear(encoded)
		if fits {
			return
		}
		response.Items[i].mcpFocusFields = mcpFocusFields{}
	}
}
