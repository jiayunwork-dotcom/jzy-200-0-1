// Package calibration fits a location-specific Paris coefficient C from its
// inspection history. The Paris exponent m is fixed by the material grade
// and is never fitted.
//
// Observation model. Let the first FOUND measurement define the anchor:
// characteristic length a0 at date t0, exposure X = Σ W·days = 0 there,
// q0 = Φ(a0). Every later state then satisfies (growth only, no intercept
// freedom — the measured state at t0 is the actual state):
//
//	Φ(a_k) = q0 + C · X_k,
//	Φ(a)   = ∫ a^{-m/2}/β(a/W)^m da,
//	W      = π^(m/2) Σ_j cyclesPerDay_j Δσ_j^m.
//
// Found points yield residuals r_k = (q_k - q0) - C·X_k and the
// unconstrained estimator is least squares through the origin on (X_k,
// q_k-q0). A "not found" record at t with detection limit l says only
// a(t) <= a_l, i.e. (monotonic Φ):
//
//	C · X_t <= Φ(a_l) - q0 =: U_t   (a one-sided upper bound on C).
//
// Each constraint is therefore a scalar cap C <= cap_t; the feasible
// estimate is min(unconstrained-OLS, min cap_t), C >= 0.
//
// Choice and effect on inspection intervals. "Not found" results are used
// ONLY as upper-bound censoring constraints, never as fake measured lengths.
// Because every cap is one-sided, including them can only move the fitted C
// downward relative to ignoring them; a smaller C predicts slower growth and
// hence LONGER inspection intervals — slightly LESS conservative (more
// aggressive spacing) than discarding not-found records, which is the
// information-preserving choice: a genuinely slow crack should be allowed to
// prove itself, while the safety factor applied in the schedule package
// keeps margin. When the caps contradict the found data (e.g. a later found
// crack longer than any prior limit allowed), the tightest cap wins and the
// residual RMS flags the inconsistency for the inspector.
//
// Fewer than two found points (anchor + one more) leaves C unidentifiable;
// the handbook prior C is returned with Calibrated=false.
package calibration

import (
	"math"

	"crackstation/internal/geometry"
	"crackstation/internal/propagation"
)

// Obs is one chronological inspection observation.
type Obs struct {
	Found  bool    // true = crack measured; false = "not found"
	A      float64 // found: characteristic length a (m)
	LimitA float64 // not found: characteristic detection limit (m)
}

// Fitted records the calibration outcome.
type Fitted struct {
	C           float64
	Calibrated  bool    // false => fewer than 2 found points, prior used
	UsedNF      int     // number of not-found constraints at the bound
	ResidualRMS float64 // RMS of found-point residuals in q units
}

// Phi is the beta-aware growth exposure to length a (lower limit fixed at a
// small epsilon; only differences between Phi values enter the fit).
func Phi(geo geometry.Type, width, m, a float64) float64 {
	if a <= 0 {
		return 0
	}
	const eps = 1e-9
	return propagation.GrowthExposure(geo, width, m, eps, a)
}

// Fit performs the anchored, censoring-aware least-squares fit.
//
//	m      material Paris exponent (fixed)
//	priorC material handbook C, returned when Calibrated=false
//	obs    observations CHRONOLOGICAL, each paired with X:
//	X      exposure per obs, X_k = Σ intervals W·days from the first found
//	       observation (0 at the anchor; X <= 0 for pre-anchor not-founds)
func Fit(geo geometry.Type, width, m, priorC float64, obs []Obs, x []float64) Fitted {
	anchor := -1
	for k := range obs {
		if obs[k].Found {
			anchor = k
			break
		}
	}
	if anchor < 0 {
		return Fitted{C: priorC, Calibrated: false}
	}
	foundAfter := 0
	for k := anchor + 1; k < len(obs); k++ {
		if obs[k].Found {
			foundAfter++
		}
	}
	if foundAfter == 0 {
		return Fitted{C: priorC, Calibrated: false}
	}

	q0 := Phi(geo, width, m, obs[anchor].A)

	// Unconstrained through-origin LS: C = Σ X(q-q0)/ΣX² over found points.
	var num, den float64
	for k := anchor + 1; k < len(obs); k++ {
		if !obs[k].Found {
			continue
		}
		qk := Phi(geo, width, m, obs[k].A)
		num += x[k] * (qk - q0)
		den += x[k] * x[k]
	}
	if den <= 1e-300 {
		return Fitted{C: priorC, Calibrated: false}
	}
	cOLS := num / den
	if cOLS < 0 { // measured shrinkage is nonphysical; clamp
		cOLS = 0
	}

	// Censoring caps from not-found records after the anchor.
	C := cOLS
	used := 0
	const capTol = 1e-9
	var tightestCap float64
	haveCap := false
	for k := anchor + 1; k < len(obs); k++ {
		if obs[k].Found || x[k] <= 0 {
			continue
		}
		u := Phi(geo, width, m, obs[k].LimitA) - q0
		if u <= 0 { // limit below anchor crack: record contradicts anchor
			u = 0
		}
		cap := u / x[k]
		if !haveCap || cap < tightestCap {
			tightestCap, haveCap = cap, true
		}
	}
	if haveCap && tightestCap < C {
		C = tightestCap
	}
	if haveCap && math.Abs(C-tightestCap) <= capTol*math.Max(1, math.Abs(C)) {
		used = 1
	}

	// Residual RMS over found points.
	var ss float64
	n := 1
	for k := anchor + 1; k < len(obs); k++ {
		if !obs[k].Found {
			continue
		}
		qk := Phi(geo, width, m, obs[k].A)
		r := (qk - q0) - C*x[k]
		ss += r * r
		n++
	}
	rms := math.Sqrt(ss / float64(n))

	if !(C > 0) || math.IsInf(C, 0) || math.IsNaN(C) {
		return Fitted{C: priorC, Calibrated: false}
	}
	return Fitted{C: C, Calibrated: true, UsedNF: used, ResidualRMS: rms}
}
