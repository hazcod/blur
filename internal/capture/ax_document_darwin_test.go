//go:build darwin

package capture

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ebitengine/purego"
)

type nativeDocumentOpener interface {
	openText(context.Context) (axDocumentText, error)
}

type nativeDocumentMetadataRef struct {
	*documentTestRef
	native  *nativeAXNode
	ctx     context.Context
	subrole bool
}

func (r *nativeDocumentMetadataRef) role(ctx context.Context) (string, error) {
	r.ctx = ctx
	if !r.subrole {
		return r.native.role(ctx)
	}
	return r.documentTestRef.role(ctx)
}

func (r *nativeDocumentMetadataRef) sensitive(ctx context.Context, role string) (bool, error) {
	r.ctx = ctx
	if r.subrole {
		return r.native.sensitive(ctx, role)
	}
	return r.documentTestRef.sensitive(ctx, role)
}

func TestNativeAXDocumentMetadataDeadlineYieldsWithoutLosingContinuation(t *testing.T) {
	for _, subrole := range []bool{false, true} {
		t.Run(map[bool]string{false: "role", true: "subrole"}[subrole], func(t *testing.T) {
			native, _, _, _, _ := nativeDocumentFixture(t, "AXGroup")
			g, root := documentTestRoot()
			root.text = "retained tail"
			ref := &nativeDocumentMetadataRef{documentTestRef: root.ref(), native: native, subrole: subrole}
			convert := native.api.stringCString
			waited := false
			native.api.stringCString = func(value, buffer uintptr, size int64, encoding uint32) uint8 {
				result := convert(value, buffer, size, encoding)
				if !waited {
					waited = true
					<-ref.ctx.Done()
				}
				return result
			}
			c, err := newAXDocumentCursor(ref, 64, func(context.Context) error { return nil }, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer c.close()
			limits := documentTestLimits(64, 17)
			limits.MaxDuration = 10 * time.Millisecond
			var got strings.Builder
			emit := func(_ context.Context, text string, _ bool) error { got.WriteString(text); return nil }
			p, err := c.read(context.Background(), limits, emit)
			if !waited || err != nil || p.Done || p.Bytes != 0 || g.active != 1 || g.textReads != 0 {
				t.Fatal("native metadata deadline terminated or advanced the cursor", p, err, g)
			}
			p, err = c.read(context.Background(), documentTestLimits(64, 17), emit)
			if err != nil || !p.Done || got.String() != "retained tail" {
				t.Fatal("native metadata deadline lost subsequent content", p, err, got.String())
			}
			assertDocumentReleased(t, g)
		})
	}
}

func nativeDocumentFixture(t *testing.T, text string) (*nativeAXNode, uintptr, *int, *int, *int) {
	t.Helper()
	nativeAX.once.Do(loadNativeAXAPI)
	if nativeAX.err != nil {
		t.Fatal(nativeAX.err)
	}
	base := nativeAX.api
	bytes, pointer := nativeCString(text)
	original := base.stringNew(0, pointer, cfUTF8)
	runtime.KeepAlive(bytes)
	if original == 0 {
		t.Fatal("native string fixture unavailable")
	}
	var mutableCopy func(uintptr, int64, uintptr) uintptr
	purego.RegisterLibFunc(&mutableCopy, base.core, "CFStringCreateMutableCopy")
	mutable := mutableCopy(0, 0, original)
	base.release(original)
	if mutable == 0 {
		t.Fatal("mutable string fixture unavailable")
	}
	t.Cleanup(func() { base.release(mutable) })
	reads, copies, releases := 0, 0, 0
	api := base
	api.setTimeout = func(uintptr, float32) int32 { return axSuccess }
	attributes := map[uintptr]bool{}
	api.copyValue = func(_ uintptr, attribute uintptr, out *uintptr) int32 {
		attributes[attribute] = true
		reads++
		base.retain(mutable)
		*out = mutable
		return axSuccess
	}
	api.stringCopy = func(allocator, value uintptr) uintptr { copies++; return base.stringCopy(allocator, value) }
	api.release = func(value uintptr) {
		if attributes[value] {
			delete(attributes, value)
		} else if value != mutable {
			releases++
		}
		base.release(value)
	}
	return &nativeAXNode{api: api, element: 42}, mutable, &reads, &copies, &releases
}

func nativeOpenDocument(t *testing.T, node *nativeAXNode) axDocumentText {
	t.Helper()
	opener, ok := any(node).(nativeDocumentOpener)
	if !ok {
		t.Fatal("native node has no immutable document value lease")
	}
	lease, err := opener.openText(context.Background())
	if err != nil || lease == nil {
		t.Fatal("native text lease unavailable", err)
	}
	return lease
}

func TestNativeAXDocumentReadsAnImmutableValueAcrossManyRanges(t *testing.T) {
	want := strings.Repeat("A🙂é 日本語\n", 100000)
	node, mutable, reads, copies, releases := nativeDocumentFixture(t, want)
	lease := nativeOpenDocument(t, node)
	defer lease.release()
	var appendText func(uintptr, uintptr, uint32)
	purego.RegisterLibFunc(&appendText, node.api.core, "CFStringAppendCString")
	bytes, pointer := nativeCString("MUTATED ORIGINAL")
	appendText(mutable, pointer, cfUTF8)
	runtime.KeepAlive(bytes)
	var got strings.Builder
	for rounds := 0; ; rounds++ {
		piece, done, err := lease.read(context.Background(), 4096)
		if err != nil || len(piece) > 4096 || !utf8.ValidString(piece) {
			t.Fatal("invalid immutable text continuation", len(piece), done, err)
		}
		got.WriteString(piece)
		if done {
			break
		}
		if rounds > 1000 {
			t.Fatal("native range did not progress")
		}
	}
	if got.String() != want || *reads != 1 || *copies != 1 {
		t.Fatal("snapshot was reread, changed or truncated", got.Len(), len(want), *reads, *copies)
	}
	lease.release()
	lease.release()
	if *releases != 1 {
		t.Fatal("immutable reference was not released exactly once", *releases)
	}
}

func TestNativeAXDocumentCancellationDoesNotAdvanceItsRange(t *testing.T) {
	node, _, _, _, releases := nativeDocumentFixture(t, "A🙂BCD")
	lease := nativeOpenDocument(t, node)
	defer lease.release()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := lease.read(cancelled, 4); !errors.Is(err, context.Canceled) {
		t.Fatal("native read ignored owner cancellation", err)
	}
	piece, done, err := lease.read(context.Background(), 4)
	if err != nil || piece != "A" || done {
		t.Fatal("cancelled range advanced or split a scalar", piece, done, err)
	}
	piece, done, err = lease.read(context.Background(), 4)
	if err != nil || piece != "🙂" || done {
		t.Fatal("UTF-16 continuation split or skipped a surrogate", piece, done, err)
	}
	lease.release()
	if _, _, err := lease.read(context.Background(), 4); err == nil {
		t.Fatal("released native text remained readable")
	}
	if *releases != 1 {
		t.Fatal("cancellation leaked the retained snapshot", *releases)
	}
}

func TestNativeAXDocumentCancellationDuringConversionKeepsItsOffset(t *testing.T) {
	node, _, _, _, _ := nativeDocumentFixture(t, "A🙂BCD")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	original := node.api.stringCharacter
	first := true
	node.api.stringCharacter = func(value uintptr, index int64) uint16 {
		unit := original(value, index)
		if first {
			first = false
			cancel()
		}
		return unit
	}
	lease := nativeOpenDocument(t, node)
	defer lease.release()
	if text, _, err := lease.read(ctx, 4); text != "" || !errors.Is(err, context.Canceled) {
		t.Fatal("interrupted conversion emitted text", text, err)
	}
	if text, done, err := lease.read(context.Background(), 4); err != nil || text != "A" || done {
		t.Fatal("interrupted conversion advanced its saved offset", text, done, err)
	}
}

func TestNativeAXDocumentUsesCFEqualityForAncestorProxies(t *testing.T) {
	calls := 0
	node := &nativeAXNode{element: 42, api: nativeAXAPI{equal: func(a, b uintptr) bool { calls++; return a == 42 && b == 77 }}}
	alias := &nativeAXNode{element: 77}
	if !node.sameElement(alias) || calls != 1 {
		t.Fatal("distinct proxies were compared by address only", calls)
	}
	alias.closed.Store(true)
	if node.sameElement(alias) || calls != 1 {
		t.Fatal("released proxy entered native equality", calls)
	}
}

func TestNativeAXDocumentRejectsFailedAndNonstringAttributesWithoutLeaking(t *testing.T) {
	for _, mode := range []string{"unsupported", "nonstring", "copy failed", "copy cancelled", "closed node"} {
		t.Run(mode, func(t *testing.T) {
			node, _, reads, _, releases := nativeDocumentFixture(t, "ordinary value")
			opener, ok := any(node).(nativeDocumentOpener)
			if !ok {
				t.Fatal("native node has no immutable document value lease")
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			copyValue := node.api.copyValue
			copyString := node.api.stringCopy
			getType := node.api.getTypeID
			switch mode {
			case "unsupported":
				node.api.copyValue = func(uintptr, uintptr, *uintptr) int32 { return axNoValue }
			case "nonstring":
				node.api.copyValue = func(element, attribute uintptr, out *uintptr) int32 {
					status := copyValue(element, attribute, out)
					node.api.getTypeID = func(value uintptr) uintptr {
						if value == *out {
							return 0
						}
						return getType(value)
					}
					return status
				}
			case "copy failed":
				node.api.stringCopy = func(uintptr, uintptr) uintptr { return 0 }
			case "copy cancelled":
				node.api.stringCopy = func(allocator, value uintptr) uintptr {
					frozen := copyString(allocator, value)
					cancel()
					return frozen
				}
			case "closed node":
				node.closed.Store(true)
			}
			lease, err := opener.openText(ctx)
			if lease != nil {
				lease.release()
				t.Fatal("invalid attribute produced a lease")
			}
			if mode == "unsupported" || mode == "nonstring" {
				if err != nil {
					t.Fatal("absent text became an error", err)
				}
			} else if err == nil {
				t.Fatal("failed native snapshot accepted")
			}
			if mode == "closed node" && *reads != 0 {
				t.Fatal("closed node accessed AX")
			}
			if mode == "copy cancelled" && (*releases != 1 || !errors.Is(err, context.Canceled)) {
				t.Fatal("cancelled copy leaked or lost its typed cause", *releases, err)
			}
		})
	}
}
