package capture

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

type documentTestGraph struct {
	active, maximum, texts, textReads, copies, doubleRelease, largestPage int
	maximumTexts                                                          int
	afterRange                                                            func(context.Context)
}
type documentTestNode struct {
	g          *documentTestGraph
	id         uintptr
	sameAs     uintptr
	name, text string
	secret     bool
	children   []*documentTestNode
}
type documentTestRef struct {
	node   *documentTestNode
	closed bool
}

func (n *documentTestNode) ref() *documentTestRef {
	n.g.active++
	n.g.maximum = max(n.g.maximum, n.g.active)
	return &documentTestRef{node: n}
}
func (r *documentTestRef) identity() uintptr { return r.node.id }
func (r *documentTestRef) sameElement(other axTreeNode) bool {
	b, ok := other.(*documentTestRef)
	if !ok {
		return false
	}
	aID, bID := r.node.id, b.node.id
	if r.node.sameAs != 0 {
		aID = r.node.sameAs
	}
	if b.node.sameAs != 0 {
		bID = b.node.sameAs
	}
	return aID == bID
}
func (r *documentTestRef) role(context.Context) (string, error)            { return r.node.name, nil }
func (r *documentTestRef) sensitive(context.Context, string) (bool, error) { return r.node.secret, nil }
func (r *documentTestRef) value(context.Context, int) (string, error) {
	return "", errors.New("obsolete prefix reader used")
}
func (r *documentTestRef) release() {
	if r.closed {
		r.node.g.doubleRelease++
		return
	}
	r.closed = true
	r.node.g.active--
}
func (r *documentTestRef) children(ctx context.Context, limit int) ([]axTreeNode, bool, error) {
	n, total, err := r.childrenPage(ctx, 0, limit, -1)
	return n, len(n) < total, err
}
func (r *documentTestRef) childrenPage(ctx context.Context, start, limit, expected int) ([]axTreeNode, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	total := len(r.node.children)
	if expected >= 0 && expected != total || start > total {
		return nil, 0, errBrowserPageChanged
	}
	r.node.g.largestPage = max(r.node.g.largestPage, limit)
	var out []axTreeNode
	for _, child := range r.node.children[start:min(total, start+limit)] {
		out = append(out, child.ref())
	}
	return out, total, nil
}
func (r *documentTestRef) childMatches(_ context.Context, index, total int, id uintptr) (bool, error) {
	return len(r.node.children) == total && index >= 0 && index < total && r.node.children[index].id == id, nil
}
func (r *documentTestRef) openText(context.Context) (axDocumentText, error) {
	if r.node.text == "" {
		return nil, nil
	}
	r.node.g.texts++
	r.node.g.maximumTexts = max(r.node.g.maximumTexts, r.node.g.texts)
	r.node.g.copies++
	return &documentTestText{g: r.node.g, text: strings.Clone(r.node.text)}, nil
}

type documentTestText struct {
	g      *documentTestGraph
	text   string
	offset int
	closed bool
}

func (v *documentTestText) read(ctx context.Context, limit int) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	v.g.textReads++
	end := min(len(v.text), v.offset+limit)
	for end < len(v.text) && !utf8.RuneStart(v.text[end]) {
		end--
	}
	out := v.text[v.offset:end]
	v.offset = end
	if v.g.afterRange != nil {
		v.g.afterRange(ctx)
	}
	return out, end == len(v.text), nil
}
func (v *documentTestText) release() {
	if v.closed {
		v.g.doubleRelease++
		return
	}
	v.closed = true
	v.g.texts--
}

func documentTestLimits(bytes, nodes int) Limits {
	return Limits{MaxBytes: bytes, MaxNodes: nodes, MaxDepth: 64, MaxDuration: time.Second}
}
func documentTestRoot() (*documentTestGraph, *documentTestNode) {
	g := &documentTestGraph{}
	return g, &documentTestNode{g: g, id: 1, name: "AXGroup"}
}
func assertDocumentReleased(t *testing.T, g *documentTestGraph) {
	t.Helper()
	if g.active != 0 || g.texts != 0 || g.doubleRelease != 0 {
		t.Fatalf("native ownership leaked or released twice: %+v", g)
	}
}

func TestAXDocumentContinuesLargeValuesAndPagedSiblingsInOrder(t *testing.T) {
	g, root := documentTestRoot()
	for i := 0; i < 130; i++ {
		root.children = append(root.children, &documentTestNode{g: g, id: uintptr(i + 2), name: "AXStaticText", text: fmt.Sprintf("section-%03d", i)})
	}
	root.children[0].text = strings.Repeat("α🙂 ordinary document text ", 50000)
	c, err := newAXDocumentCursor(root.ref(), 64, func(context.Context) error { return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	var got strings.Builder
	var rounds int
	for {
		p, err := c.read(context.Background(), documentTestLimits(64<<10, 17), func(_ context.Context, s string, first bool) error {
			if first && got.Len() > 0 {
				got.WriteByte('\n')
			}
			got.WriteString(s)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		rounds++
		if p.Bytes > 64<<10 || p.Nodes > 17 {
			t.Fatal("round exceeded work budget", p)
		}
		if p.StructureLimited {
			t.Fatal("a round budget became a total document ceiling")
		}
		if p.Done {
			break
		}
		if rounds > 100 {
			t.Fatal("continuation did not progress")
		}
	}
	var want []string
	for _, n := range root.children {
		want = append(want, n.text)
	}
	if got.String() != strings.Join(want, "\n") {
		t.Fatal("document content/order changed across ranges or child pages")
	}
	if rounds < 20 || g.largestPage > 32 || g.maximum > 2113 || g.maximumTexts > 1 {
		t.Fatalf("reader did not use bounded continuation: rounds=%d graph=%+v", rounds, g)
	}
	assertDocumentReleased(t, g)
}

func TestAXDocumentRecognizesAncestorProxiesWithDifferentHandles(t *testing.T) {
	g, root := documentTestRoot()
	alias := &documentTestNode{g: g, id: 99, sameAs: 1, name: "AXStaticText", text: "must not read an ancestor alias"}
	root.children = []*documentTestNode{alias}
	c, err := newAXDocumentCursor(root.ref(), 64, func(context.Context) error { return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	p, err := c.read(context.Background(), documentTestLimits(64, 17), func(context.Context, string, bool) error {
		t.Fatal("read a logically identical ancestor through another proxy")
		return nil
	})
	if err != nil || !p.Done || !p.StructureLimited || p.Nodes != 1 || g.textReads != 0 {
		t.Fatal("ancestor identity used pointer identity only", p, err, g)
	}
	assertDocumentReleased(t, g)
}

func TestAXDocumentBoundsReferencesAtDepthAndContinuesBeyondOldNodeCeiling(t *testing.T) {
	for _, deep := range []bool{false, true} {
		t.Run(fmt.Sprint(deep), func(t *testing.T) {
			g, root := documentTestRoot()
			parent := root
			var id uintptr = 2
			if deep {
				for depth := 1; depth < 64; depth++ {
					for i := 0; i < 32; i++ {
						parent.children = append(parent.children, &documentTestNode{g: g, id: id, name: "AXGroup"})
						id++
					}
					parent = parent.children[0]
				}
			} else {
				for i := 0; i < MaxCaptureNodes+100; i++ {
					root.children = append(root.children, &documentTestNode{g: g, id: id, name: "AXGroup"})
					id++
				}
			}
			c, err := newAXDocumentCursor(root.ref(), 64, func(context.Context) error { return nil }, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer c.close()
			total := 0
			for round := 0; round < 1000; round++ {
				p, err := c.read(context.Background(), documentTestLimits(64, 17), func(context.Context, string, bool) error { return nil })
				if err != nil {
					t.Fatal(err)
				}
				total += p.Nodes
				if p.StructureLimited {
					t.Fatal("a large valid tree was reported as omitted")
				}
				if p.Done {
					break
				}
				if round == 999 {
					t.Fatal("large structure did not progress")
				}
			}
			if total != int(id-1) || g.maximum > 2113 || g.largestPage > 32 {
				t.Fatal("invalid global ceiling or active reference budget", total, id, g)
			}
			assertDocumentReleased(t, g)
		})
	}
}

func TestAXDocumentChecksChangedPolicyAndPathBeforeAnotherRange(t *testing.T) {
	for _, change := range []string{"guard", "sensitive", "ancestor", "replacement", "count", "consumer"} {
		t.Run(change, func(t *testing.T) {
			g, root := documentTestRoot()
			child := &documentTestNode{g: g, id: 2, name: "AXStaticText", text: "abcdefghijklmno"}
			root.children = []*documentTestNode{child}
			changed := false
			c, err := newAXDocumentCursor(root.ref(), 64, func(context.Context) error {
				if changed && change == "guard" {
					return errors.New("private diagnostic text")
				}
				return nil
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer c.close()
			var emitted int
			_, err = c.read(context.Background(), documentTestLimits(4, 17), func(_ context.Context, s string, _ bool) error {
				emitted += len(s)
				changed = true
				switch change {
				case "sensitive":
					child.secret = true
				case "ancestor":
					root.secret = true
				case "replacement":
					root.children[0] = &documentTestNode{g: g, id: 3, name: "AXStaticText"}
				case "count":
					root.children = append(root.children, &documentTestNode{g: g, id: 3, name: "AXStaticText"})
				case "consumer":
					return errors.New("private diagnostic text")
				}
				return nil
			})
			if change != "consumer" && err == nil {
				_, err = c.read(context.Background(), documentTestLimits(4, 17), func(context.Context, string, bool) error {
					t.Fatal("read/emitted another raw range after policy changed")
					return nil
				})
			}
			if err == nil || strings.Contains(err.Error(), "private diagnostic") {
				t.Fatal("lost a safe terminal failure", err)
			}
			if g.textReads != 1 || emitted != 4 {
				t.Fatal("text read passed the changed boundary", g.textReads, emitted)
			}
			assertDocumentReleased(t, g)
		})
	}
}

func TestAXDocumentRetainsRangeWhenRoundDeadlineExpiresBeforeEmission(t *testing.T) {
	g, root := documentTestRoot()
	root.text = "abcdefgh🙂ijk"
	first := true
	g.afterRange = func(ctx context.Context) {
		if first {
			first = false
			<-ctx.Done()
		}
	}
	c, err := newAXDocumentCursor(root.ref(), 64, func(context.Context) error { return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	limits := documentTestLimits(8, 17)
	limits.MaxDuration = time.Millisecond
	p, err := c.read(context.Background(), limits, func(context.Context, string, bool) error {
		t.Fatal("emitted a range without post-read validation")
		return nil
	})
	if err != nil || p.Done || p.Bytes != 0 || g.textReads != 1 || g.texts != 1 {
		t.Fatal("round timeout lost its resumable text lease", p, err, g)
	}
	var got strings.Builder
	var firstFlags int
	for i := 0; i < 20; i++ {
		p, err = c.read(context.Background(), documentTestLimits(4, 17), func(_ context.Context, s string, first bool) error {
			if first {
				firstFlags++
			}
			got.WriteString(s)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if p.Done {
			break
		}
		if i == 19 {
			t.Fatal("small-budget continuation did not finish")
		}
	}
	if got.String() != root.text || firstFlags != 1 {
		t.Fatal("deadline/smaller budget skipped, repeated or separated text ranges", got.String(), firstFlags)
	}
	assertDocumentReleased(t, g)
}

func TestAXDocumentRoundDeadlineBoundsConsumerAndRetainsUnacceptedRange(t *testing.T) {
	g, root := documentTestRoot()
	root.text = "abcdefghi"
	c, err := newAXDocumentCursor(root.ref(), 64, func(context.Context) error { return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	limits := documentTestLimits(4, 17)
	limits.MaxDuration = time.Millisecond
	p, err := c.read(context.Background(), limits, func(ctx context.Context, _ string, _ bool) error {
		if _, ok := ctx.Deadline(); !ok {
			return errors.New("consumer has no round deadline")
		}
		<-ctx.Done()
		return ctx.Err() // No input consumed on failure.
	})
	if err != nil || p.Done || p.Bytes != 0 || g.textReads != 1 || g.texts != 1 {
		t.Fatal("consumer deadline discarded accepted state or closed its resumable cursor", p, err, g)
	}
	var got strings.Builder
	for round := 0; round < 10; round++ {
		p, err = c.read(context.Background(), documentTestLimits(4, 17), func(_ context.Context, text string, _ bool) error { got.WriteString(text); return nil })
		if err != nil {
			t.Fatal(err)
		}
		if p.Done {
			break
		}
	}
	if !p.Done || got.String() != root.text {
		t.Fatal("consumer retry skipped or repeated input", p, got.String())
	}
	assertDocumentReleased(t, g)
}

func TestAXDocumentSkipsSensitiveSubtreesAndIgnoredValues(t *testing.T) {
	g, root := documentTestRoot()
	secret := &documentTestNode{g: g, id: 2, name: "AXGroup", secret: true, text: "secret", children: []*documentTestNode{{g: g, id: 3, name: "AXStaticText", text: "secret descendant"}}}
	ignored := &documentTestNode{g: g, id: 4, name: "AXTextArea", text: "ignored input", children: []*documentTestNode{{g: g, id: 5, name: "AXStaticText", text: "readable descendant"}}}
	root.children = []*documentTestNode{secret, ignored}
	c, err := newAXDocumentCursor(root.ref(), 64, func(context.Context) error { return nil }, func(role string) bool { return role == "AXTextArea" })
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	var got strings.Builder
	p, err := c.read(context.Background(), documentTestLimits(64<<10, 100), func(_ context.Context, s string, _ bool) error { got.WriteString(s); return nil })
	if err != nil || !p.Done || got.String() != "readable descendant" {
		t.Fatal("changed existing sensitive/ignored role policy", p, err, got.String())
	}
	if g.copies != 1 {
		t.Fatal("sensitive/ignored value was opened", g.copies)
	}
	assertDocumentReleased(t, g)
}

func TestAXDocumentCancellationAndInvalidBudgetsReleaseOwnership(t *testing.T) {
	for _, mode := range []string{"cancelled", "nil context", "small bytes", "invalid depth", "manual close"} {
		t.Run(mode, func(t *testing.T) {
			g, root := documentTestRoot()
			depth := 64
			if mode == "invalid depth" {
				depth = 65
			}
			c, err := newAXDocumentCursor(root.ref(), depth, func(context.Context) error { return nil }, nil)
			if mode == "invalid depth" {
				if err == nil {
					t.Fatal("invalid lifetime depth accepted")
				}
				assertDocumentReleased(t, g)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer c.close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			limits := documentTestLimits(64, 10)
			switch mode {
			case "cancelled":
				cancel()
			case "nil context":
				ctx = nil
			case "small bytes":
				limits.MaxBytes = 3
			case "manual close":
				c.close()
			}
			_, err = c.read(ctx, limits, func(context.Context, string, bool) error { t.Fatal("invalid read emitted raw text"); return nil })
			if err == nil {
				t.Fatal("invalid/closed cursor read succeeded")
			}
			assertDocumentReleased(t, g)
		})
	}
}

func TestAXDocumentDepthAndAncestorCyclesAreExplicitlyPartial(t *testing.T) {
	g, root := documentTestRoot()
	child := &documentTestNode{g: g, id: 2, name: "AXStaticText", text: "retained"}
	root.children = []*documentTestNode{child}
	child.children = []*documentTestNode{root}
	c, err := newAXDocumentCursor(root.ref(), 64, func(context.Context) error { return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err := c.read(context.Background(), documentTestLimits(64, 17), func(context.Context, string, bool) error { return nil })
	if err != nil || !p.Done || !p.StructureLimited {
		t.Fatal("ancestor cycle was unbounded or hidden", p, err)
	}
	c.close()
	assertDocumentReleased(t, g)
	g, root = documentTestRoot()
	root.children = []*documentTestNode{{g: g, id: 2, name: "AXStaticText", text: "omitted"}}
	c, err = newAXDocumentCursor(root.ref(), 1, func(context.Context) error { return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err = c.read(context.Background(), documentTestLimits(64, 17), func(context.Context, string, bool) error { t.Fatal("depth-limited child was read"); return nil })
	if err != nil || !p.Done || !p.StructureLimited {
		t.Fatal("depth coverage was not explicit", p, err)
	}
	c.close()
	assertDocumentReleased(t, g)
}
