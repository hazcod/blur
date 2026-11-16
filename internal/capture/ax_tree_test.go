package capture

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/platform"
	"github.com/hazcod/blur/internal/platform/fake"
)

type fakeAXNode struct {
	id           uintptr
	roleName     string
	text         string
	isSensitive  bool
	childNodes   []*fakeAXNode
	valueReads   int
	childReads   int
	maxRequested int
	releases     int
	roleErr      error
	sensitiveErr error
	childrenErr  error
	onValue      func()
}

func (n *fakeAXNode) identity() uintptr                    { return n.id }
func (n *fakeAXNode) role(context.Context) (string, error) { return n.roleName, n.roleErr }
func (n *fakeAXNode) sensitive(_ context.Context, role string) (bool, error) {
	return n.isSensitive || isEditableTextRole(role), n.sensitiveErr
}
func (n *fakeAXNode) value(_ context.Context, max int) (string, error) {
	n.valueReads++
	if n.onValue != nil {
		n.onValue()
	}
	if len(n.text) > max {
		return n.text[:max], errAXValueTruncated
	}
	return n.text, nil
}

func TestSecureSubroleCheckTargetsTextInputsAndUnknownRoles(t *testing.T) {
	for _, role := range []string{"AXTextField", "AXTextArea", "AXComboBox", "AXSearchField", "AXSecureTextField", "AXUnknown", "AXCustomEditor"} {
		if !needsSecureSubroleCheck(role) {
			t.Errorf("role %q did not receive a secure-subrole check", role)
		}
	}
	for _, role := range []string{"AXButton", "AXStaticText", "AXScrollArea"} {
		if needsSecureSubroleCheck(role) {
			t.Errorf("role %q received an unnecessary secure-subrole check", role)
		}
	}
}

func TestCaptureRejectsAncestorBecomingSecureBeforeOrDuringDescendantRead(t *testing.T) {
	for _, during := range []bool{false, true} {
		t.Run(fmt.Sprintf("during_read_%t", during), func(t *testing.T) {
			parent := &fakeAXNode{id: 2, roleName: "AXGroup"}
			child := &fakeAXNode{id: 3, roleName: "AXStaticText", text: "protected descendant text"}
			parent.childNodes = []*fakeAXNode{child}
			root := &fakeAXNode{id: 1, roleName: "AXWindow", childNodes: []*fakeAXNode{parent}}
			makeSecure := func() { parent.isSensitive = true; parent.roleName = "AXSecureTextArea" }
			if during {
				child.onValue = makeSecure
			}
			p := Pipeline{
				Source: &fake.CaptureSource{Report: available(), ProbeResult: platform.CaptureProbe{TargetID: "t", AppID: "arbitrary.app"}},
				Limits: Limits{MaxBytes: 256, MaxNodes: 8, MaxDepth: 4, MaxDuration: time.Second},
				// Parent values are ignored; ancestor validation must not depend on reading them.
				Profiles: []AppProfile{{AppID: "arbitrary.app", IgnoreRoles: []string{"AXGroup"}}},
				Read: func(ctx context.Context, _ platform.CaptureProbe, limit Limits) (rawFrame, error) {
					frame, err := collectAXFrame(ctx, root, limit)
					if !during {
						makeSecure()
					}
					return frame, err
				},
			}
			got, err := p.Capture(context.Background())
			if err == nil || got.Text() != "" || !during && child.valueReads != 0 {
				t.Fatalf("secure ancestor did not invalidate descendant: text=%q err=%v child_reads=%d", got.Text(), err, child.valueReads)
			}
		})
	}
}
func (n *fakeAXNode) children(_ context.Context, max int) ([]axTreeNode, bool, error) {
	n.childReads++
	if n.childrenErr != nil {
		return nil, false, n.childrenErr
	}
	if max > n.maxRequested {
		n.maxRequested = max
	}
	count := len(n.childNodes)
	more := count > max
	if count > max {
		count = max
	}
	result := make([]axTreeNode, count)
	for i := 0; i < count; i++ {
		result[i] = n.childNodes[i]
	}
	return result, more, nil
}

func TestCaptureKeepsReadableSiblingsWhenOneAXElementIsUnavailable(t *testing.T) {
	for _, failure := range []string{"role", "sensitivity", "children"} {
		t.Run(failure, func(t *testing.T) {
			bad := &fakeAXNode{id: 2, roleName: "AXGroup", text: "unverified private text"}
			switch failure {
			case "role":
				bad.roleErr = errors.New("unavailable")
			case "sensitivity":
				bad.sensitiveErr = errors.New("unavailable")
			case "children":
				bad.childrenErr = errors.New("unavailable")
			}
			good := &fakeAXNode{id: 3, roleName: "AXStaticText", text: "Visible result: Mira owns rollback."}
			root := &fakeAXNode{id: 1, roleName: "AXWindow", childNodes: []*fakeAXNode{bad, good}}
			l := Limits{MaxBytes: 256, MaxNodes: 8, MaxDepth: 4, MaxDuration: time.Second}
			p := Pipeline{Source: &fake.CaptureSource{Report: available(), ProbeResult: platform.CaptureProbe{TargetID: "fixture", AppID: "arbitrary.app"}}, Limits: l,
				Read: func(ctx context.Context, _ platform.CaptureProbe, limit Limits) (rawFrame, error) {
					return collectAXFrame(ctx, root, limit)
				},
			}
			got, err := p.Capture(context.Background())
			if err != nil || got.Text() != "Visible result: Mira owns rollback." || !got.Truncated() || bad.valueReads != 0 || good.valueReads != 1 {
				t.Fatalf("one stale element discarded readable work or exposed unverified text: text=%q partial=%t err=%v", got.Text(), got.Truncated(), err)
			}
		})
	}
}
func (n *fakeAXNode) release() { n.releases++ }

func TestCollectAXFrameBoundsNodesAndDefersValuesUntilPipeline(t *testing.T) {
	root := &fakeAXNode{id: 1, roleName: "AXWindow"}
	first := &fakeAXNode{id: 2, roleName: "AXStaticText", text: "one"}
	second := &fakeAXNode{id: 3, roleName: "AXStaticText", text: "two"}
	extra := &fakeAXNode{id: 4, roleName: "AXStaticText", text: "extra"}
	root.childNodes = []*fakeAXNode{first, second, extra}
	frame, err := collectAXFrame(context.Background(), root, Limits{MaxBytes: 32, MaxNodes: 3, MaxDepth: 3, MaxDuration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if !frame.truncated || root.maxRequested != 3 || first.valueReads+second.valueReads+extra.valueReads != 0 {
		t.Fatalf("frame did not stay bounded/deferred: truncated=%t max=%d reads=%d", frame.truncated, root.maxRequested, first.valueReads+second.valueReads+extra.valueReads)
	}
	if len(frame.roots) != 1 || len(frame.roots[0].children) != 2 {
		t.Fatalf("unexpected bounded tree shape")
	}
	value, err := frame.roots[0].children[1].value(context.Background(), 8)
	if err != nil || value != "two" || second.valueReads != 1 {
		t.Fatalf("deferred value=%q reads=%d err=%v", value, second.valueReads, err)
	}
	frame.cleanup()
	if root.releases != 1 || first.releases != 1 || second.releases != 1 || extra.releases != 1 {
		t.Fatalf("native nodes not released exactly once: %d,%d,%d,%d", root.releases, first.releases, second.releases, extra.releases)
	}
}

func TestCollectAXFrameRechecksGlobalNodeBudgetBetweenBranches(t *testing.T) {
	first := &fakeAXNode{id: 3, roleName: "AXStaticText", text: "first"}
	second := &fakeAXNode{id: 4, roleName: "AXStaticText", text: "second"}
	branch := &fakeAXNode{id: 2, roleName: "AXGroup", childNodes: []*fakeAXNode{first, second}}
	later := &fakeAXNode{id: 5, roleName: "AXStaticText", text: "later"}
	root := &fakeAXNode{id: 1, roleName: "AXWindow", childNodes: []*fakeAXNode{branch, later}}
	limits := Limits{MaxBytes: 128, MaxNodes: 4, MaxDepth: 4, MaxDuration: time.Second}
	frame, err := collectAXFrame(context.Background(), root, limits)
	if err != nil {
		t.Fatal(err)
	}
	defer frame.cleanup()
	if _, err := validateFrame(frame, limits); err != nil {
		t.Fatalf("bounded branched capture was discarded by frame validation: %v", err)
	}
	if !frame.truncated || len(frame.roots[0].children) != 1 || later.childReads != 0 || later.valueReads != 0 {
		t.Fatalf("late sibling was traversed after descendants exhausted the budget: truncated=%t children=%d late=%+v", frame.truncated, len(frame.roots[0].children), later)
	}
	frame.cleanup()
	for _, node := range []*fakeAXNode{root, branch, first, second, later} {
		if node.releases != 1 {
			t.Fatalf("node %d released %d times", node.id, node.releases)
		}
	}
}

func TestCollectAXFrameDepthLimitMarksTruncationAndReleasesCycle(t *testing.T) {
	root := &fakeAXNode{id: 1, roleName: "AXWindow"}
	child := &fakeAXNode{id: 2, roleName: "AXGroup"}
	grandchild := &fakeAXNode{id: 3, roleName: "AXStaticText", text: "loaded"}
	root.childNodes = []*fakeAXNode{child}
	child.childNodes = []*fakeAXNode{grandchild}
	grandchild.childNodes = []*fakeAXNode{root}
	frame, err := collectAXFrame(context.Background(), root, Limits{MaxBytes: 32, MaxNodes: 8, MaxDepth: 2, MaxDuration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if !frame.truncated || len(frame.roots[0].children) != 1 || len(frame.roots[0].children[0].children) != 0 {
		t.Fatalf("depth limit not represented: %+v", frame)
	}
	frame.cleanup()
	if root.releases != 1 || child.releases != 1 || grandchild.releases != 1 {
		t.Fatalf("cycle/depth node release mismatch: %d,%d,%d", root.releases, child.releases, grandchild.releases)
	}
}

func TestCollectAXFramePrunesSensitiveSubtreeBeforeReadingChildren(t *testing.T) {
	secret := &fakeAXNode{id: 3, roleName: "AXStaticText", text: "password"}
	secure := &fakeAXNode{id: 2, roleName: "AXTextField", isSensitive: true, childNodes: []*fakeAXNode{secret}}
	root := &fakeAXNode{id: 1, roleName: "AXWindow", childNodes: []*fakeAXNode{secure}}
	frame, err := collectAXFrame(context.Background(), root, Limits{MaxBytes: 32, MaxNodes: 8, MaxDepth: 4, MaxDuration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if !frame.roots[0].children[0].sensitive || secure.childReads != 0 || secret.childReads != 0 || secret.valueReads != 0 {
		t.Fatalf("secure subtree was traversed: node=%+v childReads=%d secretReads=%d", frame.roots[0].children[0], secure.childReads, secret.valueReads)
	}
	frame.cleanup()
	if root.releases != 1 || secure.releases != 1 {
		t.Fatalf("sensitive native references not released")
	}
}

func TestCapturePrunesEditableDraftSubtreeAndKeepsRenderedHistory(t *testing.T) {
	draftText := &fakeAXNode{id: 3, roleName: "AXStaticText", text: "unsent private draft"}
	draft := &fakeAXNode{id: 2, roleName: "AXTextField", text: "unsent private draft", childNodes: []*fakeAXNode{draftText}}
	history := &fakeAXNode{id: 4, roleName: "AXStaticText", text: "Mina 10:42: launch is Thursday"}
	root := &fakeAXNode{id: 1, roleName: "AXWindow", childNodes: []*fakeAXNode{draft, history}}
	lim := Limits{MaxBytes: 256, MaxNodes: 8, MaxDepth: 4, MaxDuration: time.Second}
	pipeline := Pipeline{
		Source: &fake.CaptureSource{Report: available(), ProbeResult: platform.CaptureProbe{TargetID: "opaque", AppID: "com.tinyspeck.slackmacgap"}},
		Limits: lim,
		Read: func(ctx context.Context, _ platform.CaptureProbe, limits Limits) (rawFrame, error) {
			return collectAXFrame(ctx, root, limits)
		},
	}
	observation, err := pipeline.Capture(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(observation.Text(), "unsent") || !strings.Contains(observation.Text(), "Mina 10:42: launch is Thursday") {
		t.Fatalf("capture text = %q, want rendered history without editable draft", observation.Text())
	}
	if draft.childReads != 0 || draft.valueReads != 0 || draftText.valueReads != 0 {
		t.Fatalf("editable subtree was traversed/read: draft children=%d values=%d descendant values=%d", draft.childReads, draft.valueReads, draftText.valueReads)
	}
}

func TestBoundedATSPIChildCountNeverExceedsRequestedMaximum(t *testing.T) {
	for _, tc := range []struct {
		count int32
		max   int
		want  int
		more  bool
		valid bool
	}{{8, 3, 3, true, true}, {2, 3, 2, false, true}, {0, 3, 0, false, true}, {-1, 3, 0, false, false}, {4, 0, 0, false, false}} {
		got, more, err := boundedATSPIChildCount(tc.count, tc.max)
		if (err == nil) != tc.valid || got != tc.want || more != tc.more {
			t.Errorf("boundedATSPIChildCount(%d,%d) = %d,%t,%v; want %d,%t valid=%t", tc.count, tc.max, got, more, err, tc.want, tc.more, tc.valid)
		}
	}
}

func TestBoundedATSPITextPreservesUTF8AndReportsMoreContent(t *testing.T) {
	got, truncated, err := boundedATSPIText("ab🙂z", 4, false)
	if err != nil || got != "ab" || !truncated {
		t.Fatalf("UTF-8 clipping = %q,%t,%v", got, truncated, err)
	}
	got, truncated, err = boundedATSPIText("ready", 16, true)
	if err != nil || got != "ready" || !truncated {
		t.Fatalf("remote-more clipping = %q,%t,%v", got, truncated, err)
	}
	if _, _, err := boundedATSPIText(string([]byte{0xff}), 8, false); err == nil {
		t.Fatal("invalid UTF-8 accessibility data accepted")
	}
}
