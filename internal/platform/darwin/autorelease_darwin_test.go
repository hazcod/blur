//go:build darwin

package darwin

import (
	"runtime"
	"sync"
	"testing"

	"github.com/ebitengine/purego/objc"
)

// Cocoa autorelease pools belong to a native thread, not a goroutine. Yielding
// under concurrent scheduler pressure must not move a pool's body to another
// native thread before its deferred drain.
func TestAutoreleasePoolKeepsNativeThread(t *testing.T) {
	const workers = 16
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for range 8 {
				withAutoreleasePool(func() {
					thread := objc.ID(objc.GetClass("NSThread")).Send(sel("currentThread"))
					if thread == 0 {
						t.Error("Foundation thread unavailable")
						return
					}
					for range 20 {
						runtime.Gosched()
						if current := objc.ID(objc.GetClass("NSThread")).Send(sel("currentThread")); current != thread {
							t.Error("autorelease pool crossed native threads")
							return
						}
					}
				})
			}
		})
	}
	wg.Wait()
}
