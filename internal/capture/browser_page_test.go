package capture

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/platform"
	"github.com/hazcod/blur/internal/platform/fake"
)

type browserFixtureNode struct {
	*fakeAXNode
	childrenNodes []*browserFixtureNode
	hiddenValue   bool
	domainValue   string
	domainKnown   bool
	afterValue    func()
}

func (n *browserFixtureNode) value(ctx context.Context, max int) (string, error) {
	text, err := n.fakeAXNode.value(ctx, max)
	if n.afterValue != nil {
		n.afterValue()
	}
	return text, err
}

func browserFixture(id uintptr, role string, children ...*browserFixtureNode) *browserFixtureNode {
	return &browserFixtureNode{fakeAXNode: &fakeAXNode{id: id, roleName: role}, childrenNodes: children}
}
func (n *browserFixtureNode) hidden(context.Context) (bool, error) { return n.hiddenValue, nil }
func (n *browserFixtureNode) location(context.Context) (string, bool, error) {
	return n.domainValue, n.domainKnown, nil
}
func (n *browserFixtureNode) childMatches(_ context.Context, index, total int, expected uintptr) (bool, error) {
	return len(n.childrenNodes) == total && index >= 0 && index < total && n.childrenNodes[index].id == expected, nil
}
func (n *browserFixtureNode) children(_ context.Context, max int) ([]axTreeNode, bool, error) {
	n.childReads++
	count := min(max, len(n.childrenNodes))
	children := make([]axTreeNode, count)
	for i := range children {
		children[i] = n.childrenNodes[i]
	}
	return children, count < len(n.childrenNodes), nil
}

func TestBrowserPageSelectionPrunesChromeAndChoosesVisibleOuterPage(t *testing.T) {
	page := browserFixture(4, "AXWebArea", browserFixture(5, "AXWebArea")) // iframe stays inside its outer page
	page.domainValue, page.domainKnown = "docs.example", true
	toolbar := browserFixture(2, "AXToolbar", browserFixture(3, "AXTextField"))
	hidden := browserFixture(6, "AXWebArea")
	hidden.hiddenValue = true
	window := browserFixture(1, "AXWindow", toolbar, page, hidden)
	lease, err := findBrowserPage(context.Background(), window, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if lease.root.identity() != page.id || toolbar.childReads != 0 || page.childReads != 0 || page.valueReads != 0 {
		t.Fatalf("page selection traversed chrome, content or iframe: page=%d", lease.root.identity())
	}
	if err := lease.validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	window.childrenNodes = []*browserFixtureNode{toolbar, browserFixture(7, "AXWebArea"), hidden}
	if err := lease.validate(context.Background()); !errors.Is(err, errBrowserPageChanged) {
		t.Fatalf("changed tab accepted: %v", err)
	}
	lease.release()
	if page.releases != 1 || toolbar.releases != 1 || hidden.releases != 1 || window.releases != 0 {
		t.Fatalf("lease ownership page=%d toolbar=%d hidden=%d window=%d", page.releases, toolbar.releases, hidden.releases, window.releases)
	}
}

func TestBrowserPageDiscoveryAllowsDeepNativeChromeWithoutReadingContent(t *testing.T) {
	for _, depth := range []int{60, 82} {
		page := browserFixture(1000, "AXWebArea")
		branch := page
		for i := 0; i < depth; i++ {
			branch = browserFixture(uintptr(i+2), "AXGroup", branch)
		}
		window := browserFixture(1, "AXWindow", branch)
		lease, err := findBrowserPage(context.Background(), window, DefaultLimits())
		if depth == 60 {
			if err != nil || lease == nil || lease.root.identity() != 1000 {
				t.Fatalf("deep native chrome hid a readable browser page: %v", err)
			}
			lease.release()
		} else if err == nil || lease != nil {
			t.Fatal("metadata traversal lost its independent depth bound")
		}
		if page.valueReads != 0 || page.childReads != 0 {
			t.Fatal("metadata discovery read page content")
		}
	}
}

func TestBrowserPageSelectionRejectsAmbiguousOrMissingContentBeforeValues(t *testing.T) {
	for _, children := range [][]*browserFixtureNode{
		{browserFixture(2, "AXWebArea"), browserFixture(3, "AXWebArea")},
		{browserFixture(2, "AXToolbar", browserFixture(3, "AXStaticText"))},
	} {
		window := browserFixture(1, "AXWindow", children...)
		lease, err := findBrowserPage(context.Background(), window, DefaultLimits())
		if err == nil || lease != nil {
			t.Fatal("missing or ambiguous browser page accepted")
		}
		for _, child := range children {
			if child.valueReads != 0 || child.releases != 1 {
				t.Fatalf("unsafe read/ownership: %+v", child)
			}
		}
	}
}

func TestBrowserPageSelectionUsesActiveWindowHostToResolveMultipleWebAreas(t *testing.T) {
	background := browserFixture(2, "AXWebArea")
	background.domainValue, background.domainKnown = "old.example", true
	active := browserFixture(3, "AXWebArea")
	active.domainValue, active.domainKnown = "current.example", true
	window := browserFixture(1, "AXWindow", background, active)
	window.domainValue, window.domainKnown = "current.example", true

	lease, err := findBrowserPage(context.Background(), window, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer lease.release()
	if got := lease.root.identity(); got != active.identity() {
		t.Fatalf("selected WebArea %d, want active URL match %d", got, active.identity())
	}
	if background.valueReads != 0 || active.valueReads != 0 {
		t.Fatal("page selection read document text")
	}
}

func TestBrowserPageSelectionStillRejectsNonUniqueActiveHostMatch(t *testing.T) {
	first := browserFixture(2, "AXWebArea")
	first.domainValue, first.domainKnown = "current.example", true
	second := browserFixture(3, "AXWebArea")
	second.domainValue, second.domainKnown = "current.example", true
	window := browserFixture(1, "AXWindow", first, second)
	window.domainValue, window.domainKnown = "current.example", true
	if lease, err := findBrowserPage(context.Background(), window, DefaultLimits()); err == nil || lease != nil {
		if lease != nil {
			lease.release()
		}
		t.Fatal("same-host ambiguity accepted")
	}
	if first.valueReads != 0 || second.valueReads != 0 {
		t.Fatal("ambiguous page selection read document text")
	}
}

func TestBrowserPageSelectionBoundsMetadataAndReleasesNodes(t *testing.T) {
	children := make([]*browserFixtureNode, 20)
	for i := range children {
		children[i] = browserFixture(uintptr(i+2), "AXGroup")
	}
	window := browserFixture(1, "AXWindow", children...)
	lease, err := findBrowserPage(context.Background(), window, Limits{MaxBytes: 100, MaxNodes: 5, MaxDepth: 2, MaxDuration: time.Second})
	if err == nil || lease != nil {
		t.Fatal("incomplete browser metadata accepted")
	}
	for _, child := range children[:4] {
		if child.releases != 1 {
			t.Fatal("bounded metadata reference leaked")
		}
	}
}

func TestBrowserPageDomainUsesWebAreaMetadataAndRejectsConflicts(t *testing.T) {
	page := browserFixture(2, "AXWebArea")
	page.domainValue, page.domainKnown = "docs.example", true
	window := browserFixture(1, "AXWindow", page)
	lease, err := findBrowserPage(context.Background(), window, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer lease.release()
	domain, known, err := lease.domain(context.Background())
	if err != nil || !known || domain != "docs.example" {
		t.Fatalf("page domain=%s known=%v err=%v", domain, known, err)
	}
	window.domainValue, window.domainKnown = "excluded.example", true
	if _, _, err := lease.domain(context.Background()); !errors.Is(err, errCaptureDomainChanged) {
		t.Fatalf("conflicting location accepted: %v", err)
	}
}

func browserPagePipeline(window *browserFixtureNode, beforeValues func()) Pipeline {
	return Pipeline{
		Source: &fake.CaptureSource{Report: available(), ProbeResult: platform.CaptureProbe{TargetID: "browser", AppID: "com.apple.Safari"}},
		Limits: DefaultLimits(), Exclusions: Exclusions{Domains: []string{"private.example"}},
		Read: func(ctx context.Context, _ platform.CaptureProbe, limits Limits) (rawFrame, error) {
			lease, err := findBrowserPage(ctx, window, limits)
			if err != nil {
				return rawFrame{}, err
			}
			domain, known, err := lease.domain(ctx)
			if err != nil {
				lease.release()
				return rawFrame{}, err
			}
			frame, err := collectAXFrame(ctx, lease.takeRoot(), limits)
			if err != nil {
				lease.release()
				return rawFrame{}, err
			}
			cleanup := frame.cleanup
			frame.cleanup = func() { cleanup(); lease.release() }
			frame.validate = lease.validate
			frame.domain, frame.domainKnown = domain, known
			frame.validateDomain = func(ctx context.Context, expected string) error {
				current, known, err := lease.domain(ctx)
				if err != nil {
					return err
				}
				if !known || current != expected {
					return errCaptureDomainChanged
				}
				return nil
			}
			if beforeValues != nil {
				beforeValues()
			}
			return frame, nil
		},
	}
}

func TestBrowserCaptureReadsPageWithoutChromeOrDraftsAndReleasesLease(t *testing.T) {
	text := browserFixture(4, "AXStaticText")
	text.text = "Project Northstar launches Thursday."
	draft := browserFixture(5, "AXTextField")
	draft.text = "unsent private draft"
	page := browserFixture(3, "AXWebArea", text, draft)
	page.domainValue, page.domainKnown = "work.example", true
	toolbar := browserFixture(2, "AXToolbar", browserFixture(7, "AXStaticText"))
	window := browserFixture(1, "AXWindow", toolbar, page)
	pipeline := browserPagePipeline(window, nil)
	observation, err := pipeline.Capture(context.Background())
	if err != nil || observation.Text() != text.text || observation.Domain() != "work.example" {
		t.Fatalf("content=%q domain=%q err=%v", observation.Text(), observation.Domain(), err)
	}
	if toolbar.childReads != 0 || toolbar.valueReads != 0 || draft.valueReads != 0 || draft.childReads != 0 || window.childReads != 1 {
		t.Fatal("browser chrome/draft read or repeated whole-window traversal")
	}
	if page.releases != 1 || toolbar.releases != 1 || text.releases != 1 || draft.releases != 1 {
		t.Fatalf("page refs leaked: page=%d toolbar=%d text=%d draft=%d", page.releases, toolbar.releases, text.releases, draft.releases)
	}
}

func TestBrowserCaptureRejectsExcludedHostAndTabSwitchBeforeText(t *testing.T) {
	for _, blockedHost := range []bool{true, false} {
		text := browserFixture(3, "AXStaticText")
		text.text = "protected page text"
		page := browserFixture(2, "AXWebArea", text)
		page.domainValue, page.domainKnown = "work.example", true
		window := browserFixture(1, "AXWindow", page)
		var switchTab func()
		if blockedHost {
			page.domainValue = "private.example"
		} else {
			switchTab = func() { window.childrenNodes = []*browserFixtureNode{browserFixture(4, "AXWebArea")} }
		}
		pipeline := browserPagePipeline(window, switchTab)
		_, err := pipeline.Capture(context.Background())
		var captureErr *CaptureError
		if !errors.As(err, &captureErr) || captureErr.Code != Excluded || text.valueReads != 0 {
			t.Fatalf("excluded/tab race reached text: err=%v reads=%d", err, text.valueReads)
		}
	}
}

func TestBrowserCaptureDiscardsPageWhenTabChangesDuringRead(t *testing.T) {
	text := browserFixture(3, "AXStaticText")
	text.text = "original public page"
	page := browserFixture(2, "AXWebArea", text)
	page.domainValue, page.domainKnown = "work.example", true
	window := browserFixture(1, "AXWindow", page)
	text.afterValue = func() { window.childrenNodes = []*browserFixtureNode{browserFixture(4, "AXWebArea")} }
	pipeline := browserPagePipeline(window, nil)
	observation, err := pipeline.Capture(context.Background())
	if err == nil || observation.ContentID() != "" || text.valueReads != 1 {
		t.Fatalf("stale page published: source=%q err=%v reads=%d", observation.ContentID(), err, text.valueReads)
	}
}
