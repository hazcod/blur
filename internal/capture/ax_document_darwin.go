//go:build darwin

package capture

import (
	"context"
	"sync/atomic"
	"unicode/utf8"
)

var _ axDocumentNode = (*nativeAXNode)(nil)

// AX may supply another retained proxy for the same logical element. Pointer
// identity alone cannot detect an ancestor cycle through such a proxy.
func (n *nativeAXNode) sameElement(other axTreeNode) bool {
	b, ok := other.(*nativeAXNode)
	return ok && n != nil && b != nil && !n.closed.Load() && !b.closed.Load() && n.api.equal != nil && n.api.equal(n.element, b.element)
}

// AX can return a mutable CFString. Freeze it once, then use scalar UTF-16
// ranges without rereading the live value or retaining each emitted Go string.
// Native memory remains proportional to this one AX value; Go range buffers
// are bounded. The owning document cursor revalidates target/path/policy.
func (n *nativeAXNode) openText(ctx context.Context) (axDocumentText, error) {
	if n == nil || ctx == nil || n.closed.Load() || n.element == 0 || n.api.stringCopy == nil {
		return nil, errAXDocumentRead
	}
	value, status, err := n.copyAttribute(ctx, "AXValue")
	if err != nil {
		return nil, err
	}
	if status == axAttributeUnsupported || status == axNoValue {
		return n.openDocumentRangeText(ctx)
	}
	defer n.api.release(value)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	textValue := n.textString(value)
	if textValue == 0 {
		return nil, nil
	}
	if n.api.stringLen(textValue) == 0 {
		return n.openDocumentRangeText(ctx)
	}
	frozen := n.api.stringCopy(0, textValue)
	if frozen == 0 {
		return nil, errAXDocumentRead
	}
	if err := ctx.Err(); err != nil {
		n.api.release(frozen)
		return nil, err
	}
	if n.api.getTypeID(frozen) != n.api.stringType() {
		n.api.release(frozen)
		return nil, errAXDocumentRead
	}
	return &nativeAXDocumentText{api: n.api, value: frozen}, nil
}

type nativeAXDocumentText struct {
	api    nativeAXAPI
	value  uintptr
	offset int64
	closed atomic.Bool
}

func (v *nativeAXDocumentText) read(ctx context.Context, maxBytes int) (string, bool, error) {
	if v == nil || ctx == nil || v.closed.Load() || maxBytes < utf8.UTFMax || maxBytes > axDocumentTextRange {
		return "", false, errAXDocumentRead
	}
	text, next, more, err := copyCFStringRange(ctx, v.api, v.value, v.offset, maxBytes)
	if err != nil {
		return "", false, err
	}
	if next < v.offset || more && next == v.offset {
		return "", false, errAXDocumentRead
	}
	v.offset = next
	return text, !more, nil
}

func (v *nativeAXDocumentText) release() {
	if v == nil || v.closed.Swap(true) {
		return
	}
	v.api.release(v.value)
}
