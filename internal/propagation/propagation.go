// Package propagation integrates the Paris crack-growth law
//
//	da/dN = C * (ΔK)^m,   ΔK = Δσ·sqrt(π·a)·β(a/W)
//
// under constant- or variable-amplitude (blocked) spectra, locates the
// critical crack size (fracture toughness or net-section yield, whichever
// comes first) and provides the growth/exposure primitives used by
// coefficient calibration and remaining-life planning.
//
// Units: crack lengths in m, stresses in MPa, K in MPa·sqrt(m), C in
// (m/cycle)/(MPa·sqrt(m))^m.
package propagation

import (
	"errors"
	"math"

	"crackstation/internal/geometry"
	"crackstation/internal/spectrum"
)

// Material holds the Paris-law and fracture parameters of a material grade.
type Material struct {
	ParisM        float64 // Paris exponent m
	ParisC        float64 // Paris coefficient C
	FractureKIC   float64 // plane-strain fracture toughness, MPa·sqrt(m)
	YieldStrength float64 // sigma_y used for net-section yielding, MPa
}

// Params is everything the integrator needs for one calculation. When
// OverrideCriticalA > 0 it replaces the computed critical characteristic
// length (used to reproduce the reference example, which specifies the
// critical size directly). BetaOne forces beta ≡ 1 (infinite-plate case).
type Params struct {
	Geo               geometry.Type
	Width             float64
	Material          Material
	Blocks            []spectrum.Block
	OverrideCriticalA float64
	BetaOne           bool
}

// DefaultTol is the default relative tolerance of the adaptive integrator.
//
// On the reference example (constant amplitude, beta ≡ 1) every cell is
// integrated by the closed-form power-law antiderivative, so the result is
// exact to machine precision. For finite-width geometries each cell freezes
// beta at its midpoint and uses the same closed form; adaptive halving with
// Richardson correction keeps the global relative error below ~1e-8, at the
// cost of a few thousand cells per remaining-life call (tens of
// microseconds). Docs in docs/accuracy.md.
const DefaultTol = 1e-8

const maxDepth = 60

// beta evaluates the geometry factor, honoring the infinite-plate flag.
func (p Params) beta(a float64) float64 {
	if p.BetaOne {
		return 1
	}
	return geometry.Beta(p.Geo, a, p.Width)
}

// geoMax returns the maximum admissible characteristic length.
func (p Params) geoMax() float64 {
	if p.BetaOne {
		return math.Inf(1)
	}
	return geometry.MaxCharacteristic(p.Geo, p.Width)
}

// CriticalA returns the critical characteristic crack length: the smaller of
// the fracture root K(a;σmax*)=KIC and the net-section-yield root
// σnet(a;σmax*)=σy. ok is false when the plate is already critical at a=0.
func CriticalA(p Params) (aC, fractureA, yieldA float64, ok bool) {
	if p.OverrideCriticalA > 0 {
		return p.OverrideCriticalA, p.OverrideCriticalA, math.NaN(), true
	}
	aMax := p.geoMax()
	sMax := spectrum.MaxStress(p.Blocks)

	// --- Net-section yielding: sMax * W / lig(a) = sigma_y.
	if sMax >= p.Material.YieldStrength {
		yieldA = 0
	} else {
		ligW := sMax / p.Material.YieldStrength // remaining lig/W
		if p.BetaOne {
			yieldA = math.Inf(1)
		} else {
			switch p.Geo {
			case geometry.CenterThrough:
				yieldA = p.Width * (1 - ligW) / 2
			case geometry.Edge:
				yieldA = p.Width * (1 - ligW)
			}
		}
	}

	// --- Fracture: K(a) = sMax sqrt(pi a) beta(a/W) = KIC, monotone.
	fractureA = math.Inf(1)
	if sMax > 0 && !p.BetaOne {
		aLo, aHi := 0.0, aMax
		if kHi := geometry.StressIntensity(p.Geo, sMax, math.Nextafter(aMax, 0), p.Width); math.IsInf(kHi, 1) || kHi >= p.Material.FractureKIC {
			for i := 0; i < 200; i++ {
				mid := (aLo + aHi) / 2
				if geometry.StressIntensity(p.Geo, sMax, mid, p.Width) > p.Material.FractureKIC {
					aHi = mid
				} else {
					aLo = mid
				}
			}
			fractureA = (aLo + aHi) / 2
		}
	}
	aC = math.Min(fractureA, math.Min(yieldA, aMax))
	return aC, fractureA, yieldA, aC > 0 && !math.IsInf(aC, 1)
}

// TargetCritical evaluates the critical characteristic length.
func (p Params) TargetCritical() float64 {
	aC, _, _, ok := CriticalA(p)
	if !ok {
		// Already critical at a=0 (or no finite root): callers starting at
		// positive a get zero life via the aTarget <= a0 guard.
		return 0
	}
	return aC
}

// mergedWeight computes, for a cell on which beta is frozen at aMid, the
// combined weight w such that da/day = w · a^(m/2):
//
//	w = C · π^(m/2) · β(aMid)^m · Σ_j cyclesPerDay_j · Δσ_j^m.
func (p Params) mergedWeight(aMid float64) float64 {
	bm := math.Pow(p.beta(aMid), p.Material.ParisM)
	w := 0.0
	for _, b := range p.Blocks {
		w += b.CyclesPerDay * math.Pow(b.StressAmp, p.Material.ParisM)
	}
	return p.Material.ParisC * math.Pow(math.Pi, p.Material.ParisM/2) * bm * w
}

// integratePower returns ∫_x1^x2 1/(w a^q) da, q = m/2 (log at m=2).
func integratePower(w, m, x1, x2 float64) float64 {
	q := m / 2
	if math.Abs(q-1) < 1e-12 {
		return math.Log(x2/x1) / w
	}
	e := 1 - q
	return (math.Pow(x2, e) - math.Pow(x1, e)) / (w * e)
}

// analyticCell integrates "days" (da/(w·a^q)) over [x1,x2] with beta frozen
// at the cell midpoint. All blocks share exponent m, so their weights merge
// into a single power.
func (p Params) analyticCell(x1, x2 float64) float64 {
	w := p.mergedWeight((x1 + x2) / 2)
	if w <= 0 {
		return 0
	}
	return integratePower(w, p.Material.ParisM, x1, x2)
}

// adaptiveDays integrates ∫ da / (da/day) from a0 to aTarget with adaptive
// midpoint subdivision and Richardson correction. Units: days (blocks carry
// cycles/day). With CyclesPerDay=1 the number equals cycles.
func (p Params) adaptiveDays(a0, aTarget, tol float64) float64 {
	var rec func(x1, x2, whole, tAbs float64, depth int) float64
	rec = func(x1, x2, whole, tAbs float64, depth int) float64 {
		mid := (x1 + x2) / 2
		l := p.analyticCell(x1, mid)
		r := p.analyticCell(mid, x2)
		split := l + r
		err := math.Abs(split - whole)
		if depth >= maxDepth || err <= 15*tAbs {
			return split + (split-whole)/15
		}
		return rec(x1, mid, l, tAbs/2, depth+1) +
			rec(mid, x2, r, tAbs/2, depth+1)
	}
	whole := p.analyticCell(a0, aTarget)
	return rec(a0, aTarget, whole, tol*math.Abs(whole), 0)
}

// DaysTo integrates days to grow from a0 to aTarget.
func DaysTo(p Params, a0, aTarget float64) (float64, error) {
	if a0 <= 0 {
		return 0, errors.New("propagation: starting crack length must be positive")
	}
	if aTarget <= a0 {
		return 0, nil
	}
	return p.adaptiveDays(a0, aTarget, DefaultTol), nil
}

// DaysToCritical returns days for the characteristic crack to grow from a0
// to the critical size. Returns (0, nil) when already at/past critical.
func DaysToCritical(p Params, a0 float64) (float64, error) {
	if a0 <= 0 {
		return 0, errors.New("propagation: starting crack length must be positive")
	}
	aC, _, _, ok := CriticalA(p)
	if !ok || aC <= a0 {
		return 0, nil
	}
	return DaysTo(p, a0, aC)
}

// Grow advances the crack from a0 over elapsed days, stopping at the
// critical length if it intervenes. Bisection on the adaptive day integral.
func Grow(p Params, a0, days float64) (aEnd float64, reachedCritical bool, err error) {
	if a0 <= 0 {
		return 0, false, errors.New("propagation: starting crack length must be positive")
	}
	aC, _, _, ok := CriticalA(p)
	if !ok || a0 >= aC {
		return a0, true, nil
	}
	if days <= 0 {
		return a0, false, nil
	}
	criticalDays := p.adaptiveDays(a0, aC, DefaultTol)
	if criticalDays <= days {
		return aC, true, nil
	}
	lo, hi := a0, aC
	for i := 0; i < 100; i++ {
		mid := (lo + hi) / 2
		if p.adaptiveDays(a0, mid, DefaultTol) < days {
			lo = mid
		} else {
			hi = mid
		}
	}
	return (lo + hi) / 2, false, nil
}

// GrowthExposure returns the beta-aware integral
//
//	Φ(a1→a2) = ∫_{a1}^{a2} a^{-m/2} / β(a/W)^m da
//
// which is the quantity linear in C in the growth equation
//
//	C · π^(m/2) · Σ_j perDay_j·Δσ_j^m · days = Φ.
//
// It uses the same adaptive midpoint scheme (here weight = β^-m only).
func GrowthExposure(geo geometry.Type, width, mParis, a1, a2 float64) float64 {
	if a2 <= a1 {
		return 0
	}
	p := Params{
		Geo:      geo,
		Width:    width,
		Material: Material{ParisM: mParis, ParisC: 1},
		Blocks: []spectrum.Block{{
			StressAmp: 1 / math.Sqrt(math.Pi), // cancels π^(m/2)
			// Δσ^m π^(m/2) = 1, so weight = β^m → integrand a^-m/2 / β^m.
			MaxStress:    0,
			CyclesPerDay: 1,
		}},
	}
	return p.adaptiveDays(a1, a2, DefaultTol)
}
