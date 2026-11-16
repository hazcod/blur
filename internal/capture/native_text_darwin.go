//go:build darwin

package capture

import (
	"context"
	"math"
	"runtime"
)

// CFRange uses signed native CFIndex values on supported 64-bit macOS targets.
type nativeTextRange struct{ Location, Length int64 }

const nativeCFRangeType = 4
const nativeSInt64Type = 4

// textString borrows a string from an owned native value. Keep that value alive
// until conversion or an immutable copy completes; attributed strings own it.
func (n *nativeAXNode) textString(value uintptr) uintptr {
	if value == 0 {
		return 0
	}
	kind := n.api.getTypeID(value)
	if kind == n.api.stringType() {
		return value
	}
	if n.api.attributedType != nil && n.api.attributedString != nil && kind == n.api.attributedType() {
		text := n.api.attributedString(value)
		if text != 0 && n.api.getTypeID(text) == n.api.stringType() {
			return text
		}
	}
	return 0
}

func (n *nativeAXNode) visibleTextRange(ctx context.Context, total int64, known bool) (nativeTextRange, bool, error) {
	value, status, err := n.copyAttribute(ctx, "AXVisibleCharacterRange")
	if err != nil {
		return nativeTextRange{}, false, err
	}
	if status == axAttributeUnsupported || status == axNoValue {
		return nativeTextRange{}, false, nil
	}
	defer n.api.release(value)
	var r nativeTextRange
	if n.api.getTypeID(value) != n.api.valueType() || n.api.valueKind(value) != nativeCFRangeType || n.api.valueGetRange(value, nativeCFRangeType, &r) == 0 ||
		r.Location < 0 || r.Length < 0 || r.Length > math.MaxInt64-r.Location || known && r.Location+r.Length > total {
		return nativeTextRange{}, false, NativeReadError(ReadStageRange, 0)
	}
	return r, true, ctx.Err()
}

// For long values, preserve the text the person is looking at instead of always
// saving the oldest scrollback prefix. Off-screen omissions remain partial.
func (n *nativeAXNode) copyVisibleText(ctx context.Context, value uintptr, maxBytes int) (string, error) {
	start := int64(0)
	if n.api.stringLen(value) > int64(maxBytes) {
		r, found, err := n.visibleTextRange(ctx, n.api.stringLen(value), true)
		if err != nil {
			return "", err
		}
		if found && r.Length > 0 {
			start = r.Location
			// AX ranges are UTF-16. Move off the middle of a surrogate pair.
			if start > 0 && start < n.api.stringLen(value) {
				unit := n.api.stringCharacter(value, start)
				if unit >= 0xdc00 && unit <= 0xdfff {
					previous := n.api.stringCharacter(value, start-1)
					if previous >= 0xd800 && previous <= 0xdbff {
						start--
					}
				}
			}
		}
	}
	text, _, more, err := copyCFStringRange(ctx, n.api, value, start, maxBytes)
	if err != nil {
		return "", err
	}
	if start != 0 || more {
		return text, errAXValueTruncated
	}
	return text, nil
}

func nativeRangeTextRole(role string) bool {
	switch role {
	case "AXTextArea", "AXTextField", "AXSearchField", "AXComboBox", "AXStaticText", "AXWebArea", "AXHeading", "AXCell", "AXDocument":
		return true
	default:
		return false
	}
}

// Some applications expose text through a parameterized range but no AXValue.
// Request only the bounded current range, never the entire reported document.
func (n *nativeAXNode) rangeText(ctx context.Context, maxBytes int) (string, error) {
	role, err := n.role(ctx)
	if err != nil {
		return "", err
	}
	if !nativeRangeTextRole(role) || maxBytes < 1 || n.api.parameterizedValue == nil {
		return "", nil
	}
	if err := n.verifyTextSensitivity(ctx); err != nil {
		return "", err
	}
	total, known := int64(0), false
	value, status, err := n.copyAttribute(ctx, "AXNumberOfCharacters")
	if err != nil {
		return "", err
	}
	if status == axSuccess {
		valid := n.api.getTypeID(value) == n.api.numberType() && n.api.numberGetInt64(value, nativeSInt64Type, &total) != 0 && total >= 0
		n.api.release(value)
		if !valid {
			return "", NativeReadError(ReadStageRange, 0)
		}
		known = true
	}
	r, visible, err := n.visibleTextRange(ctx, total, known)
	if err != nil {
		return "", err
	}
	if !visible {
		if !known {
			return "", nil
		}
		r = nativeTextRange{Length: total}
	}
	if r.Length == 0 {
		return "", nil
	}
	// One extra UTF-16 unit lets a byte-bounded conversion finish a pair.
	r.Length = min(r.Length, int64(maxBytes)+1)
	parameter := n.api.valueCreate(nativeCFRangeType, &r)
	runtime.KeepAlive(r)
	if parameter == 0 {
		return "", NativeReadError(ReadStageRange, 0)
	}
	defer n.api.release(parameter)
	attribute, storage, err := createAXAttribute(n.api, "AXStringForRange")
	if err != nil {
		return "", err
	}
	defer n.api.release(attribute)
	defer runtime.KeepAlive(storage)
	if err := n.verifyTextSensitivity(ctx); err != nil {
		return "", err
	}
	if err := n.prepare(ctx); err != nil {
		return "", err
	}
	var result uintptr
	status = n.api.parameterizedValue(n.element, attribute, parameter, &result)
	if result != 0 {
		defer n.api.release(result)
	}
	if err := n.verifyTextSensitivity(ctx); err != nil {
		return "", err
	}
	if status == axAttributeUnsupported || status == axParameterizedUnsupported || status == axNoValue {
		return "", nil
	}
	if status != axSuccess || result == 0 {
		return "", NativeReadError(ReadStageRange, status)
	}
	textValue := n.textString(result)
	if textValue == 0 || n.api.stringLen(textValue) > r.Length {
		return "", NativeReadError(ReadStageRange, 0)
	}
	text, more, err := copyCFStringContext(ctx, n.api, textValue, maxBytes)
	if err != nil {
		return "", err
	}
	if more || !known || r.Location != 0 || r.Length < total {
		return text, errAXValueTruncated
	}
	return text, nil
}
