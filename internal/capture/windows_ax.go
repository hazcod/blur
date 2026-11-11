package capture

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/hazcod/blur/internal/platform"
	platformwindows "github.com/hazcod/blur/internal/platform/windows"
)

// windowsAXElement keeps native UI Automation calls behind a small, testable
// boundary. Implementations must cap child enumeration and text reads before
// returning to this package.
type windowsAXElement interface {
	identity() uintptr
	processID(context.Context) (uint32, error)
	controlType(context.Context) (int32, error)
	password(context.Context) (bool, error)
	children(context.Context, int) ([]windowsAXElement, bool, error)
	text(context.Context, int) (string, error)
	release()
	closeSession()
}

type windowsVerifiedSource interface {
	platform.CaptureSource
	WithVerifiedTarget(context.Context, string, func(context.Context, platformwindows.FocusedTarget) error) error
}

type windowsAXNode struct {
	element     windowsAXElement
	releaseOnce sync.Once
}

const windowsUIACleanupReserve = 200 * time.Millisecond
const windowsAXChildInitialCapacity = 16

func windowsChildInitialCapacity(max int) int {
	if max < windowsAXChildInitialCapacity {
		return max
	}
	return windowsAXChildInitialCapacity
}

// windowsOperationContext leaves room for one provider transaction to time
// out and for its COM references to be released before the capture deadline.
func windowsOperationContext(ctx context.Context, maxDuration time.Duration) (context.Context, context.CancelFunc) {
	now := time.Now()
	deadline := now.Add(maxDuration - windowsUIACleanupReserve)
	if parentDeadline, ok := ctx.Deadline(); ok {
		parentDeadline = parentDeadline.Add(-windowsUIACleanupReserve)
		if parentDeadline.Before(deadline) {
			deadline = parentDeadline
		}
	}
	return context.WithDeadline(ctx, deadline)
}

// windowsTextUnitLimit bounds native UTF-16 reads. The exact UTF-8 byte limit
// is enforced immediately after conversion by windowsAXNode.value; using the
// full byte budget in UTF-16 units avoids discarding most ASCII text while
// keeping transient provider output bounded to at most three UTF-8 bytes per
// requested code unit.
func windowsTextUnitLimit(maxBytes int) int {
	if maxBytes < 1 {
		return 1
	}
	return maxBytes
}

// windowsUIACall bounds caller wait without starting a second concurrent COM
// operation. Native implementations serialize on one locked worker thread and
// provide discard so late interface results do not leak references.
func windowsUIACall[T any](ctx context.Context, call func() (T, error), discard func(T)) (T, error) {
	value, _, err := windowsUIACallWithAbandon(ctx, call, discard, nil)
	return value, err
}

func newWindowsAXNode(element windowsAXElement) axTreeNode {
	if element == nil {
		return nil
	}
	return &windowsAXNode{element: element}
}

func (n *windowsAXNode) identity() uintptr { return n.element.identity() }

func (n *windowsAXNode) role(ctx context.Context) (string, error) {
	controlType, err := n.element.controlType(ctx)
	if err != nil {
		return "", err
	}
	return windowsControlRole(controlType), nil
}

func (n *windowsAXNode) sensitive(ctx context.Context, role string) (bool, error) {
	if isEditableTextRole(role) {
		return true, nil
	}
	return n.element.password(ctx)
}

func (n *windowsAXNode) children(ctx context.Context, max int) ([]axTreeNode, bool, error) {
	if max < 1 {
		return nil, false, errors.New("capture: invalid Windows child limit")
	}
	children, more, err := n.element.children(ctx, max)
	if err != nil {
		releaseWindowsElements(children)
		return nil, false, err
	}
	if len(children) > max {
		releaseWindowsElements(children)
		return nil, false, errors.New("capture: Windows child limit exceeded")
	}
	nodes := make([]axTreeNode, 0, len(children))
	for _, child := range children {
		if child == nil {
			continue
		}
		nodes = append(nodes, newWindowsAXNode(child))
	}
	return nodes, more, nil
}

func (n *windowsAXNode) value(ctx context.Context, max int) (string, error) {
	if max < 0 {
		return "", errors.New("capture: invalid Windows value limit")
	}
	value, err := n.element.text(ctx, max)
	if err != nil && !errors.Is(err, errAXValueTruncated) {
		return "", err
	}
	bounded, truncated := truncateUTF8(value, max)
	if truncated || errors.Is(err, errAXValueTruncated) {
		return bounded, errAXValueTruncated
	}
	return bounded, nil
}

func (n *windowsAXNode) release() {
	if n == nil {
		return
	}
	n.releaseOnce.Do(n.element.release)
}

func releaseWindowsElements(elements []windowsAXElement) {
	for _, element := range elements {
		if element != nil {
			element.release()
		}
	}
}

func windowsControlRole(controlType int32) string {
	switch controlType {
	case 50000:
		return "AXButton"
	case 50001:
		return "AXCalendar"
	case 50002:
		return "AXCheckBox"
	case 50003:
		return "AXComboBox"
	case 50004:
		return "AXTextField"
	case 50005:
		return "AXLink"
	case 50006:
		return "AXImage"
	case 50007:
		return "AXRow"
	case 50008:
		return "AXList"
	case 50009:
		return "AXMenu"
	case 50010:
		return "AXMenuBar"
	case 50011:
		return "AXMenuItem"
	case 50012:
		return "AXProgressIndicator"
	case 50013:
		return "AXRadioButton"
	case 50014:
		return "AXScrollBar"
	case 50015:
		return "AXSlider"
	case 50016:
		return "AXIncrementor"
	case 50017:
		return "AXStatusBar"
	case 50018:
		return "AXTabGroup"
	case 50019:
		return "AXTab"
	case 50020:
		return "AXStaticText"
	case 50021:
		return "AXToolbar"
	case 50022:
		return "AXTooltip"
	case 50023:
		return "AXOutline"
	case 50024:
		return "AXRow"
	case 50026:
		return "AXGroup"
	case 50028:
		return "AXTable"
	case 50029:
		return "AXRow"
	case 50030:
		return "AXDocument"
	case 50031:
		return "AXButton"
	case 50032:
		return "AXWindow"
	case 50033:
		return "AXGroup"
	case 50034:
		return "AXHeading"
	case 50035:
		return "AXColumn"
	case 50036:
		return "AXTable"
	case 50037:
		return "AXTitleBar"
	case 50038:
		return "AXSeparator"
	case 50039:
		return "AXGroup"
	case 50040:
		return "AXToolbar"
	default:
		return ""
	}
}

// readWindowsAX holds all raw text inside the existing capture package and
// validates the foreground lease before structure access and around every
// deferred value read. A changed target discards the entire frame.
func readWindowsAX(ctx context.Context, source windowsVerifiedSource, probe platform.CaptureProbe, limits Limits, rootFactory func(context.Context, platformwindows.FocusedTarget) (windowsAXElement, error)) (rawFrame, error) {
	if source == nil || ctx == nil || rootFactory == nil || !limits.valid() || probe.TargetID == "" || probe.Private || probe.Secure || probe.Elevated {
		return rawFrame{}, errors.New("capture: Windows accessibility source unavailable")
	}
	operationCtx, cancel := windowsOperationContext(ctx, limits.MaxDuration)
	defer cancel()
	var frame rawFrame
	var captured platformwindows.FocusedTarget
	err := source.WithVerifiedTarget(operationCtx, probe.TargetID, func(ctx context.Context, target platformwindows.FocusedTarget) error {
		if target.Private || target.Secure || target.Elevated || target.Window == 0 || target.ProcessID == 0 || target.AppID == "" || target.AppID != probe.AppID {
			return platform.ErrInvalidLease
		}
		root, err := rootFactory(ctx, target)
		if err != nil {
			if root != nil {
				root.closeSession()
			}
			return err
		}
		if root == nil {
			return errors.New("capture: Windows accessibility root unavailable")
		}
		processID, err := root.processID(ctx)
		if err != nil || processID != target.ProcessID {
			root.release()
			root.closeSession()
			return platform.ErrInvalidLease
		}
		frame, err = collectAXFrame(ctx, newWindowsAXNode(root), limits)
		if err != nil {
			root.closeSession()
			return err
		}
		frameCleanup := frame.cleanup
		var closeOnce sync.Once
		frame.cleanup = func() {
			if frameCleanup != nil {
				frameCleanup()
			}
			closeOnce.Do(root.closeSession)
		}
		captured = target
		return nil
	})
	if err != nil {
		if frame.cleanup != nil {
			frame.cleanup()
		}
		return rawFrame{}, err
	}
	if frame.cleanup == nil || captured.Window == 0 {
		return rawFrame{}, errors.New("capture: Windows accessibility frame unavailable")
	}
	wrapAXValues(frame.roots, func(ctx context.Context, max int, read func(context.Context, int) (string, error)) (string, error) {
		operationCtx, cancel := windowsOperationContext(ctx, limits.MaxDuration)
		defer cancel()
		var value string
		var valueErr error
		err := source.WithVerifiedTarget(operationCtx, probe.TargetID, func(ctx context.Context, current platformwindows.FocusedTarget) error {
			if current != captured || current.Private || current.Secure || current.Elevated {
				return platform.ErrInvalidLease
			}
			value, valueErr = read(ctx, max)
			if errors.Is(valueErr, errAXValueTruncated) {
				return nil
			}
			return valueErr
		})
		if err != nil {
			return "", err
		}
		return value, valueErr
	})
	return frame, nil
}
