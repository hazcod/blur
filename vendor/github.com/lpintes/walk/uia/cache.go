// Copyright 2026 The Walk Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build windows

package uia

import (
	"image"

	"github.com/lpintes/walk/internal/win"
)

// CacheRequest says which properties FindFirstBuildCache,
// FindAllBuildCache and BuildUpdatedCache read at once, in one call to
// the program that owns the elements, and for which elements. The Cached
// methods of the returned elements then read the cached values without
// another call. That is much faster than the getters without the Cached
// prefix when a client reads several properties of many elements, for
// example a whole list or the tree of a window, and especially for the
// elements of another process.
//
// A CacheRequest is a plain Go value, like a Condition: it can be reused
// and needs no release.
type CacheRequest struct {
	// Properties are the properties to cache.
	Properties []PropertyID

	// Scope selects the elements whose properties are cached, relative
	// to each element the request is used for. The zero value means
	// ScopeElement. With ScopeChildren, ScopeDescendants or ScopeSubtree
	// the elements below are cached too, and CachedChildren and
	// CachedParent move through them.
	Scope Scope

	// Filter selects the elements cached below the element. A nil Filter
	// means the elements of the control view, as ControlView.
	Filter Condition
}

// create returns the UI Automation cache request. It runs on the thread
// of the Automation; the caller releases the result.
func (c *CacheRequest) create(uia *win.IUIAutomation) (*win.IUIAutomationCacheRequest, error) {
	var r *win.IUIAutomationCacheRequest
	if hr := uia.CreateCacheRequest(&r); win.FAILED(hr) {
		return nil, hresultError("CreateCacheRequest", hr)
	}
	ok := false
	defer func() {
		if !ok {
			r.Release()
		}
	}()
	for _, id := range c.Properties {
		if hr := r.AddProperty(int32(id)); win.FAILED(hr) {
			return nil, hresultError("CacheRequest AddProperty", hr)
		}
	}
	scope := c.Scope
	if scope == 0 {
		scope = ScopeElement
	}
	if hr := r.Put_TreeScope(int32(scope)); win.FAILED(hr) {
		return nil, hresultError("CacheRequest TreeScope", hr)
	}
	if c.Filter != nil {
		filter, err := c.Filter.create(uia)
		if err != nil {
			return nil, err
		}
		hr := r.Put_TreeFilter(filter)
		filter.Release()
		if win.FAILED(hr) {
			return nil, hresultError("CacheRequest TreeFilter", hr)
		}
	}
	ok = true
	return r, nil
}

// withCache creates cond, unless it is nil, and cache and runs f with them
// and the COM object of e on the thread of the Automation.
func (e *Element) withCache(cond Condition, cache *CacheRequest, f func(p *win.IUIAutomationElement, c *win.IUIAutomationCondition, r *win.IUIAutomationCacheRequest) error) error {
	return e.call(func(p *win.IUIAutomationElement) error {
		var c *win.IUIAutomationCondition
		if cond != nil {
			var err error
			if c, err = createCondition(e.a.uia, cond); err != nil {
				return err
			}
			defer c.Release()
		}
		r, err := cache.create(e.a.uia)
		if err != nil {
			return err
		}
		defer r.Release()
		return f(p, c, r)
	})
}

// FindFirstBuildCache is FindFirst that also caches what cache asks for in
// the element it returns. It returns nil and no error if no element
// matches cond, and also if the element found does not match the Filter
// of cache while the Scope of cache includes ScopeElement.
func (e *Element) FindFirstBuildCache(scope Scope, cond Condition, cache CacheRequest) (*Element, error) {
	var found *Element
	if cond == nil {
		cond = True()
	}
	err := e.withCache(cond, &cache, func(p *win.IUIAutomationElement, c *win.IUIAutomationCondition, r *win.IUIAutomationCacheRequest) error {
		var f *win.IUIAutomationElement
		if hr := p.FindFirstBuildCache(int32(scope), c, r, &f); win.FAILED(hr) {
			return hresultError("FindFirstBuildCache", hr)
		}
		found = e.a.newElement(f)
		return nil
	})
	return found, err
}

// FindAllBuildCache is FindAll that also caches what cache asks for in
// the elements it returns.
func (e *Element) FindAllBuildCache(scope Scope, cond Condition, cache CacheRequest) ([]*Element, error) {
	var found []*Element
	if cond == nil {
		cond = True()
	}
	err := e.withCache(cond, &cache, func(p *win.IUIAutomationElement, c *win.IUIAutomationCondition, r *win.IUIAutomationCacheRequest) error {
		var array *win.IUIAutomationElementArray
		if hr := p.FindAllBuildCache(int32(scope), c, r, &array); win.FAILED(hr) {
			return hresultError("FindAllBuildCache", hr)
		}
		var err error
		found, err = e.a.elements("FindAllBuildCache", array)
		return err
	})
	return found, err
}

// BuildUpdatedCache returns a new Element for the same user interface
// object as e, with what cache asks for cached. The cache of e itself does
// not change. Use it to cache the properties of an element got without a
// cache, such as Root or ElementFromHandle, or to read them again.
//
// BuildUpdatedCache can return nil and no error: UI Automation returns no
// element when e does not match the Filter of cache and the Scope includes
// ScopeElement. Use a Filter that also matches e, or a Scope without
// ScopeElement, such as ScopeChildren alone, to get the children of e
// whatever e is, and check the result for nil.
func (e *Element) BuildUpdatedCache(cache CacheRequest) (*Element, error) {
	var updated *Element
	err := e.withCache(nil, &cache, func(p *win.IUIAutomationElement, _ *win.IUIAutomationCondition, r *win.IUIAutomationCacheRequest) error {
		var u *win.IUIAutomationElement
		if hr := p.BuildUpdatedCache(r, &u); win.FAILED(hr) {
			return hresultError("BuildUpdatedCache", hr)
		}
		updated = e.a.newElement(u)
		return nil
	})
	return updated, err
}

// The Cached methods return the value of a property from the cache of the
// element, filled in when the element was returned by FindFirstBuildCache,
// FindAllBuildCache, BuildUpdatedCache or CachedChildren. They fail if the
// cache request did not include the property.

// CachedName is Name from the cache.
func (e *Element) CachedName() (string, error) {
	return e.stringProperty("CachedName", (*win.IUIAutomationElement).Get_CachedName)
}

// CachedControlType is ControlType from the cache.
func (e *Element) CachedControlType() (ControlType, error) {
	n, err := e.intProperty("CachedControlType", (*win.IUIAutomationElement).Get_CachedControlType)
	return ControlType(n), err
}

// CachedAutomationID is AutomationID from the cache.
func (e *Element) CachedAutomationID() (string, error) {
	return e.stringProperty("CachedAutomationId", (*win.IUIAutomationElement).Get_CachedAutomationId)
}

// CachedClassName is ClassName from the cache.
func (e *Element) CachedClassName() (string, error) {
	return e.stringProperty("CachedClassName", (*win.IUIAutomationElement).Get_CachedClassName)
}

// CachedIsEnabled is IsEnabled from the cache.
func (e *Element) CachedIsEnabled() (bool, error) {
	return e.boolProperty("CachedIsEnabled", (*win.IUIAutomationElement).Get_CachedIsEnabled)
}

// CachedBoundingRectangle is BoundingRectangle from the cache.
func (e *Element) CachedBoundingRectangle() (image.Rectangle, error) {
	var r win.RECT
	err := e.call(func(p *win.IUIAutomationElement) error {
		return check("CachedBoundingRectangle", p.Get_CachedBoundingRectangle(&r))
	})
	return image.Rect(int(r.Left), int(r.Top), int(r.Right), int(r.Bottom)), err
}

// PropertyValue returns the current value of any property, with the Go
// type listed next to the PropertyID constant: a string, a bool or an int
// (a ControlType for PropertyControlType), and a float64 for
// floating-point properties. It is nil for properties of other types, such
// as rectangles and elements, and for a property the element does not
// have. Any property ID of UI Automation works, also one without a
// constant in this package, for example PropertyID(30045), the value of
// the Value pattern.
func (e *Element) PropertyValue(id PropertyID) (any, error) {
	return e.propertyValue("GetCurrentPropertyValue", id, (*win.IUIAutomationElement).GetCurrentPropertyValue)
}

// CachedPropertyValue is PropertyValue from the cache.
func (e *Element) CachedPropertyValue(id PropertyID) (any, error) {
	return e.propertyValue("GetCachedPropertyValue", id, (*win.IUIAutomationElement).GetCachedPropertyValue)
}

func (e *Element) propertyValue(op string, id PropertyID, get func(*win.IUIAutomationElement, int32, *win.VARIANT) win.HRESULT) (any, error) {
	var value any
	err := e.call(func(p *win.IUIAutomationElement) error {
		var v win.VARIANT
		if err := check(op, get(p, int32(id), &v)); err != nil {
			return err
		}
		defer win.VariantClear(&v)
		value = variantValue(id, &v)
		return nil
	})
	return value, err
}

// CachedChildren returns the cached children of the element, in tree
// order, when the cache request had a Scope with ScopeChildren or
// ScopeDescendants: the children that match its Filter, with their
// properties cached. It returns nil for an element without such children,
// and nil or an error if the request did not cache children.
func (e *Element) CachedChildren() ([]*Element, error) {
	var children []*Element
	err := e.call(func(p *win.IUIAutomationElement) error {
		var array *win.IUIAutomationElementArray
		if err := check("GetCachedChildren", p.GetCachedChildren(&array)); err != nil {
			return err
		}
		var err error
		children, err = e.a.elements("GetCachedChildren", array)
		return err
	})
	return children, err
}

// CachedParent returns the parent of the element in the cached tree, or
// nil for the element the cache request was used for.
func (e *Element) CachedParent() (*Element, error) {
	var parent *Element
	err := e.call(func(p *win.IUIAutomationElement) error {
		var q *win.IUIAutomationElement
		if err := check("GetCachedParent", p.GetCachedParent(&q)); err != nil {
			return err
		}
		parent = e.a.newElement(q)
		return nil
	})
	return parent, err
}
