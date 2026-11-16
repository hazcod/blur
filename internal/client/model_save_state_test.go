package client

import "testing"

func TestEnabledModelSaveButtonReflectsSavedDraftAndBusyState(t *testing.T) {
	saved := LocalAISettings{Enabled: true, Provider: ProviderOllama, Endpoint: "http://127.0.0.1:11434", Model: "qwen3:14b"}
	for _, test := range []struct {
		name, label string
		ready       bool
		change      func(*desktop)
	}{
		{name: "saved", label: "Saved", ready: false},
		{name: "whitespace", label: "Saved", change: func(d *desktop) { d.modelName = " qwen3:14b " }},
		{name: "model", label: "Save changes", ready: true, change: func(d *desktop) { d.modelName = "qwen2.5:14b" }},
		{name: "endpoint", label: "Save changes", ready: true, change: func(d *desktop) { d.modelEndpoint = "http://127.0.0.1:11435" }},
		{name: "empty", label: "Save changes", ready: false, change: func(d *desktop) { d.modelName = "" }},
		{name: "repair", label: "Repair settings", ready: true, change: func(d *desktop) { d.aiNeedsRepair = true }},
		{name: "busy", label: "Saving…", ready: false, change: func(d *desktop) { d.loading = true; d.status = "Saving AI settings…" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			d := &desktop{localAIEnabled: true, modelProvider: saved.Provider, modelEndpoint: saved.Endpoint, modelName: saved.Model}
			if test.change != nil {
				test.change(d)
			}
			label, ready := d.localAIModelSaveState(saved)
			if label != test.label || ready != test.ready {
				t.Fatal("save button misrepresents persisted settings", label, ready)
			}
		})
	}
}

func TestEnabledRemoteModelSaveButtonAllowsCredentialAndConsentUpdates(t *testing.T) {
	saved := LocalAISettings{Enabled: true, Provider: ProviderOpenAICompatible, Endpoint: "https://ai.example.test/v1", Model: "fixture"}
	d := &desktop{localAIEnabled: true, modelProvider: saved.Provider, modelEndpoint: saved.Endpoint, modelName: saved.Model}
	if label, ready := d.localAIModelSaveState(saved); label != "Saved" || ready {
		t.Fatal("unchanged provider appears unsaved", label, ready)
	}
	d.modelCredential = "replacement-fixture"
	if label, ready := d.localAIModelSaveState(saved); label != "Save changes" || !ready {
		t.Fatal("credential rotation cannot be saved", label, ready)
	}
	d.modelCredential = ""
	d.remoteAskEnabled = true
	if label, ready := d.localAIModelSaveState(saved); label != "Save changes" || !ready {
		t.Fatal("consent change cannot be saved", label, ready)
	}
}
