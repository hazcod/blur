//go:build darwin

package capture

import (
	"bytes"
	"context"
	"crypto/sha256"
	"hash"
	"math"
	"runtime"
	"unicode/utf8"
)

// Range-only AX sources cannot provide an immutable whole-value CFString.
// Read their complete reported range in bounded scalar-aligned pieces, checking
// the original character count before/after every piece and sensitivity through
// the same owning cursor's retained path. A second bounded pass must reproduce
// the first pass's content digest before the cursor can finish. Character count
// alone cannot detect same-length edits. Unknown/visible-only ranges remain
// explicitly partial, never a complete-document receipt.
type nativeAXDocumentRangeText struct {
	node                      *nativeAXNode
	total, start, offset, end int64
	verifyOffset              int64
	content, verification     hash.Hash
	verified                  bool
	known, limited, closed    bool
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
	rangeText.start, rangeText.verifyOffset = rangeText.offset, rangeText.offset
	rangeText.content, rangeText.verification = sha256.New(), sha256.New()
	return rangeText, nil
}
func (t *nativeAXDocumentRangeText) coverageLimited() bool { return t.limited }
func (t *nativeAXDocumentRangeText) release() {
	t.closed = true
	t.node, t.content, t.verification = nil, nil, nil
}
func (t *nativeAXDocumentRangeText) read(ctx context.Context, maxBytes int) (string, bool, error) {
	if t == nil || t.closed || t.node == nil || ctx == nil || maxBytes < utf8.UTFMax || maxBytes > axDocumentTextRange {
		return "", false, errAXDocumentRead
	}
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	if t.verified || t.start == t.end {
		return "", true, nil
	}
	if t.offset == t.end {
		text, next, supported, err := t.readRange(ctx, t.verifyOffset, maxBytes)
		if err != nil {
			return "", false, err
		}
		if !supported {
			return "", false, errBrowserPageChanged
		}
		_, _ = t.verification.Write([]byte(text))
		t.verifyOffset += next
		if t.verifyOffset == t.end {
			if !bytes.Equal(t.content.Sum(nil), t.verification.Sum(nil)) {
				return "", false, errBrowserPageChanged
			}
			t.verified = true
		}
		return "", false, &axDocumentTextWork{bytes: len(text)}
	}
	text, next, supported, err := t.readRange(ctx, t.offset, maxBytes)
	if err != nil {
		return "", false, err
	}
	if !supported {
		if t.offset != t.start {
			return "", false, errBrowserPageChanged
		}
		t.limited, t.verified, t.offset = true, true, t.end
		return "", true, nil
	}
	_, _ = t.content.Write([]byte(text))
	t.offset += next
	return text, false, nil
}

func (t *nativeAXDocumentRangeText) readRange(ctx context.Context, offset int64, maxBytes int) (string, int64, bool, error) {
	if err := t.node.verifyTextSensitivity(ctx); err != nil {
		return "", 0, false, err
	}
	if t.known {
		total, known, err := t.node.documentCharacterCount(ctx)
		if err != nil {
			return "", 0, false, err
		}
		if !known || total != t.total {
			return "", 0, false, errBrowserPageChanged
		}
	}
	// One lookahead UTF16 unit keeps a surrogate pair available when the output
	// byte budget ends immediately before it. Only consumed units advance offset.
	length := min(t.end-offset, int64(maxBytes)+1)
	if length <= 0 || offset < t.start || offset > math.MaxInt64-length {
		return "", 0, false, errAXDocumentRead
	}
	r := nativeTextRange{Location: offset, Length: length}
	api := t.node.api
	parameter := api.valueCreate(nativeCFRangeType, &r)
	runtime.KeepAlive(r)
	if parameter == 0 {
		return "", 0, false, NativeReadError(ReadStageRange, 0)
	}
	defer api.release(parameter)
	attribute, storage, err := createAXAttribute(api, "AXStringForRange")
	if err != nil {
		return "", 0, false, err
	}
	defer api.release(attribute)
	defer runtime.KeepAlive(storage)
	if err := t.node.prepare(ctx); err != nil {
		return "", 0, false, err
	}
	var result uintptr
	status := api.parameterizedValue(t.node.element, attribute, parameter, &result)
	if result != 0 {
		defer api.release(result)
	}
	if err := t.node.verifyTextSensitivity(ctx); err != nil {
		return "", 0, false, err
	}
	if status == axAttributeUnsupported || status == axParameterizedUnsupported || status == axNoValue {
		return "", 0, false, nil
	}
	if status != axSuccess || result == 0 {
		return "", 0, false, NativeReadError(ReadStageRange, status)
	}
	value := t.node.textString(result)
	if value == 0 || api.stringLen(value) != length {
		return "", 0, false, NativeReadError(ReadStageRange, 0)
	}
	text, next, _, err := copyCFStringRange(ctx, api, value, 0, maxBytes)
	if err != nil {
		return "", 0, false, err
	}
	if next <= 0 || next > length {
		return "", 0, false, errAXDocumentRead
	}
	if t.known {
		total, known, err := t.node.documentCharacterCount(ctx)
		if err != nil {
			return "", 0, false, err
		}
		if !known || total != t.total {
			return "", 0, false, errBrowserPageChanged
		}
	}
	return text, next, true, nil
}
