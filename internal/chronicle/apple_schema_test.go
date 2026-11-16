package chronicle

import (
	"encoding/json"
	"testing"
)

// Mirror the native bridge's supported constraint vocabulary. Unsupported
// keywords switch the entire request to prompt guidance, so catch them here.
func TestAppleGuidedSchemaUsesOnlyNativeSupportedKeys(t *testing.T) {
	requireAppleNativeSchema(t, []byte(appleSummarySchema))
}

func requireAppleNativeSchema(t *testing.T, raw []byte) {
	t.Helper()
	var node map[string]json.RawMessage
	if json.Unmarshal(raw, &node) != nil {
		t.Fatalf("invalid native schema: %s", raw)
	}
	var kind string
	if json.Unmarshal(node["type"], &kind) != nil {
		t.Fatalf("native schema lacks type: %s", raw)
	}
	allowed := map[string]bool{"type": true, "description": true}
	var keys []string
	switch kind {
	case "object":
		keys = []string{"properties", "required", "additionalProperties"}
	case "array":
		keys = []string{"items", "minItems", "maxItems"}
	case "string":
		keys = []string{"enum"}
	case "boolean", "integer", "number":
	default:
		t.Fatalf("unsupported native schema type %q", kind)
	}
	for _, key := range keys {
		allowed[key] = true
	}
	for key := range node {
		if !allowed[key] {
			t.Fatalf("unsupported native schema keyword %q would disable guidance", key)
		}
	}
	switch kind {
	case "object":
		if string(node["additionalProperties"]) != "false" || node["required"] == nil {
			t.Fatalf("native object is not closed: %s", raw)
		}
		var properties map[string]json.RawMessage
		if json.Unmarshal(node["properties"], &properties) != nil || properties == nil {
			t.Fatalf("native object lacks properties: %s", raw)
		}
		for _, child := range properties {
			requireAppleNativeSchema(t, child)
		}
	case "array":
		requireAppleNativeSchema(t, node["items"])
	}
}
