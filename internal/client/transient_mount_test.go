package client

import (
	"github.com/golang-gui/goui/ui"
	"testing"
)

type transientMountContext struct {
	ui.BuildContext
	state any
	after func()
}

func (c *transientMountContext) State() any           { return c.state }
func (c *transientMountContext) SetState(v any)       { c.state = v }
func (c *transientMountContext) AfterUpdate(f func()) { c.after = f }

func TestTransientPreparesOnceAfterMountBeforeAutomaticShow(t *testing.T) {
	prepared := 0
	v := transientMount(ui.Label("panel"), func() { prepared++ }).(*transientMountView)
	ctx := &transientMountContext{}
	v.Build(ctx)
	if prepared != 0 || ctx.after == nil {
		t.Fatal("activation must wait for native mount")
	}
	ctx.after()
	ctx.after = nil
	v.Build(ctx)
	if prepared != 1 || ctx.after != nil {
		t.Fatal("ordinary updates must not reactivate hidden panels")
	}
}
