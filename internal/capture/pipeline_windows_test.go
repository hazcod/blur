//go:build windows

package capture

import (
	"testing"

	platformwindows "github.com/hazcod/blur/internal/platform/windows"
)

func TestNewWindowsPipelineWiresBoundedUIAutomationReader(t *testing.T) {
	source := platformwindows.New()
	profiles := []AppProfile{{AppID: "editor.exe", IgnoreRoles: []string{"AXToolbar"}}}
	pipeline := NewWindowsPipeline(source, limits(), Exclusions{Apps: []string{"private.exe"}}, profiles)
	if pipeline.Source != source || pipeline.Read == nil || pipeline.Limits != limits() || len(pipeline.Profiles) != 1 {
		t.Fatalf("Windows pipeline not wired: %+v", pipeline)
	}
	if got := pipeline.Profiles[0].IgnoreRoles[0]; got != "AXToolbar" {
		t.Fatalf("Windows app profile not preserved: %q", got)
	}
}
