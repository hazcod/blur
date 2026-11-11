//go:build windows

package power

import "testing"

func TestWindowsSaverFlagRejectsUnknownStatus(t *testing.T) {
	for _, test := range []struct {
		flag  byte
		want  SystemState
		valid bool
	}{{0, SystemState{Known: true}, true}, {1, SystemState{Known: true, SaverEnabled: true}, true}, {255, SystemState{}, false}, {2, SystemState{}, false}} {
		got, valid := windowsSaverState(test.flag)
		if got != test.want || valid != test.valid {
			t.Errorf("windowsSaverState(%d) = (%+v, %t), want (%+v, %t)", test.flag, got, valid, test.want, test.valid)
		}
	}
}
