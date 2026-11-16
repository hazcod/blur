//go:build darwin && blur_acceptance

package client

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/hazcod/blur/internal/acceptance"
	"github.com/hazcod/blur/internal/localapi"
	"github.com/hazcod/blur/internal/preferences"
)

func TestAcceptanceSocketStaysInsideFixtureRoot(t *testing.T) {
	config, err := acceptance.Load()
	if err != nil {
		t.Fatal("compile test fixture root and nonce with linker flags", err)
	}
	address := MCPSocketAddress(config.DataDirectory())
	if filepath.Dir(address) != config.DataDirectory() || (localapi.EndpointDescriptor{Kind: localapi.EndpointUnix, Address: address}).Validate() != nil {
		t.Fatal("fixture MCP socket escaped its private data directory", address)
	}
	if address := MCPSocketAddress(filepath.Join(t.TempDir(), "unrelated")); address != "" {
		t.Fatal("fixture accepted an unrelated socket data directory", address)
	}
}

func TestAcceptanceStartsWithDashboardOpen(t *testing.T) {
	// Also expose the setup issue when the encrypted store cannot open. The
	// fixture has no global shortcut or menu item to reach a hidden window.
	d := newDesktop(nil, nil, nil)
	if !d.dashboardOpen || !reflect.DeepEqual(quickPanelWindowIDs(d.dashboardOpen, d.quickPanelMounted), []string{"main"}) {
		t.Fatal("acceptance startup has no visible entry point")
	}
}

func TestAcceptanceScopeSurvivesUserPreferenceChanges(t *testing.T) {
	config, err := acceptance.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, disabled := range []bool{false, true} {
		scope := captureExclusionsFromPreferences(preferences.Config{CaptureDefaultDisabled: disabled, IncludedApps: []string{"com.google.chrome"}, IncludedDomains: []string{"personal.example"}})
		if !reflect.DeepEqual(scope.RequiredApps, []string{config.AppID}) || !reflect.DeepEqual(scope.RequiredDomains, []string{config.Domain}) {
			t.Fatal("user settings removed fixture capture guard", scope)
		}
	}
}

func TestAcceptanceBootstrapRejectsProductionSecretStoreBeforeIO(t *testing.T) {
	config, err := acceptance.Load()
	if err != nil {
		t.Fatal(err)
	}
	secrets := &bootstrapSecrets{values: make(map[string][]byte)}
	before, beforeErr := os.Lstat(config.DataDirectory())
	if app, err := openAcceptanceApp(context.Background(), secrets); err == nil || app != nil {
		t.Fatal("unscoped secrets admitted")
	}
	if secrets.puts != 0 || len(secrets.values) != 0 {
		t.Fatal("unscoped secret store was written")
	}
	after, afterErr := os.Lstat(config.DataDirectory())
	if os.IsNotExist(beforeErr) {
		if !os.IsNotExist(afterErr) {
			t.Fatal("rejected bootstrap created fixture data", afterErr)
		}
	} else if beforeErr != nil || afterErr != nil || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("rejected bootstrap modified fixture data", beforeErr, afterErr)
	}
}
