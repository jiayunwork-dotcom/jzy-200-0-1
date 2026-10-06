// Package calibration estimates a site-specific Paris coefficient C from the
// inspection history (the exponent m stays fixed at the material value).
//
// # Estimator for measured findings
//
// For two consecutive positive findings (characteristic sizes a_i, a_{i+1},
// inspection times t_i, t_{i+1}) the growth law predicts
//
//	t_{i+1} - t_i = G(a_i, a_{i+1}) / C,
//
// where G is growth.GrowthFactor (propagation days at unit C) under the
// location's current geometry and spectrum. Each pair yields
// C_k = G_k / (t_{i+1}-t_i). A weighted mean with weights proportional to the
// interval growth (maximum-likelihood for constant relative error) collapses
// to the end-point form
//
//	C_hat = G(a_first, a_last) / (t_last - t_first),
//
// which is order-independent: backfilling an earlier record gives exactly
// the same result as entering records chronologically. Calibration needs at
// least two positive findings; with fewer, the material (catalog) C is kept.
//
// Treatment of "not detected" (NDF) records
//
// An NDF record with detection limit d says only that the true crack size at
// that date was a <= a_d (a_d = d converted to characteristic size). It is a
// one-sided constraint, not a measurement at d:
//
//   - NDF at t_d between findings a_1(t_1) and a_2(t_2): reaching a_d takes
//     G/C days. "Not yet at a_d by t_d" requires G(a_1,a_d)/C > t_d-t_1,
//     hence C < G(a_1,a_d)/(t_d-t_1): an upper bound on C.
//
//   - The point estimate is clamped to the minimum such upper bound. NDF
//     records never push C upwards and never replace the measured estimate.
//
// Direction: clamping C down means slower predicted growth, longer remaining
// life and later inspections — so including NDFs makes the schedule LESS
// conservative (more aggressive) than ignoring them would, but never more
// aggressive than the censored data allow. Ignoring NDFs is more
// conservative; treating them as cracks of exactly size d would be the most
// conservative and is deliberately avoided (it would manufacture fictitious
// growth). Pairs of NDFs carry no information and are skipped.
package calibration

import (
	"math"
	"time"

	"crackwatch/internal/domain/geometry"
	"crackwatch/internal/domain/growth"
	"crackwatch/internal/domain/spectrum"
	"crackwatch/internal/model"
)

// Observation is one chronological, still-effective inspection observation.
type Observation struct {
	At         time.Time
	A          float64 // characteristic size [m]
	UpperBound bool    // not-detected: true size is <= A
}

// Result is the coefficient calibration outcome.
type Result struct {
	C            float64
	Calibrated   bool
	NDFBounds    int  // number of not-detected constraints applied
	ClampedByNDF bool // an upper-bound constraint reduced the estimate
	Inconsistent bool // contradictory data; catalog/measured C retained
}

// Estimate returns the Paris coefficient to use.
//
// cCatalog is the material-table C used when the history cannot support a
// site estimate. Observations must be supplied in ascending InspectedAt
// order (callers resolve corrections before sorting).
func Estimate(g geometry.Geometry, blocks []spectrum.Block, m, cCatalog float64, obs []Observation) Result {
	res := Result{C: cCatalog}

	type point struct {
		t time.Time
		a float64
	}
	var positives []point
	for _, o := range obs {
		if !o.UpperBound {
			positives = append(positives, point{o.At, o.A})
		}
	}

	if len(positives) >= 2 && positives[len(positives)-1].a > positives[0].a &&
		positives[len(positives)-1].t.After(positives[0].t) {
		first, last := positives[0], positives[len(positives)-1]
		gTotal := growth.GrowthFactor(g, blocks, m, first.a, last.a)
		elapsed := last.t.Sub(first.t).Hours() / 24
		if gTotal > 0 && elapsed > 0 {
			res.C = gTotal / elapsed
			res.Calibrated = true
		}
	}

	// Apply not-detected upper bounds relative to the closest measured
	// finding strictly before the NDF.
	cUpper := math.Inf(1)
	bounds := 0
	consistent := true
	for _, o := range obs {
		if !o.UpperBound {
			continue
		}
		limit := o.A

		// Closest positive finding strictly before the NDF.
		var before *point
		for i := range positives {
			if positives[i].t.Before(o.At) {
				before = &positives[i]
			}
		}
		if before != nil {
			if before.a > limit {
				// A crack already measured larger than the NDF limit is
				// physically inconsistent data.
				consistent = false
			} else if limit > before.a {
				dt := o.At.Sub(before.t).Hours() / 24
				if dt > 0 {
					cUpper = math.Min(cUpper,
						growth.GrowthFactor(g, blocks, m, before.a, limit)/dt)
					bounds++
				}
			}
		}
	}
	if bounds > 0 {
		res.NDFBounds = bounds
		if cUpper < res.C {
			res.C = cUpper
			res.ClampedByNDF = true
		}
	}
	res.Inconsistent = !consistent
	return res
}

// FromFindings converts model findings (sorted ascending by InspectedAt) to
// observations and runs Estimate.
func FromFindings(g geometry.Geometry, blocks []spectrum.Block, mExp, cCatalog float64, findings []model.Finding) Result {
	obs := make([]Observation, 0, len(findings))
	for _, f := range findings {
		obs = append(obs, Observation{At: f.Event.InspectedAt, A: f.A, UpperBound: f.UpperBound})
	}
	return Estimate(g, blocks, mExp, cCatalog, obs)
}
