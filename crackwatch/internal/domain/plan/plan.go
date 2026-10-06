// Package plan turns a location's resolved inputs and inspection findings
// into the remaining-life assessment and the next inspection schedule.
package plan

import (
	"time"

	"crackwatch/internal/domain/calibration"
	"crackwatch/internal/domain/geometry"
	"crackwatch/internal/domain/growth"
	"crackwatch/internal/domain/material"
	"crackwatch/internal/domain/spectrum"
)

// Input bundles everything a plan is computed from.
type Input struct {
	Geometry     geometry.Geometry
	Blocks       []spectrum.Block
	Material     material.Params
	SafetyFactor float64

	// Findings are the location's effective observations sorted ascending by
	// InspectedAt (corrections already resolved).
	Findings []calibration.Observation

	// Now is the assessment instant (the "as of" date for historical
	// queries). Next-inspection scheduling starts from the last record date
	// when it is later than Now, otherwise from Now.
	Now time.Time
}

// Result is the computed assessment without version binding (which the
// service layer adds).
type Result struct {
	CurrentA          float64 // characteristic size used as growth start [m]
	CurrentIsBound    bool    // started from an NDF detection limit
	CalibratedC       float64
	CatalogC          float64
	CWasCalibrated    bool
	ClampedByNDF      bool
	CalibInconsistent bool

	CriticalA   float64
	FailureMode material.FailureMode

	RemainingCycles float64
	RemainingDays   float64
	IntervalDays    float64
	NextInspection  time.Time
	NoFinding       bool
}

// Compute builds the assessment. The load spectrum must already be validated.
func Compute(in Input) Result {
	r := Result{
		CatalogC:    in.Material.C,
		CalibratedC: in.Material.C,
	}

	r.CriticalA, r.FailureMode = material.CriticalCharacteristic(
		in.Geometry, in.Material, spectrum.MaxStress(in.Blocks))

	if len(in.Findings) == 0 {
		// Never inspected with a result: schedule from commissioning with
		// catalog parameters and no measured starting size.
		r.NoFinding = true
		r.IntervalDays = 0
		return r
	}

	cal := calibration.Estimate(in.Geometry, in.Blocks, in.Material.M, in.Material.C, in.Findings)
	r.CalibratedC = cal.C
	r.CWasCalibrated = cal.Calibrated
	r.ClampedByNDF = cal.ClampedByNDF
	r.CalibInconsistent = cal.Inconsistent

	last := in.Findings[len(in.Findings)-1]
	r.CurrentA = last.A
	r.CurrentIsBound = last.UpperBound

	if last.A >= r.CriticalA {
		// At/beyond critical: inspect immediately.
		r.NextInspection = last.At
		return r
	}

	r.RemainingCycles = growth.Cycles(in.Geometry, in.Blocks, cal.C, in.Material.M,
		last.A, r.CriticalA)
	_, perDay := growth.Intensity(in.Blocks, in.Material.M)
	if perDay > 0 {
		r.RemainingDays = r.RemainingCycles / perDay
	}

	if in.SafetyFactor > 0 {
		r.IntervalDays = r.RemainingDays / in.SafetyFactor
	}
	start := in.Now
	if last.At.After(start) {
		start = last.At
	}
	r.NextInspection = start.Add(days(r.IntervalDays))
	return r
}

func days(d float64) time.Duration {
	return time.Duration(d * float64(24*time.Hour))
}
