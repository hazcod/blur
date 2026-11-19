package mcpserver

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// focusFields is the focused-element breadcrumb on get_snapshots,
// get_working_memory and read_session items. Labels only; each name and path
// is at most 120 bytes and the role at most 64.
type focusFields struct {
	FocusedRole string `json:"focused_role,omitempty"`
	FocusedName string `json:"focused_name,omitempty"`
	FocusedPath string `json:"focused_path,omitempty"`

	// WindowTitle is the sanitized title of the window the capture came from
	// (search parity: window titles), at most 256 bytes. It rides with the
	// breadcrumb because both are optional per-capture context.
	WindowTitle string `json:"window_title,omitempty"`
}

const (
	maxFocusFieldBytes  = 120
	maxFocusRoleBytes   = 64
	maxWindowTitleBytes = 256
)

func (f focusFields) valid() bool {
	for _, v := range []struct {
		value string
		max   int
	}{{f.FocusedRole, maxFocusRoleBytes}, {f.FocusedName, maxFocusFieldBytes}, {f.FocusedPath, maxFocusFieldBytes}, {f.WindowTitle, maxWindowTitleBytes}} {
		if len(v.value) > v.max || !utf8.ValidString(v.value) || strings.IndexByte(v.value, 0) >= 0 {
			return false
		}
	}
	return true
}

// withFocusProperties adds the three breadcrumb properties to the item objects
// of the output schema's array named arrayKey. Everything else in the schema is
// left untouched, so it composes with other additive item fields declared in
// the schema constants.
func withFocusProperties(schema, arrayKey string) json.RawMessage {
	var root map[string]any
	if json.Unmarshal([]byte(schema), &root) != nil {
		return json.RawMessage(schema)
	}
	properties, _ := root["properties"].(map[string]any)
	array, _ := properties[arrayKey].(map[string]any)
	items, _ := array["items"].(map[string]any)
	itemProperties, _ := items["properties"].(map[string]any)
	if itemProperties == nil {
		return json.RawMessage(schema)
	}
	itemProperties["focused_role"] = map[string]any{"type": "string", "maxLength": maxFocusRoleBytes, "description": "Accessibility role of the control that had keyboard focus."}
	itemProperties["focused_name"] = map[string]any{"type": "string", "maxLength": maxFocusFieldBytes, "description": "Label of the focused control, never its contents."}
	itemProperties["focused_path"] = map[string]any{"type": "string", "maxLength": maxFocusFieldBytes, "description": "Labels of up to three enclosing elements, outermost first, joined by ' > '."}
	itemProperties["window_title"] = map[string]any{"type": "string", "maxLength": maxWindowTitleBytes, "description": "Title of the window the capture came from; empty when none was recorded."}
	encoded, err := json.Marshal(root)
	if err != nil {
		return json.RawMessage(schema)
	}
	return encoded
}
