package model

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestApplePrioritySnapshotTracksRealWaitersAndHasNoMutableAlias(t *testing.T) {
	gate := &applePriorityGate{}
	_, release, err := gate.acquire(WithBackgroundInferenceGroup(context.Background(), "notes"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		_, release, err := gate.acquire(WithBackgroundInferenceGroup(ctx, "library"))
		if err == nil {
			release()
		}
		close(done)
	}()
	deadline := time.Now().Add(time.Second)
	for {
		sample := gate.snapshot()
		if sample.BackgroundWaiting == 1 {
			if sample.Active != "background" || sample.Group != "notes" || sample.WaitingGroups["library"] != 1 {
				t.Fatal("bad live scheduler snapshot", sample)
			}
			sample.WaitingGroups["library"] = 999
			if gate.snapshot().WaitingGroups["library"] != 1 {
				t.Fatal("snapshot aliases scheduler map")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("waiter count not visible")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	if gate.snapshot().BackgroundWaiting != 0 {
		t.Fatal("canceled waiter retained in telemetry")
	}
}

func TestApplePrioritySnapshotRetainsRetiringSlotUntilActualRelease(t *testing.T) {
	gate := &applePriorityGate{}
	running, release, err := gate.acquire(WithBackgroundInferenceGroup(context.Background(), "recaps"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		_, release, err := gate.acquire(ctx)
		if err == nil {
			release()
		}
		close(done)
	}()
	select {
	case <-running.Done():
	case <-time.After(time.Second):
		t.Fatal("foreground did not preempt background")
	}
	status := gate.snapshot()
	if !errors.Is(context.Cause(running), ErrBackgroundPreempted) || status.Active != "background" || status.Group != "recaps" || !status.Retiring || status.InteractiveWaiting != 1 {
		t.Fatal("status claimed canceled helper had already retired", status)
	}
	cancel()
	<-done
	status = gate.snapshot()
	if status.InteractiveWaiting != 0 || status.Active != "background" || !status.Retiring {
		t.Fatal("canceled foreground changed actual held background slot", status)
	}
	release()
	if status := gate.snapshot(); status.Active != "idle" || status.Retiring {
		t.Fatal("retired slot remained active", status)
	}
}
