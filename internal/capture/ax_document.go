package capture

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"
)

type axDocumentNode interface {
	axTreeNode
	childrenPage(context.Context, int, int, int) ([]axTreeNode, int, error)
	childMatches(context.Context, int, int, uintptr) (bool, error)
	openText(context.Context) (axDocumentText, error)
	sameElement(axTreeNode) bool
}

type axDocumentText interface {
	read(context.Context, int) (string, bool, error)
	release()
}

type axDocumentProgress struct {
	Done, StructureLimited bool
	Advanced               bool
	Bytes, Nodes           int
}

var (
	errAXDocumentRead  = errors.New("capture: document read unavailable")
	errAXDocumentYield = errors.New("capture: document round complete")
)

type axDocumentReadError struct{ cause error }

func (e *axDocumentReadError) Error() string        { return errAXDocumentRead.Error() }
func (e *axDocumentReadError) Unwrap() error        { return e.cause }
func (e *axDocumentReadError) Is(target error) bool { return target == errAXDocumentRead }

const axDocumentChildPage = 32
const axDocumentTextRange = 64 << 10

type axDocumentFrame struct {
	node                              axDocumentNode
	role                              string
	phase                             int // enter, immutable-value open, value ranges, children
	text                              axDocumentText
	first                             bool
	pending                           string
	pendingDone                       bool
	children                          []axTreeNode
	pageIndex, nextChild, total, edge int
}

// Raw values stay confined to the capture job. The caller owns this cursor on
// one goroutine; owner cancellation precedes close, rather than racing it.
type axDocumentCursor struct {
	stack                 []*axDocumentFrame
	maxDepth              int
	guard                 func(context.Context) error
	ignored               func(string) bool
	closed, done, limited bool
}

func newAXDocumentCursor(root axDocumentNode, maxDepth int, guard func(context.Context) error, ignored func(string) bool) (*axDocumentCursor, error) {
	if root == nil || maxDepth < 1 || maxDepth > MaxCaptureDepth || guard == nil || root.identity() == 0 {
		if root != nil {
			root.release()
		}
		return nil, errAXDocumentRead
	}
	return &axDocumentCursor{stack: []*axDocumentFrame{{node: root, total: -1, first: true}}, maxDepth: maxDepth, guard: guard, ignored: ignored}, nil
}

// emit receives the round deadline. It must consume the entire piece on success
// and leave it unconsumed on error, so a round timeout can retry the pending piece.
func (c *axDocumentCursor) read(ctx context.Context, limits Limits, emit func(context.Context, string, bool) error) (axDocumentProgress, error) {
	if ctx == nil {
		if c != nil {
			c.close()
		}
		return axDocumentProgress{}, errAXDocumentRead
	}
	round, cancel := context.WithTimeout(ctx, limits.MaxDuration)
	defer cancel()
	return c.readInRound(ctx, round, limits, emit)
}

func (c *axDocumentCursor) readInRound(ctx, round context.Context, limits Limits, emit func(context.Context, string, bool) error) (p axDocumentProgress, err error) {
	if c == nil {
		return p, errAXDocumentRead
	}
	p.StructureLimited = c.limited
	if ctx == nil || round == nil || !limits.valid() || limits.MaxBytes < utf8.UTFMax || emit == nil {
		c.close()
		return p, errAXDocumentRead
	}
	if err := ctx.Err(); err != nil {
		c.close()
		return p, err
	}
	if c.done {
		p.Done = true
		return p, nil
	}
	if c.closed {
		return p, errAXDocumentRead
	}
	// Structural work that does not enter a new node (empty pages/cycles)
	// also has a per-round ceiling.
	for steps := 0; steps < limits.MaxNodes*8+MaxCaptureDepth*4+16; steps++ {
		if len(c.stack) == 0 {
			c.done = true
			c.close()
			p.Done = true
			break
		}
		f := c.stack[len(c.stack)-1]
		if p.Bytes >= limits.MaxBytes || f.phase == 0 && p.Nodes >= limits.MaxNodes {
			break
		}
		beforePhase, beforeStack, beforeNext, beforePage, beforePending, beforeChildren, beforeLimited := f.phase, len(c.stack), f.nextChild, f.pageIndex, len(f.pending), len(f.children), c.limited
		var n, b int
		n, b, err = c.step(round, ctx, min(c.maxDepth, limits.MaxDepth), limits.MaxBytes-p.Bytes, emit)
		p.Advanced = p.Advanced || n > 0 || b > 0 || beforePhase != f.phase || beforeStack != len(c.stack) || beforeNext != f.nextChild || beforePage != f.pageIndex || beforePending != len(f.pending) || beforeChildren != len(f.children) || beforeLimited != c.limited
		p.Nodes += n
		p.Bytes += b
		if err != nil {
			if ctx.Err() != nil {
				err = ctx.Err()
				c.close()
			} else if errors.Is(err, errAXDocumentYield) || errors.Is(err, context.DeadlineExceeded) && round.Err() != nil {
				err = nil
			} else {
				c.close()
				err = &axDocumentReadError{cause: err}
			}
			break
		}
	}
	p.StructureLimited = c.limited
	return p, err
}

func (c *axDocumentCursor) verifyPath(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.guard(ctx); err != nil {
		return err
	}
	for i := 1; i < len(c.stack); i++ {
		parent, child := c.stack[i-1], c.stack[i]
		role, err := parent.node.role(ctx)
		if err != nil {
			return err
		}
		if role != parent.role {
			return errBrowserPageChanged
		}
		secret, err := parent.node.sensitive(ctx, role)
		if err != nil {
			return err
		}
		if secret {
			return errBrowserPageChanged
		}
		matches, err := parent.node.childMatches(ctx, child.edge, parent.total, child.node.identity())
		if err != nil {
			return err
		}
		if !matches {
			return errBrowserPageChanged
		}
	}
	return ctx.Err()
}

func (c *axDocumentCursor) verifyNode(ctx context.Context, f *axDocumentFrame, value bool) error {
	if err := c.verifyPath(ctx); err != nil {
		return err
	}
	role, err := f.node.role(ctx)
	if err != nil {
		return err
	}
	if role != f.role {
		return errBrowserPageChanged
	}
	secret, err := f.node.sensitive(ctx, role)
	if err != nil {
		return err
	}
	if secret || value && c.ignored != nil && c.ignored(role) {
		return errBrowserPageChanged
	}
	return ctx.Err()
}

func (c *axDocumentCursor) step(round, owner context.Context, depth, remaining int, emit func(context.Context, string, bool) error) (nodes, bytes int, err error) {
	if err := round.Err(); err != nil {
		return 0, 0, err
	}
	f := c.stack[len(c.stack)-1]
	switch f.phase {
	case 0:
		if err := c.verifyPath(round); err != nil {
			return 0, 0, err
		}
		role, err := f.node.role(round)
		if err != nil {
			return 0, 0, err
		}
		if role != "" && !validAXRole(role) {
			return 0, 0, errAXDocumentRead
		}
		secret, err := f.node.sensitive(round, role)
		if err != nil {
			return 0, 0, err
		}
		if secret {
			c.pop()
			return 1, 0, nil
		}
		f.role = role
		f.phase = 1
		if c.ignored != nil && c.ignored(role) {
			f.phase = 3
		}
		return 1, 0, nil
	case 1:
		if err := c.verifyNode(round, f, true); err != nil {
			return 0, 0, err
		}
		if support, ok := f.node.(axValueSupport); ok {
			available, err := support.supportsValue(round, f.role)
			if err != nil {
				return 0, 0, err
			}
			if !available {
				f.phase = 3
				return 0, 0, nil
			}
		}
		text, err := f.node.openText(round)
		if err != nil {
			if text != nil {
				text.release()
			}
			return 0, 0, err
		}
		f.text = text
		if limited, ok := text.(interface{ coverageLimited() bool }); ok && limited.coverageLimited() {
			c.limited = true
		}
		f.phase = 2
		if text == nil {
			f.phase = 3
		}
	case 2:
		if remaining < utf8.UTFMax {
			return 0, 0, errAXDocumentYield
		}
		if f.pending == "" {
			if err := c.verifyNode(round, f, true); err != nil {
				return 0, 0, err
			}
			limit := min(remaining, axDocumentTextRange-1)
			text, done, err := f.text.read(round, limit)
			if limited, ok := f.text.(interface{ coverageLimited() bool }); ok && limited.coverageLimited() {
				c.limited = true
			}
			if err != nil {
				return 0, 0, err
			}
			if len(text) > limit || !utf8.ValidString(text) || strings.ContainsRune(text, 0) || text == "" && !done {
				return 0, 0, errAXDocumentRead
			}
			// The range has advanced. Retain it if post-read validation reaches
			// the round deadline; the next round validates before emitting it.
			f.pending, f.pendingDone = text, done
		}
		if err := c.verifyNode(round, f, true); err != nil {
			return 0, 0, err
		}
		piece := f.pending
		if len(piece) > remaining {
			piece, _ = truncateUTF8(piece, remaining)
		}
		if piece != "" {
			if err := emit(round, piece, f.first); err != nil {
				return 0, 0, err
			}
			if err := owner.Err(); err != nil {
				return 0, 0, err
			}
			f.first = false
			f.pending = f.pending[len(piece):]
			bytes = len(piece)
		}
		if f.pending == "" && f.pendingDone {
			f.text.release()
			f.text = nil
			f.phase = 3
		}
		return 0, bytes, nil
	case 3:
		if f.pageIndex < len(f.children) {
			child := f.children[f.pageIndex]
			f.children[f.pageIndex] = nil
			f.pageIndex++
			edge := f.nextChild
			f.nextChild++
			for _, ancestor := range c.stack {
				if ancestor.node.identity() == child.identity() || ancestor.node.sameElement(child) {
					c.limited = true
					child.release()
					return 0, 0, nil
				}
			}
			node, ok := child.(axDocumentNode)
			if !ok {
				child.release()
				return 0, 0, errAXDocumentRead
			}
			c.stack = append(c.stack, &axDocumentFrame{node: node, total: -1, edge: edge, first: true})
			return 0, 0, nil
		}
		f.children = nil
		f.pageIndex = 0
		if f.total >= 0 && f.nextChild == f.total {
			c.pop()
			return 0, 0, nil
		}
		if err := c.verifyNode(round, f, false); err != nil {
			return 0, 0, err
		}
		limit := axDocumentChildPage
		if len(c.stack) >= depth {
			limit = 1
		}
		children, total, err := f.node.childrenPage(round, f.nextChild, limit, f.total)
		if err != nil {
			releaseAXNodes(children)
			return 0, 0, err
		}
		if total < 0 || f.total >= 0 && f.total != total || f.nextChild > total || len(children) > min(limit, total-f.nextChild) || len(children) == 0 && f.nextChild < total {
			releaseAXNodes(children)
			return 0, 0, errAXDocumentRead
		}
		for _, child := range children {
			if child == nil || child.identity() == 0 {
				releaseAXNodes(children)
				return 0, 0, errAXDocumentRead
			}
			if _, ok := child.(axDocumentNode); !ok {
				releaseAXNodes(children)
				return 0, 0, errAXDocumentRead
			}
		}
		if len(c.stack) >= depth {
			if total > 0 {
				c.limited = true
			}
			releaseAXNodes(children)
			c.pop()
			return 0, 0, nil
		}
		f.total = total
		f.children = children
	default:
		return 0, 0, errAXDocumentRead
	}
	return 0, 0, nil
}

func (c *axDocumentCursor) pop() {
	n := len(c.stack) - 1
	f := c.stack[n]
	if f.text != nil {
		f.text.release()
		f.text = nil
	}
	f.pending = ""
	releaseAXNodes(f.children)
	f.children = nil
	f.node.release()
	c.stack[n] = nil
	c.stack = c.stack[:n]
}

func (c *axDocumentCursor) close() {
	if c == nil || c.closed {
		return
	}
	for len(c.stack) > 0 {
		c.pop()
	}
	c.closed = true
}
