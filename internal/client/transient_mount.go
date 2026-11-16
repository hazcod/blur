package client

import "github.com/golang-gui/goui/ui"

// A new GoUI window is automatically shown after AfterUpdate. Prepare its
// native target here, before that first Show can restore Settings as key.
type transientMountView struct {
	ui.ComposeBase[transientMountView]
	content ui.View
	prepare func()
}

func transientMount(content ui.View, prepare func()) ui.View {
	v := &transientMountView{content: content, prepare: prepare}
	v.Self = v
	return v
}

func (v *transientMountView) Build(ctx ui.BuildContext) ui.View {
	if ctx.State() == nil {
		ctx.SetState(true)
		ctx.AfterUpdate(v.prepare)
	}
	return v.content
}
