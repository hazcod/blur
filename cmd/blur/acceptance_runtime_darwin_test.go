//go:build darwin && blur_acceptance

package main

import (
	"testing"

	"github.com/hazcod/blur/internal/acceptance"
	"github.com/hazcod/blur/internal/appidentity"
)

func TestAcceptanceDesktopNamespaces(t *testing.T) {
	config, err := acceptance.Load()
	if err != nil {
		t.Fatal(err)
	}
	data, err := desktopDataDirectory()
	if err != nil || data != config.DataDirectory() {
		t.Fatal("desktop data path escaped fixture", data, err)
	}
	previous := persistentLogDirectory
	persistentLogDirectory = t.TempDir()
	t.Cleanup(func() { persistentLogDirectory = previous })
	logs, err := blurLogsDirectory()
	if err != nil || logs != config.LogDirectory() {
		t.Fatal("desktop logs escaped fixture", logs, err)
	}
	secrets, err := desktopSecretStore()
	if err != nil {
		t.Fatal(err)
	}
	identity, ok := secrets.(interface{ ServiceIdentity() string })
	if !ok || identity.ServiceIdentity() != config.SecretService || identity.ServiceIdentity() == "sh.blur.desktop" {
		t.Fatal("desktop secret store escaped fixture namespace")
	}
	if appidentity.BundleID != "com.hazcod.blur.acceptance" || appidentity.DisplayName != "Blur Acceptance" {
		t.Fatal("fixture app identity is not isolated")
	}
}
