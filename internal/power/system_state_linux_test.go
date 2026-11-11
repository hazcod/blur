package power

import "testing"

func TestLinuxProfileStateUsesOnlyVerifiedPowerProfiles(t *testing.T) {
	if powerProfilesService != "org.freedesktop.UPower.PowerProfiles" || powerProfilesPath != "/org/freedesktop/UPower/PowerProfiles" || powerProfilesInterface != "org.freedesktop.UPower.PowerProfiles" {
		t.Fatalf("Power Profiles D-Bus address = %q %q %q", powerProfilesService, powerProfilesPath, powerProfilesInterface)
	}
	for _, test := range []struct {
		profile string
		want    SystemState
	}{
		{profile: "power-saver", want: SystemState{Known: true, SaverEnabled: true}},
		{profile: "balanced", want: SystemState{Known: true}},
		{profile: "performance", want: SystemState{Known: true}},
		{profile: "", want: SystemState{}},
		{profile: "unknown", want: SystemState{}},
	} {
		if got := linuxProfileState(test.profile); got != test.want {
			t.Errorf("linuxProfileState(%q) = %+v, want %+v", test.profile, got, test.want)
		}
	}
}
