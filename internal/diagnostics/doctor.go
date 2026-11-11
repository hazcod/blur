// Package diagnostics exposes content-free health data for local doctor tools.
package diagnostics

import "github.com/hazcod/blur/internal/memory"

type IntegrityState string
type Freshness string
type CapabilityState string

const (
	Healthy     IntegrityState  = "healthy"
	Unchecked   IntegrityState  = "unchecked"
	Corrupt     IntegrityState  = "corrupt"
	Fresh       Freshness       = "fresh"
	Stale       Freshness       = "stale"
	Missing     Freshness       = "missing"
	Available   CapabilityState = "available"
	Disabled    CapabilityState = "disabled"
	Unavailable CapabilityState = "unavailable"
)

type Report struct {
	StoreVersion           uint32                     `json:"store_version"`
	Integrity              IntegrityState             `json:"integrity"`
	Queue                  memory.Stats               `json:"queue"`
	StaleDerivations       int                        `json:"stale_derivations"`
	QuarantinedDerivations int                        `json:"quarantined_derivations"`
	DerivationsSampled     int                        `json:"derivations_sampled"`
	DerivationCountsCapped bool                       `json:"derivation_counts_capped"`
	Today                  Freshness                  `json:"today_freshness"`
	Capabilities           map[string]CapabilityState `json:"capabilities"`
}

type Inputs struct {
	StoreVersion                             uint32
	Integrity                                IntegrityState
	Queue                                    memory.Stats
	StaleDerivations, QuarantinedDerivations int
	DerivationsSampled                       int
	DerivationCountsCapped                   bool
	Today                                    Freshness
	Capabilities                             map[string]CapabilityState
}

func Build(in Inputs) Report {
	state := in.Integrity
	if state != Healthy && state != Unchecked && state != Corrupt {
		state = Unchecked
	}
	fresh := in.Today
	if fresh != Fresh && fresh != Stale && fresh != Missing {
		fresh = Missing
	}
	stale, quarantined := in.StaleDerivations, in.QuarantinedDerivations
	if stale < 0 {
		stale = 0
	}
	if quarantined < 0 {
		quarantined = 0
	}
	caps := make(map[string]CapabilityState, len(in.Capabilities))
	for name, value := range in.Capabilities {
		if !safeName(name) {
			continue
		}
		if value != Available && value != Disabled && value != Unavailable {
			value = Unavailable
		}
		caps[name] = value
	}
	sampled := in.DerivationsSampled
	if sampled < 0 {
		sampled = 0
	}
	return Report{StoreVersion: in.StoreVersion, Integrity: state, Queue: in.Queue, StaleDerivations: stale, QuarantinedDerivations: quarantined, DerivationsSampled: sampled, DerivationCountsCapped: in.DerivationCountsCapped, Today: fresh, Capabilities: caps}
}

func safeName(name string) bool {
	if len(name) == 0 || len(name) > 32 {
		return false
	}
	for i := range name {
		c := name[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}
