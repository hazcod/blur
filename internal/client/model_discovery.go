package client

import (
	"context"
	"strings"
	"time"

	"github.com/golang-gui/goui/theme/modern"
	"github.com/golang-gui/goui/ui"
	"github.com/hazcod/blur/internal/model"
)

type modelDiscoveryState struct {
	catalogs            map[string][]model.InstalledModel
	failed              map[string]bool
	checking, attempted bool
	request             uint64
	endpoint            string
	checkedAt, appleAt  time.Time
	cancel              context.CancelFunc
	refreshScheduled    bool
	editRequest         uint64
}

func (d *desktop) selectDetectedModel(name string, embedding bool) bool {
	endpoint := strings.TrimSpace(d.modelEndpoint)
	if embedding {
		endpoint = strings.TrimSpace(d.memoryEndpoint)
	} else if d.modelProvider != ProviderOllama {
		return false
	}
	for _, item := range d.modelDiscovery.catalogs[endpoint] {
		if item.Name != name || embedding && !item.Embedding || !embedding && !item.Completion {
			continue
		}
		if embedding {
			d.memoryModel = name
		} else {
			d.modelName = name
			d.remoteAskEnabled = false
		}
		return true
	}
	return false
}

// Detection reads status and local manifests, never prompts or credentials.
// Selecting an item only changes the draft; Save applies that choice.
func (d *desktop) refreshDetectedModels() {
	if d == nil || d.app == nil {
		return
	}
	if d.modelProvider == ProviderApple && (d.modelDiscovery.appleAt.IsZero() || time.Since(d.modelDiscovery.appleAt) >= 30*time.Second) {
		d.modelDiscovery.appleAt = time.Now()
		d.refreshAppleAvailability()
	}
	if d.modelProvider != ProviderOllama {
		if d.modelDiscovery.cancel != nil {
			d.modelDiscovery.cancel()
		}
		d.modelDiscovery.request++
		d.modelDiscovery.checking = false
		return
	}
	endpoint := strings.TrimSpace(d.modelEndpoint)
	if endpoint == "" {
		endpoint = "http://127.0.0.1:11434"
	}
	if endpoint == d.modelDiscovery.endpoint && (d.modelDiscovery.checking || !d.modelDiscovery.checkedAt.IsZero() && time.Since(d.modelDiscovery.checkedAt) < 30*time.Second) {
		return
	}
	if d.modelDiscovery.cancel != nil {
		d.modelDiscovery.cancel()
	}
	d.modelDiscovery.request++
	request := d.modelDiscovery.request
	d.modelDiscovery.checking, d.modelDiscovery.attempted = true, true
	d.modelDiscovery.endpoint = endpoint
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	d.modelDiscovery.cancel = cancel
	d.app.RequestUpdate()
	go func() {
		defer cancel()
		items, err := model.DiscoverInstalledModels(ctx, endpoint)
		d.app.Post(func() {
			if request != d.modelDiscovery.request {
				return
			}
			if d.modelDiscovery.catalogs == nil {
				d.modelDiscovery.catalogs = make(map[string][]model.InstalledModel)
			}
			if d.modelDiscovery.failed == nil {
				d.modelDiscovery.failed = make(map[string]bool)
			}
			d.modelDiscovery.catalogs[endpoint], d.modelDiscovery.failed[endpoint] = items, err != nil
			d.modelDiscovery.checking, d.modelDiscovery.checkedAt = false, time.Now()
			d.modelDiscovery.cancel = nil
			d.app.RequestUpdate()
		})
	}()
}

// Refresh status automatically while AI settings are visible. Closing Settings
// stops the timer chain; no capture text or validation prompt is submitted.
func (d *desktop) ensureAIModelStatus() {
	if d == nil || d.app == nil {
		return
	}
	d.refreshDetectedModels()
	if d.modelDiscovery.refreshScheduled || !d.dashboardOpen || d.page != "Settings" || d.settingsGroup != settingsGroupAI {
		return
	}
	d.modelDiscovery.refreshScheduled = true
	d.app.TimeoutFunc(30*time.Second, func() {
		d.modelDiscovery.refreshScheduled = false
		if d.dashboardOpen && d.page == "Settings" && d.settingsGroup == settingsGroupAI {
			d.ensureAIModelStatus()
		}
	})
}

func (d *desktop) queueModelDiscovery() {
	if d == nil || d.app == nil {
		return
	}
	d.modelDiscovery.editRequest++
	request := d.modelDiscovery.editRequest
	if d.modelDiscovery.cancel != nil {
		d.modelDiscovery.cancel()
	}
	d.modelDiscovery.request++
	d.modelDiscovery.checking = false
	d.app.TimeoutFunc(350*time.Millisecond, func() {
		if request == d.modelDiscovery.editRequest {
			d.ensureAIModelStatus()
		}
	})
}

func (d *desktop) detectedModelRows(embedding bool) []ui.View {
	endpoint := strings.TrimSpace(d.modelEndpoint)
	selected := d.modelName
	if embedding {
		endpoint, selected = strings.TrimSpace(d.memoryEndpoint), d.memoryModel
	}
	rows := []ui.View{}
	if d.modelDiscovery.checking {
		rows = append(rows, wrapLabel("Finding installed models…").Style(modern.MutedText))
	}
	if d.modelDiscovery.failed[endpoint] {
		return append(rows, wrapLabel("Ollama is unavailable. Start it; Blur will reconnect automatically.").Style(modern.MutedText))
	}
	count := 0
	for _, item := range d.modelDiscovery.catalogs[endpoint] {
		if embedding && !item.Embedding || !embedding && !item.Completion {
			continue
		}
		name := item.Name
		rows = append(rows, choiceButton(name, selected == name).OnClick(func() {
			if d.selectDetectedModel(name, embedding) {
				d.app.RequestUpdate()
			}
		}))
		count++
	}
	if count == 0 && d.modelDiscovery.attempted && !d.modelDiscovery.checking {
		rows = append(rows, wrapLabel("No compatible installed models detected for this address. Detection never downloads models.").Style(modern.MutedText))
	}
	return rows
}
