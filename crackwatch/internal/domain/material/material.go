// Package material holds fracture-mechanics material properties and the
// critical-crack-size solver (fracture toughness or net-section yielding,
// whichever is reached first).
package material

import (
	"errors"
	"math"

	"crackwatch/internal/domain/geometry"
)

// Params are the fixed fracture-mechanics properties of a material grade.
//
// The Paris crack-growth law is
//
//	da/dN = C * (dK)^m
//
// with C in m/cycle for dK expressed in MPa*sqrt(m):
//
//   - K1c   fracture toughness [MPa*sqrt(m)]
//   - Sy    yield strength [MPa] (used for the net-section-yield criterion)
//   - M     Paris exponent m
//   - C     Paris coefficient [m/cycle per (MPa*sqrt(m))^m]
type Params struct {
	Grade string  `json:"grade"`
	K1c   float64 `json:"k1c"`
	Sy    float64 `json:"sy"`
	M     float64 `json:"m"`
	C     float64 `json:"c"`
}

// Validate returns field-named validation errors.
func (p Params) Validate() error {
	if p.K1c <= 0 {
		return errors.New("k1c: must be positive")
	}
	if p.Sy <= 0 {
		return errors.New("sy: must be positive")
	}
	if p.M <= 0 {
		return errors.New("m: Paris exponent must be positive")
	}
	if p.C <= 0 {
		return errors.New("c: Paris coefficient must be positive")
	}
	return nil
}

// Kmax returns the maximum stress intensity factor at characteristic size a
// under the block maximum stress sigmaMax.
func Kmax(g geometry.Geometry, sigmaMax, a float64) float64 {
	return sigmaMax * math.Sqrt(math.Pi*a) * g.Beta(a)
}

// FailureMode reports which critical condition was reached.
type FailureMode string

const (
	FailureFracture FailureMode = "fracture"        // Kmax reaches K1c
	FailureYield    FailureMode = "net_yield"       // net-section stress reaches Sy
	FailureGeometry FailureMode = "geometric_limit" // a reached the geometric bound
)

// CriticalCharacteristic finds, by bisection, the smallest characteristic
// size at which either Kmax = K1c or the net-section stress reaches Sy.
//
// sigmaMax is the largest stress the spectrum applies (its block maximum).
// The geometric bound (a = W/2 for center, W for edge) is always a valid
// upper limit; net yield is defined there if nowhere earlier.
func CriticalCharacteristic(g geometry.Geometry, mat Params, sigmaMax float64) (ac float64, mode FailureMode) {
	low := math.SmallestNonzeroFloat64 * 1e6 // safely positive, effectively zero
	high := g.MaxCharacteristic()
	// Pull high just inside the geometric bound to keep secant/net-section
	// expressions finite during the search.
	high *= 1 - 1e-12

	// Net-section yield is monotone in a (net width shrinks); find its root.
	yieldAt := func(a float64) float64 { return g.NetStress(sigmaMax, a) - mat.Sy }
	var aYield float64
	if yieldAt(low) >= 0 {
		// Even gross stress exceeds Sy: effectively no margin.
		aYield = low
	} else if yieldAt(high) < 0 {
		aYield = math.Inf(1) // never yields within the geometric range
	} else {
		aYield = bisect(yieldAt, low, high, high)
	}

	// Fracture: Kmax is monotone in a for both supplied geometries.
	fractAt := func(a float64) float64 { return Kmax(g, sigmaMax, a) - mat.K1c }
	var aFrac float64
	if fractAt(high) < 0 {
		aFrac = math.Inf(1) // never fractures within the geometric range
	} else {
		aFrac = bisect(fractAt, low, high, high)
	}

	switch {
	case aYield <= aFrac:
		if math.IsInf(aYield, 1) {
			return g.MaxCharacteristic(), FailureGeometry
		}
		return aYield, FailureYield
	default:
		return aFrac, FailureFracture
	}
}

func bisect(f func(float64) float64, lo, hi, fallback float64) float64 {
	// Precondition f(lo) < 0 <= f(hi).
	for i := 0; i < 200; i++ {
		mid := (lo + hi) / 2
		if f(mid) < 0 {
			lo = mid
		} else {
			hi = mid
		}
		if hi-lo < 1e-13*(hi+lo)+1e-30 {
			break
		}
	}
	return (lo + hi) / 2
}
