//go:build darwin

package capture

import (
	"context"
	"runtime"
)

const maxAXAttributeNames = 128

// Apple documents this API as the complete supported attribute-name list:
// https://developer.apple.com/documentation/applicationservices/1459475-axuielementcopyattributenames
// Only a valid bounded list without AXValue proves absence. Text/range roles
// keep their existing accessors; unknown/malformed metadata keeps the fallback.
// No source value is requested or converted by this metadata probe.
func (n *nativeAXNode) supportsValue(ctx context.Context, role string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if nativeRangeTextRole(role) || n.api.copyNames == nil {
		return true, nil
	}
	if _, ignored := commonChromeRoles[role]; ignored {
		return true, nil
	}
	if err := n.prepare(ctx); err != nil {
		return false, err
	}
	var names uintptr
	status := n.api.copyNames(n.element, &names)
	if names != 0 {
		defer n.api.release(names)
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if status != axSuccess || names == 0 || n.api.getTypeID(names) != n.api.arrayType() {
		return true, nil
	}
	count := n.api.arrayCount(names)
	if count < 0 || count > maxAXAttributeNames {
		return true, nil
	}
	valueName, storage, err := createAXAttribute(n.api, "AXValue")
	if err != nil {
		return true, nil
	}
	defer n.api.release(valueName)
	defer runtime.KeepAlive(storage)
	for index := int64(0); index < count; index++ {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		name := n.api.arrayValue(names, index)
		if name == 0 || n.api.getTypeID(name) != n.api.stringType() {
			return true, nil
		}
		if n.api.equal(name, valueName) {
			return true, nil
		}
	}
	return false, ctx.Err()
}
