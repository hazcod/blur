// Copyright 2017 by the rasterx Authors. All rights reserved.
//_
// created: 2017 by S.R.Wiley

package rasterx

import (
	"golang.org/x/image/math/fixed"
	"math"
)

// Dasher struct extends the Stroker and can draw
// dashed lines with end capping
type Dasher struct {
	Stroker
	Dashes                    []fixed.Int26_6
	dashPlace                 int
	firstDashIsGap, dashIsGap bool
	deltaDash                 float64
	DashOffset                fixed.Int26_6
	startCapPending           bool
	sgm                       Rasterx
	// sgm allows us to switch between dashing
	// and non-dashing rasterizers in the SetStroke function.
}

// joinF overides stroker joinF during dashed stroking, because we need to slightly modify
// the the call as below to handle the case of the join being in a dash gap.
func (r *Dasher) joinF() {
	if len(r.Dashes) == 0 || !r.inStroke || (!r.dashIsGap && r.deltaDash > 0) {
		r.Stroker.joinF()
	} else {
		// A dash starting at a command boundary has no join with the previous
		// command. Its cap and first edge use the new command's tangent.
		r.a = r.trailPoint.P
		r.ln = r.trailPoint.LNorm
	}
}

// Start starts a dashed line
func (r *Dasher) Start(a fixed.Point26_6) {
	r.startCapPending = false
	// Advance dashPlace to the dashOffset start point and set deltaDash
	if len(r.Dashes) > 0 {
		r.deltaDash = float64(r.DashOffset)
		r.dashIsGap = false
		r.dashPlace = 0
		for r.deltaDash > float64(r.Dashes[r.dashPlace]) {
			r.deltaDash -= float64(r.Dashes[r.dashPlace])
			r.dashIsGap = !r.dashIsGap
			r.dashPlace++
			if r.dashPlace == len(r.Dashes) {
				r.dashPlace = 0
			}
		}
		r.firstDashIsGap = r.dashIsGap
	}
	r.Stroker.Start(a)
}

// lineF overides stroker lineF to modify the the call as below
// while performing the join in a dashed stroke.
func (r *Dasher) lineF(b fixed.Point26_6) {
	var bnorm fixed.Point26_6
	a := r.a // Copy local a since r.a is going to change during stroke operation
	ba := b.Sub(a)
	segLen := math.Hypot(float64(ba.X), float64(ba.Y))
	if segLen == 0 {
		return
	}
	length := segLen
	nlt := 0.0
	if b == r.leadPoint.P { // End of segment
		bnorm = r.leadPoint.TNorm // Use more accurate leadPoint tangent
	} else {
		bnorm = turnPort90(ToLength(b.Sub(a), r.u)) // Intra segment normal
	}
	if r.startCapPending {
		r.CapT(&r.Filler, a, Invert(r.ln))
		r.a = a
		r.startCapPending = false
	}
	for segLen+r.deltaDash >= float64(r.Dashes[r.dashPlace]) {
		nl := float64(r.Dashes[r.dashPlace]) - r.deltaDash
		nlt += nl
		atEnd := nl == segLen
		point := fixed.Point26_6{
			X: a.X + fixed.Int26_6(math.Round(float64(ba.X)*nlt/length)),
			Y: a.Y + fixed.Int26_6(math.Round(float64(ba.Y)*nlt/length)),
		}
		// Defer a new dash's start cap at a vertex until its outgoing tangent
		// is known. If the path ends here, there is no new dash to paint.
		r.startCapPending = atEnd && r.dashIsGap
		r.dashLineStrokeBit(point, bnorm, r.startCapPending)
		r.dashIsGap = !r.dashIsGap
		segLen -= nl
		r.deltaDash = 0
		r.dashPlace++
		if r.dashPlace == len(r.Dashes) {
			r.dashPlace = 0
		}
		if atEnd {
			return
		}
	}
	r.deltaDash += segLen
	r.dashLineStrokeBit(b, bnorm, true)
}

// SetStroke set the parameters for stroking a line. width is the width of the line, miterlimit is the miter cutoff
// value for miter, arc, miterclip and arcClip joinModes. CapL and CapT are the capping functions for leading and trailing
// line ends. If one is nil, the other function is used at both ends. gp is the gap function that determines how a
// gap on the convex side of two lines joining is filled. jm is the JoinMode for curve segments. Dashes is the values for
// the dash pattern. Pass in nil or an empty slice for no dashes. dashoffset is the starting offset into the dash array.
// Odd-length patterns repeat once. The offset is normalized to that period;
// pattern values and the offset are rounded to the nearest 1/64 coordinate unit.
func (r *Dasher) SetStroke(width, miterLimit fixed.Int26_6, capL, capT CapFunc, gp GapFunc, jm JoinMode, dashes []float64, dashOffset float64) {
	r.Stroker.SetStroke(width, miterLimit, capL, capT, gp, jm)

	r.Dashes = r.Dashes[:0] // clear the dash array
	if len(dashes) == 0 {
		r.sgm = &r.Stroker // This is just plain stroking
		return
	}
	// Dashed Stroke
	// Convert the float dash array and offset to fixed point and attach to the Filler
	oneIsPos := false // Check to see if at least one dash is > 0
	for _, v := range dashes {
		fv := fixed.Int26_6(math.Round(v * 64))
		if fv <= 0 { // Negatives are considered 0s.
			fv = 0
		} else {
			oneIsPos = true
		}
		r.Dashes = append(r.Dashes, fv)
	}
	if oneIsPos == false {
		r.Dashes = r.Dashes[:0]
		r.sgm = &r.Stroker // This is just plain stroking
		return
	}
	if len(r.Dashes)%2 != 0 {
		r.Dashes = append(r.Dashes, r.Dashes...)
	}
	period := 0.0
	for _, dash := range r.Dashes {
		period += float64(dash)
	}
	offset := math.Mod(dashOffset*64, period)
	if offset < 0 {
		offset += period
	}
	r.DashOffset = fixed.Int26_6(math.Round(offset))
	r.sgm = r // Use the full dasher
}

// Stop terminates a dashed line
func (r *Dasher) Stop(isClosed bool) {
	if len(r.Dashes) == 0 {
		r.Stroker.Stop(isClosed)
		return
	}
	if r.inStroke == false {
		return
	}
	if isClosed && r.a != r.firstP.P {
		r.LineSeg(r.sgm, r.firstP.P)
	}
	ra := &r.Filler
	if isClosed && !r.firstDashIsGap && !r.dashIsGap && !r.startCapPending && r.deltaDash > 0 { // closed connect w/o caps
		a := r.a
		r.firstP.TNorm = r.leadPoint.TNorm
		r.firstP.RT = r.leadPoint.RT
		r.firstP.TTan = r.leadPoint.TTan
		ra.Start(r.firstP.P.Sub(r.firstP.TNorm))
		ra.Line(a.Sub(r.ln))
		ra.Start(a.Add(r.ln))
		ra.Line(r.firstP.P.Add(r.firstP.TNorm))
		r.Joiner(r.firstP)
		r.firstP.blackWidowMark(ra)
	} else { // Cap open ends
		if !r.dashIsGap && !r.startCapPending {
			r.CapL(ra, r.leadPoint.P, r.leadPoint.TNorm)
		}
		if !r.firstDashIsGap {
			r.CapT(ra, r.firstP.P, Invert(r.firstP.LNorm))
		}
	}
	r.inStroke = false
}

// dashLineStrokeBit is a helper function that reduces code redundancey in the
// lineF function.
func (r *Dasher) dashLineStrokeBit(b, bnorm fixed.Point26_6, dontClose bool) {
	if !r.dashIsGap { // Moving from dash to gap
		a := r.a
		ra := &r.Filler
		ra.Start(b.Sub(bnorm))
		ra.Line(a.Sub(r.ln))
		ra.Start(a.Add(r.ln))
		ra.Line(b.Add(bnorm))
		if dontClose == false {
			r.CapL(ra, b, bnorm)
		}
	} else { // Moving from gap to dash
		if dontClose == false {
			ra := &r.Filler
			r.CapT(ra, b, Invert(bnorm))
		}
	}
	r.a = b
	r.ln = bnorm
}

// Line for Dasher is here to pass the dasher sgm to LineP
func (r *Dasher) Line(b fixed.Point26_6) {
	r.LineSeg(r.sgm, b)
}

// QuadBezier for dashing
func (r *Dasher) QuadBezier(b, c fixed.Point26_6) {
	r.quadBezierf(r.sgm, b, c)
}

// CubeBezier starts a stroked cubic bezier.
// It is a low level function exposed for the purposes of callbacks
// and debugging.
func (r *Dasher) CubeBezier(b, c, d fixed.Point26_6) {
	r.cubeBezierf(r.sgm, b, c, d)
}

// NewDasher returns a Dasher ptr with default values.
// A Dasher has all of the capabilities of a Stroker, Filler, and Scanner, plus the ability
// to stroke curves with solid lines. Use SetStroke to configure with non-default
// values.
func NewDasher(width, height int, scanner Scanner) *Dasher {
	r := new(Dasher)
	r.Scanner = scanner
	r.SetBounds(width, height)
	r.SetWinding(true)
	r.SetStroke(1*64, 4*64, ButtCap, nil, FlatGap, MiterClip, nil, 0)
	r.sgm = &r.Stroker
	return r
}
