package darwin

import (
	"context"
	"math"
	"testing"
)

func TestUserIdleUsesAggregateInputAgeAndRejectsInvalidSignals(t *testing.T) {
	for _, tc := range []struct {
		seconds       float64
		idle, invalid bool
	}{
		{0, false, false}, {179.9, false, false}, {180, true, false}, {3600, true, false}, {-1, false, true}, {math.NaN(), false, true}, {math.Inf(1), false, true},
	} {
		a := &Adapter{idleSeconds: func() (float64, error) { return tc.seconds, nil }}
		idle, err := a.UserIdle(context.Background())
		if (err != nil) != tc.invalid || idle != tc.idle {
			t.Fatalf("idle age=%f got=%v err=%v", tc.seconds, idle, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a := &Adapter{idleSeconds: func() (float64, error) { t.Fatal("cancelled idle request reached API"); return 0, nil }}
	if _, err := a.UserIdle(ctx); err != context.Canceled {
		t.Fatalf("cancelled idle request: %v", err)
	}
}
