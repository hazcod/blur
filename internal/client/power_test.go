package client

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/hazcod/blur/internal/power"
)

func TestClientPowerSettingsRequireAnExplicitControllerAndPersist(t *testing.T) {
	client := NewClient(nil)
	if client.CanConfigurePower() {
		t.Fatal("client without a power controller reports settings available")
	}
	if err := client.ConfigurePower(context.Background(), power.Config{Mode: power.Saver}); err != ErrNotConfigured {
		t.Fatalf("ConfigurePower without controller = %v, want ErrNotConfigured", err)
	}
	controller, err := power.NewController(filepath.Join(t.TempDir(), "power.json"))
	if err != nil {
		t.Fatal(err)
	}
	client.WithPower(controller)
	if !client.CanConfigurePower() {
		t.Fatal("client with power controller reports settings unavailable")
	}
	want := power.Config{Mode: power.Saver}
	if err := client.ConfigurePower(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	if got, err := client.PowerConfig(context.Background()); err != nil || got != want {
		t.Fatalf("PowerConfig() = (%+v, %v), want %+v", got, err, want)
	}
}
