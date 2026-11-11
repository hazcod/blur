// Copyright 2026 The Walk Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build windows

package uia

import (
	"fmt"
	"strings"

	"github.com/lpintes/walk/internal/win"
)

// Condition selects elements for FindFirst, FindAll and TreeWalker. A
// Condition is a plain Go value; it can be reused and needs no release. A
// nil Condition matches every element, like True.
type Condition interface {
	// create returns the UI Automation condition. It runs on the thread
	// of the Automation; the caller releases the result.
	create(uia *win.IUIAutomation) (*win.IUIAutomationCondition, error)
}

// createCondition is cond.create, with True for a nil cond.
func createCondition(uia *win.IUIAutomation, cond Condition) (*win.IUIAutomationCondition, error) {
	if cond == nil {
		cond = True()
	}
	return cond.create(uia)
}

type boolCondition bool

func (c boolCondition) create(uia *win.IUIAutomation) (*win.IUIAutomationCondition, error) {
	var p *win.IUIAutomationCondition
	if c {
		if hr := uia.CreateTrueCondition(&p); win.FAILED(hr) {
			return nil, hresultError("CreateTrueCondition", hr)
		}
	} else if hr := uia.CreateFalseCondition(&p); win.FAILED(hr) {
		return nil, hresultError("CreateFalseCondition", hr)
	}
	return p, nil
}

// True returns a condition every element matches.
func True() Condition { return boolCondition(true) }

// False returns a condition no element matches.
func False() Condition { return boolCondition(false) }

// MatchFlags change how PropertyMatch compares strings.
type MatchFlags int32

const (
	// MatchIgnoreCase compares without regard to case.
	MatchIgnoreCase MatchFlags = win.PropertyConditionFlags_IgnoreCase
	// MatchSubstring matches if the property contains the value. It
	// needs Windows 10 version 1809 or later.
	MatchSubstring MatchFlags = win.PropertyConditionFlags_MatchSubstring
)

type propertyCondition struct {
	id    PropertyID
	value any
	flags MatchFlags
}

func (c propertyCondition) create(uia *win.IUIAutomation) (*win.IUIAutomationCondition, error) {
	var v win.VARIANT
	switch value := c.value.(type) {
	case string:
		if strings.ContainsRune(value, 0) {
			return nil, fmt.Errorf("uia: property condition: string %q contains NUL", value)
		}
		v.SetBSTR(win.SysAllocString(value))
		defer win.VariantClear(&v)
	case bool:
		if value {
			v.SetBool(win.VARIANT_TRUE)
		} else {
			v.SetBool(win.VARIANT_FALSE)
		}
	case int:
		v.SetLong(int32(value))
	case int32:
		v.SetLong(value)
	case ControlType:
		v.SetLong(int32(value))
	default:
		return nil, fmt.Errorf("uia: property condition: unsupported value type %T", c.value)
	}

	var p *win.IUIAutomationCondition
	if c.flags != 0 {
		if hr := uia.CreatePropertyConditionEx(int32(c.id), &v, int32(c.flags), &p); win.FAILED(hr) {
			return nil, hresultError("CreatePropertyConditionEx", hr)
		}
	} else if hr := uia.CreatePropertyCondition(int32(c.id), &v, &p); win.FAILED(hr) {
		return nil, hresultError("CreatePropertyCondition", hr)
	}
	return p, nil
}

// Property returns a condition matching elements whose property id has
// the value. The value must have the Go type listed next to the
// PropertyID constant: a string, a bool, an int (int32 works too) or a
// ControlType. Strings are compared exactly.
func Property(id PropertyID, value any) Condition {
	return propertyCondition{id: id, value: value}
}

// PropertyMatch returns a condition matching elements whose string
// property id matches value as flags say.
func PropertyMatch(id PropertyID, value string, flags MatchFlags) Condition {
	return propertyCondition{id: id, value: value, flags: flags}
}

// ByName returns a condition matching elements with the name.
func ByName(name string) Condition { return Property(PropertyName, name) }

// ByAutomationID returns a condition matching elements with the automation
// ID.
func ByAutomationID(id string) Condition { return Property(PropertyAutomationID, id) }

// ByClassName returns a condition matching elements with the class name.
func ByClassName(name string) Condition { return Property(PropertyClassName, name) }

// ByControlType returns a condition matching elements of the control type.
func ByControlType(t ControlType) Condition { return Property(PropertyControlType, t) }

type andOrCondition struct {
	or         bool
	conditions []Condition
}

func (c andOrCondition) create(uia *win.IUIAutomation) (*win.IUIAutomationCondition, error) {
	switch len(c.conditions) {
	case 0:
		return boolCondition(!c.or).create(uia)
	case 1:
		return createCondition(uia, c.conditions[0])
	}

	created := make([]*win.IUIAutomationCondition, 0, len(c.conditions))
	defer func() {
		for _, p := range created {
			p.Release()
		}
	}()
	for _, cond := range c.conditions {
		p, err := createCondition(uia, cond)
		if err != nil {
			return nil, err
		}
		created = append(created, p)
	}

	var p *win.IUIAutomationCondition
	if c.or {
		if hr := uia.CreateOrConditionFromNativeArray(&created[0], int32(len(created)), &p); win.FAILED(hr) {
			return nil, hresultError("CreateOrConditionFromNativeArray", hr)
		}
	} else if hr := uia.CreateAndConditionFromNativeArray(&created[0], int32(len(created)), &p); win.FAILED(hr) {
		return nil, hresultError("CreateAndConditionFromNativeArray", hr)
	}
	return p, nil
}

// And returns a condition matching elements that match all conditions.
// Without conditions it matches every element.
func And(conditions ...Condition) Condition {
	return andOrCondition{conditions: conditions}
}

// Or returns a condition matching elements that match at least one of the
// conditions. Without conditions it matches no element.
func Or(conditions ...Condition) Condition {
	return andOrCondition{or: true, conditions: conditions}
}

type notCondition struct {
	condition Condition
}

func (c notCondition) create(uia *win.IUIAutomation) (*win.IUIAutomationCondition, error) {
	inner, err := createCondition(uia, c.condition)
	if err != nil {
		return nil, err
	}
	defer inner.Release()
	var p *win.IUIAutomationCondition
	if hr := uia.CreateNotCondition(inner, &p); win.FAILED(hr) {
		return nil, hresultError("CreateNotCondition", hr)
	}
	return p, nil
}

// Not returns a condition matching elements that do not match condition.
func Not(condition Condition) Condition {
	return notCondition{condition: condition}
}
