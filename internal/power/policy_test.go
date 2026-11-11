package power

import (
	"testing"
	"time"
)

func TestResolveUsesVerifiedSaverStateWithoutTreatingBatteryAsSaver(t *testing.T) {
	tests := []struct {
		name  string
		mode  Mode
		state SystemState
		want  Mode
	}{
		{name: "follow system saver", mode: FollowSystem, state: SystemState{Known: true, SaverEnabled: true}, want: Saver},
		{name: "follow system balanced", mode: FollowSystem, state: SystemState{Known: true, SaverEnabled: false}, want: Balanced},
		{name: "unknown fallback is balanced", mode: FollowSystem, state: SystemState{}, want: Balanced},
		{name: "plugged or battery is not the policy signal", mode: Balanced, state: SystemState{Known: true, SaverEnabled: false}, want: Balanced},
		{name: "explicit saver overrides OS", mode: Saver, state: SystemState{Known: true, SaverEnabled: false}, want: Saver},
		{name: "explicit performance overrides OS", mode: Performance, state: SystemState{Known: true, SaverEnabled: true}, want: Performance},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := Resolve(Config{Mode: test.mode}, test.state)
			if err != nil {
				t.Fatal(err)
			}
			if got.Mode != test.want {
				t.Fatalf("effective mode = %q, want %q", got.Mode, test.want)
			}
		})
	}
}

func TestResolveNeverSuppressesCaptureOrInteractiveWork(t *testing.T) {
	for _, mode := range []Mode{FollowSystem, Balanced, Saver, Performance} {
		effective, err := Resolve(Config{Mode: mode}, SystemState{Known: true, SaverEnabled: true})
		if err != nil {
			t.Fatal(err)
		}
		if !effective.CaptureEnabled || !effective.InteractiveAIEnabled || !effective.RetentionEnabled {
			t.Fatalf("mode %q suppressed required work: %+v", mode, effective)
		}
	}
}

func TestLinuxPowerProfilesUsesDocumentedSystemBusAddress(t *testing.T) {
	if linuxPowerProfilesService != "org.freedesktop.UPower.PowerProfiles" || linuxPowerProfilesPath != "/org/freedesktop/UPower/PowerProfiles" || linuxPowerProfilesInterface != "org.freedesktop.UPower.PowerProfiles" {
		t.Fatalf("Linux Power Profiles address = %q %q %q", linuxPowerProfilesService, linuxPowerProfilesPath, linuxPowerProfilesInterface)
	}
}

func TestBackgroundAIIsLocalBoundedAndModeAware(t *testing.T) {
	tests := []struct {
		name      string
		mode      Mode
		state     SystemState
		provider  Provider
		wantAllow bool
		wantJobs  int
	}{
		{name: "saver blocks optional background", mode: FollowSystem, state: SystemState{Known: true, SaverEnabled: true}, provider: LocalProvider, wantJobs: 0},
		{name: "balanced permits one local job", mode: FollowSystem, state: SystemState{Known: true}, provider: LocalProvider, wantAllow: true, wantJobs: 1},
		{name: "performance remains bounded", mode: Performance, provider: LocalProvider, wantAllow: true, wantJobs: 2},
		{name: "background remote provider is never allowed", mode: Performance, provider: RemoteProvider, wantJobs: 2},
		{name: "unknown power permits balanced work", mode: FollowSystem, provider: LocalProvider, wantAllow: true, wantJobs: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			effective, err := Resolve(Config{Mode: test.mode}, test.state)
			if err != nil {
				t.Fatal(err)
			}
			if effective.MaxBackgroundAIJobs != test.wantJobs {
				t.Fatalf("max background AI jobs = %d, want %d", effective.MaxBackgroundAIJobs, test.wantJobs)
			}
			got := effective.AllowBackgroundAI(test.provider, 100, time.Second)
			if got != test.wantAllow {
				t.Fatalf("AllowBackgroundAI(%q) = %t, want %t", test.provider, got, test.wantAllow)
			}
		})
	}
}

func TestBackgroundAIRejectsOverBudgetWorkAndInvalidConfiguration(t *testing.T) {
	effective, err := Resolve(Config{Mode: Balanced, MaxBackgroundTokens: 512, MaxBackgroundDuration: 3 * time.Second}, SystemState{})
	if err != nil {
		t.Fatal(err)
	}
	if effective.AllowBackgroundAI(LocalProvider, 513, time.Second) || effective.AllowBackgroundAI(LocalProvider, 512, 4*time.Second) {
		t.Fatal("background AI work exceeding its configured budget was allowed")
	}
	for _, config := range []Config{{Mode: "unknown"}, {Mode: Balanced, MaxBackgroundTokens: MaxBackgroundTokens + 1}, {Mode: Balanced, MaxBackgroundDuration: MaxBackgroundDuration + time.Second}} {
		if _, err := Resolve(config, SystemState{}); err == nil {
			t.Fatalf("Resolve(%+v) accepted invalid configuration", config)
		}
	}
}
