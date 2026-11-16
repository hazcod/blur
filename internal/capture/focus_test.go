package capture

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/platform"
	"github.com/hazcod/blur/internal/platform/fake"
)

// fakeFocusNode is a fake AX element. It records every attribute requested so
// tests can prove the focused element's value is never read.
type fakeFocusNode struct {
	id       uintptr
	role     string
	subrole  string
	title    string
	desc     string
	parent   *fakeFocusNode
	failRole error
	asked    *[]string
	released *int
}

type fakeFocusHandle struct{ n *fakeFocusNode }

func (h fakeFocusHandle) role(ctx context.Context) (string, error) {
	*h.n.asked = append(*h.n.asked, "AXRole")
	return h.n.role, h.n.failRole
}
func (h fakeFocusHandle) attribute(ctx context.Context, name string, max int) (string, error) {
	*h.n.asked = append(*h.n.asked, name)
	var v string
	switch name {
	case "AXSubrole":
		v = h.n.subrole
	case "AXTitle":
		v = h.n.title
	case "AXDescription":
		v = h.n.desc
	default:
		return "", errors.New("unexpected attribute " + name)
	}
	if len(v) > max {
		return "", errors.New("too long")
	}
	return v, nil
}
func (h fakeFocusHandle) parent(ctx context.Context) (focusNode, error) {
	if h.n.parent == nil {
		return nil, nil
	}
	return fakeFocusHandle{h.n.parent}, nil
}
func (h fakeFocusHandle) identity() uintptr { return h.n.id }
func (h fakeFocusHandle) release()          { *h.n.released++ }

type focusFixture struct {
	asked    []string
	released int
}

func (f *focusFixture) node(id uintptr, role, title string, parent *fakeFocusNode) *fakeFocusNode {
	return &fakeFocusNode{id: id, role: role, title: title, parent: parent, asked: &f.asked, released: &f.released}
}

func TestReadFocusBreadcrumbReturnsRoleNameAndOutermostFirstPath(t *testing.T) {
	var f focusFixture
	window := f.node(1, "AXWindow", "Private Window Title", nil)
	toolbar := f.node(2, "AXToolbar", "Main toolbar", window)
	group := f.node(3, "AXGroup", "", toolbar)
	search := f.node(4, "AXGroup", "Search panel", group)
	field := f.node(5, "AXTextField", "Search", search)
	field.desc = "ignored because the title is set"

	got, ok := readFocusBreadcrumb(context.Background(), fakeFocusHandle{field}, func(n focusNode) bool { return n.identity() == 1 })
	if !ok || got.role != "AXTextField" || got.name != "Search" {
		t.Fatalf("got %+v ok=%v", got, ok)
	}
	if want := []string{"Main toolbar", "Search panel"}; strings.Join(got.ancestors, "|") != strings.Join(want, "|") {
		t.Fatalf("ancestors = %v, want %v (outermost first, window title never read)", got.ancestors, want)
	}
	for _, name := range f.asked {
		if name == "AXValue" || name == "AXSelectedText" {
			t.Fatalf("value attribute read: %v", f.asked)
		}
	}
}

func TestReadFocusBreadcrumbFallsBackToDescriptionAndKeepsOnlyThreeAncestors(t *testing.T) {
	var f focusFixture
	window := f.node(1, "AXWindow", "w", nil)
	a := f.node(2, "AXGroup", "A", window)
	b := f.node(3, "AXGroup", "B", a)
	c := f.node(4, "AXGroup", "C", b)
	d := f.node(5, "AXGroup", "D", c)
	field := f.node(6, "AXButton", "", d)
	field.desc = "Send message"
	got, ok := readFocusBreadcrumb(context.Background(), fakeFocusHandle{field}, func(n focusNode) bool { return n.identity() == 1 })
	if !ok || got.name != "Send message" || strings.Join(got.ancestors, "|") != "B|C|D" {
		t.Fatalf("got %+v ok=%v", got, ok)
	}
}

func TestReadFocusBreadcrumbReadsNothingForSecureFields(t *testing.T) {
	for _, tc := range []struct{ role, subrole string }{
		{"AXSecureTextField", ""},
		{"AXTextField", "AXSecureTextField"},
		{"AXPasswordField", ""},
	} {
		var f focusFixture
		window := f.node(1, "AXWindow", "w", nil)
		field := f.node(2, tc.role, "Password", window)
		field.subrole = tc.subrole
		got, ok := readFocusBreadcrumb(context.Background(), fakeFocusHandle{field}, nil)
		if ok || got.role != "" || got.name != "" || len(got.ancestors) != 0 {
			t.Fatalf("%+v: secure field produced %+v", tc, got)
		}
		for _, name := range f.asked {
			if name == "AXTitle" || name == "AXDescription" || name == "AXValue" {
				t.Fatalf("%+v: secure field attribute read: %v", tc, f.asked)
			}
		}
	}
}

func TestReadFocusBreadcrumbFailsClosed(t *testing.T) {
	var f focusFixture
	window := f.node(1, "AXWindow", "w", nil)
	field := f.node(2, "AXTextField", "Name", window)
	if _, ok := readFocusBreadcrumb(context.Background(), fakeFocusHandle{field}, func(n focusNode) bool { return n.identity() == 9 }); ok {
		t.Fatal("focused element in a different window was accepted")
	}
	orphan := f.node(3, "AXTextField", "Name", nil)
	if _, ok := readFocusBreadcrumb(context.Background(), fakeFocusHandle{orphan}, func(focusNode) bool { return true }); ok {
		t.Fatal("focused element without a window was accepted while a window was required")
	}
	field.failRole = errors.New("AX failure")
	if _, ok := readFocusBreadcrumb(context.Background(), fakeFocusHandle{field}, nil); ok {
		t.Fatal("role error produced a breadcrumb")
	}
	field.failRole = nil
	field.role = "not a role"
	if _, ok := readFocusBreadcrumb(context.Background(), fakeFocusHandle{field}, nil); ok {
		t.Fatal("invalid role produced a breadcrumb")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	field.role = "AXTextField"
	if _, ok := readFocusBreadcrumb(ctx, fakeFocusHandle{field}, nil); ok {
		t.Fatal("cancelled context produced a breadcrumb")
	}
	if _, ok := readFocusBreadcrumb(context.Background(), nil, nil); ok {
		t.Fatal("nil element produced a breadcrumb")
	}
}

func TestReadFocusBreadcrumbReleasesEveryNode(t *testing.T) {
	var f focusFixture
	window := f.node(1, "AXWindow", "w", nil)
	mid := f.node(2, "AXGroup", "Mid", window)
	field := f.node(3, "AXTextField", "Name", mid)
	if _, ok := readFocusBreadcrumb(context.Background(), fakeFocusHandle{field}, nil); !ok {
		t.Fatal("expected breadcrumb")
	}
	if f.released != 3 {
		t.Fatalf("released %d nodes, want 3", f.released)
	}
}

func TestSanitizeFocusBoundsAndRedacts(t *testing.T) {
	raw := rawFocus{
		role:      "AXTextField",
		name:      "api_key=" + strings.Repeat("A1b2C3d4", 8) + " " + strings.Repeat("x", 300),
		ancestors: []string{strings.Repeat("é", 100), "Authorization: Bearer abcdefghijklmnopqrstuvwxyz012345"},
	}
	role, name, path := sanitizeFocus(raw)
	if role != "AXTextField" {
		t.Fatalf("role = %q", role)
	}
	for label, value := range map[string]string{"name": name, "path": path} {
		if len(value) > MaxFocusFieldBytes {
			t.Fatalf("%s is %d bytes", label, len(value))
		}
		if strings.Contains(value, "A1b2C3d4") || strings.Contains(value, "abcdefghijklmnopqrstuvwxyz012345") {
			t.Fatalf("%s kept a secret: %q", label, value)
		}
	}
	if !strings.HasPrefix(path, "é") {
		t.Fatalf("path should keep the outermost name first: %q", path)
	}
	if _, name, path := sanitizeFocus(rawFocus{role: "bad role", name: "a\x00b", ancestors: []string{"\xff"}}); name != "" || path != "" {
		t.Fatalf("invalid UTF-8/NUL kept: %q %q", name, path)
	}
	if role, _, _ := sanitizeFocus(rawFocus{role: "bad role"}); role != "" {
		t.Fatalf("invalid role kept: %q", role)
	}
}

func focusPipeline(text string, focus func(context.Context) (rawFocus, bool)) Pipeline {
	return Pipeline{
		Source: &fake.CaptureSource{Report: available(), ProbeResult: platform.CaptureProbe{TargetID: "t", AppID: "example.app"}},
		Limits: Limits{MaxBytes: 4096, MaxNodes: 3, MaxDepth: 2, MaxDuration: time.Second},
		Read: func(context.Context, platform.CaptureProbe, Limits) (rawFrame, error) {
			return rawFrame{
				roots: []*rawNode{{declaredBytes: len(text), value: func(context.Context, int) (string, error) { return text, nil }}},
				focus: focus,
			}, nil
		},
	}
}

func TestCaptureStoresSanitizedFocusBreadcrumb(t *testing.T) {
	var gotBudget time.Duration
	p := focusPipeline("visible window text", func(ctx context.Context) (rawFocus, bool) {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("focus read has no deadline")
		}
		gotBudget = time.Until(deadline)
		return rawFocus{role: "AXTextField", name: "Subject", ancestors: []string{"Compose", "Header"}}, true
	})
	observation, err := p.Capture(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if observation.FocusedRole() != "AXTextField" || observation.FocusedName() != "Subject" || observation.FocusedPath() != "Compose > Header" {
		t.Fatalf("focus = %q %q %q", observation.FocusedRole(), observation.FocusedName(), observation.FocusedPath())
	}
	if gotBudget <= 0 || gotBudget > FocusReadBudget {
		t.Fatalf("focus budget = %v, want within %v", gotBudget, FocusReadBudget)
	}
	plain := focusPipeline("visible window text", nil)
	without, err := plain.Capture(context.Background())
	if err != nil || without.ContentID() != observation.ContentID() {
		t.Fatalf("breadcrumb must not change the content ID: %v", err)
	}
}

func TestCaptureRedactsSecretLookingFocusName(t *testing.T) {
	p := focusPipeline("visible window text", func(context.Context) (rawFocus, bool) {
		return rawFocus{role: "AXTextField", name: "token: ghp_" + strings.Repeat("a1B2", 10), ancestors: []string{"password: hunter2hunter2"}}, true
	})
	observation, err := p.Capture(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(observation.FocusedName(), "ghp_") || strings.Contains(observation.FocusedPath(), "hunter2") {
		t.Fatalf("secret survived: %q %q", observation.FocusedName(), observation.FocusedPath())
	}
	if err := observation.Validate(); err != nil {
		t.Fatalf("redacted breadcrumb must validate: %v", err)
	}
}

func TestCaptureSurvivesFailedOrSlowFocusRead(t *testing.T) {
	failed := focusPipeline("visible window text", func(context.Context) (rawFocus, bool) { return rawFocus{}, false })
	observation, err := failed.Capture(context.Background())
	if err != nil || observation.FocusedRole() != "" || observation.FocusedName() != "" || observation.FocusedPath() != "" {
		t.Fatalf("failed read: %v %+v", err, observation)
	}
	slow := focusPipeline("visible window text", func(ctx context.Context) (rawFocus, bool) {
		<-ctx.Done()
		return rawFocus{role: "AXTextField", name: "late"}, true
	})
	start := time.Now()
	observation, err = slow.Capture(context.Background())
	if err != nil || observation.FocusedName() != "" || observation.FocusedRole() != "" {
		t.Fatalf("slow read: %v %q", err, observation.FocusedName())
	}
	if elapsed := time.Since(start); elapsed > 600*time.Millisecond {
		t.Fatalf("slow focus read blocked the capture for %v", elapsed)
	}
}

func TestSanitizeObservationHasNoFocusBreadcrumb(t *testing.T) {
	o, err := SanitizeObservation("example.app", "", "text", 64)
	if err != nil || o.FocusedRole() != "" || o.FocusedName() != "" || o.FocusedPath() != "" {
		t.Fatalf("%v %+v", err, o)
	}
}
