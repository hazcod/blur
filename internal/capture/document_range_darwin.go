//go:build darwin

package capture

import (
	"context"
	"math"
	"runtime"
	"unicode/utf8"
)

// Range-only AX sources cannot provide an immutable whole-value CFString.
// Read their complete reported range in bounded scalar-aligned pieces, checking
// the original character count before/after every piece and sensitivity through
// the same owning cursor's retained path. Unknown/visible-only ranges are
// explicitly partial, never a complete-document receipt.
type nativeAXDocumentRangeText struct {
	node                   *nativeAXNode
	total, offset, end     int64
	known, limited, closed bool
}

func (n *nativeAXNode) documentCharacterCount(ctx context.Context) (int64, bool, error) {
	value, status, err := n.copyAttribute(ctx, "AXNumberOfCharacters")
	if err != nil {
		return 0, false, err
	}
	if status == axAttributeUnsupported || status == axNoValue {
		return 0, false, nil
	}
	defer n.api.release(value)
	var total int64
	if n.api.numberType == nil || n.api.numberGetInt64 == nil || n.api.getTypeID(value) != n.api.numberType() || n.api.numberGetInt64(value, nativeSInt64Type, &total) == 0 || total < 0 {
		return 0, false, NativeReadError(ReadStageRange, 0)
	}
	return total, true, ctx.Err()
}
func (n *nativeAXNode) openDocumentRangeText(ctx context.Context) (axDocumentText, error) {
	role, err := n.role(ctx)
	if err != nil {
		return nil, err
	}
	if !nativeRangeTextRole(role) || n.api.parameterizedValue == nil {
		return nil, nil
	}
	if err := n.verifyTextSensitivity(ctx); err != nil {
		return nil, err
	}
	total, known, err := n.documentCharacterCount(ctx)
	if err != nil {
		return nil, err
	}
	rangeText := &nativeAXDocumentRangeText{node: n, total: total, end: total, known: known, limited: !known}
	if !known {
		visible, found, err := n.visibleTextRange(ctx, 0, false)
		if err != nil {
			return nil, err
		}
		if !found || visible.Length == 0 {
			return nil, nil
		}
		rangeText.offset, rangeText.end = visible.Location, visible.Location+visible.Length
	}
	return rangeText, nil
}
func (t *nativeAXDocumentRangeText) coverageLimited() bool { return t.limited }
func (t *nativeAXDocumentRangeText) release()              { t.closed = true; t.node = nil }
func (t *nativeAXDocumentRangeText) read(ctx context.Context, maxBytes int) (string, bool, error) {
	if t == nil || t.closed || ctx == nil || maxBytes < utf8.UTFMax || maxBytes > axDocumentTextRange {
		return "", false, errAXDocumentRead
	}
	if t.offset == t.end {
		return "", true, nil
	}
	if err := t.node.verifyTextSensitivity(ctx); err != nil {
		return "", false, err
	}
	if t.known {
		total, known, err := t.node.documentCharacterCount(ctx)
		if err != nil {
			return "", false, err
		}
		if !known || total != t.total {
			return "", false, errBrowserPageChanged
		}
	}
	// One lookahead UTF16 unit keeps a surrogate pair available when the output
	// byte budget ends immediately before it. Only consumed units advance offset.
	length := min(t.end-t.offset, int64(maxBytes)+1)
	if length <= 0 || t.offset > math.MaxInt64-length {
		return "", false, errAXDocumentRead
	}
	r := nativeTextRange{Location: t.offset, Length: length}
	api := t.node.api
	parameter := api.valueCreate(nativeCFRangeType, &r)
	runtime.KeepAlive(r)
	if parameter == 0 {
		return "", false, NativeReadError(ReadStageRange, 0)
	}
	defer api.release(parameter)
	attribute, storage, err := createAXAttribute(api, "AXStringForRange")
	if err != nil {
		return "", false, err
	}
	defer api.release(attribute)
	defer runtime.KeepAlive(storage)
	if err := t.node.prepare(ctx); err != nil {
		return "", false, err
	}
	var result uintptr
	status := api.parameterizedValue(t.node.element, attribute, parameter, &result)
	if result != 0 {
		defer api.release(result)
	}
	if err := t.node.verifyTextSensitivity(ctx); err != nil {
		return "", false, err
	}
	if status == axAttributeUnsupported || status == axParameterizedUnsupported || status == axNoValue {
		t.limited = true
		return "", true, nil
	}
	if status != axSuccess || result == 0 {
		return "", false, NativeReadError(ReadStageRange, status)
	}
	value := t.node.textString(result)
	if value == 0 || api.stringLen(value) != length {
		return "", false, NativeReadError(ReadStageRange, 0)
	}
	text, next, _, err := copyCFStringRange(ctx, api, value, 0, maxBytes)
	if err != nil {
		return "", false, err
	}
	if next <= 0 || next > length {
		return "", false, errAXDocumentRead
	}
	if t.known {
		total, known, err := t.node.documentCharacterCount(ctx)
		if err != nil {
			return "", false, err
		}
		if !known || total != t.total {
			return "", false, errBrowserPageChanged
		}
	}
	t.offset += next
	return text, t.offset == t.end, nil
}
