// Package growth integrates the Paris crack-growth law
//
//	da/dN = C * (dK)^m,   dK = dSigma * sqrt(pi*a) * beta(a/W)
//
// over a block (variable-amplitude) load spectrum.
//
// # Block accumulation
//
// The spectrum repeats daily; block i applies n_i cycles of range dSigma_i.
// Propagating the blocks in their given order, the crack advance per day is
//
//	da/dD = C * (sqrt(pi*a)*beta(a))^m * S,   S = sum_i n_i * dSigma_i^m
//
// (the per-block advances add, so the blocks are accumulated one by one; the
// crack length change within one day is negligible, hence treating their
// rates as simultaneous at crack length a is exact to far beyond the target
// accuracy).  Propagation cycles are N = D * sum_i n_i.
//
// # Step control
//
// Integration proceeds in crack-length increments. Within a step beta is
// frozen at the step midpoint and the remaining power law is closed form:
//
//	D = [ a^(1-m/2) ]_{a0}^{a1} / ((1-m/2) * C * S * pi^(m/2) * beta^m)
//
// (log form for m = 2).  With beta constant this is exact for any step size,
// which is why the constant-beta acceptance example has essentially zero
// discretisation error.  Each step is evaluated as one interval (coarse) and
// as two half intervals (fine); their relative difference estimates the
// frozen-beta error.  The step is accepted below a tight local tolerance,
// otherwise halved; accepted steps grow back.  The fine (order-accurate)
// value is accumulated, Richardson-style.
package growth

import (
	"math"

	"crackwatch/internal/domain/geometry"
	"crackwatch/internal/domain/spectrum"
)

// localTol is the maximum accepted relative difference of the fine/coarse
// pair per step.  Global accumulated error is of the same order (~1e-9),
// far inside the required 0.1 %.
const localTol = 1e-9

// Intensity returns S = sum_i n_i * dSigma_i^m over the active blocks and the
// total active cycles per day.  Blocks with zero amplitude or zero cycles
// are inert.
func Intensity(blocks []spectrum.Block, m float64) (s, cyclesPerDay float64) {
	for _, b := range blocks {
		if b.StressAmp > 0 && b.CyclesPerDay > 0 {
			s += b.CyclesPerDay * math.Pow(b.StressAmp, m)
			cyclesPerDay += b.CyclesPerDay
		}
	}
	return s, cyclesPerDay
}

// frozenStep integrates time in DAYS to grow from a0 to a1 with beta frozen.
func frozenStep(a0, a1, c, m, s, beta float64) float64 {
	k := c * s * math.Pow(math.Pi*beta*beta, m/2)
	if m == 2 {
		return math.Log(a1/a0) / k
	}
	q := 1 - m/2
	return (math.Pow(a1, q) - math.Pow(a0, q)) / (q * k)
}

// minStepFraction bounds how far the adaptive step may shrink: below this
// fraction of the total span the fine estimate is accepted as-is. This
// guards against endless halving where beta diverges (a -> geometric limit).
const minStepFraction = 1e-13

// integrateDays accumulates days of propagation from a0 to aStop.
func integrateDays(beta geometry.Geometry, c, m, s, a0, aStop float64) float64 {
	if s <= 0 || aStop <= a0 {
		return 0
	}
	a := a0
	days := 0.0
	span := aStop - a0
	h := span / 64 // initial crack-length step
	for a < aStop {
		if a+h > aStop {
			h = aStop - a
		}
		coarse := frozenStep(a, a+h, c, m, s, beta.Beta(a+h/2))
		mid := a + h/2
		fine := frozenStep(a, mid, c, m, s, beta.Beta(a+h/4)) +
			frozenStep(mid, a+h, c, m, s, beta.Beta(a+3*h/4))

		err := math.Abs(fine-coarse) / math.Max(math.Abs(fine), 1e-300)
		if err <= localTol || h <= span*minStepFraction {
			a += h
			days += fine
			if rem := aStop - a; h < rem/8 {
				h = math.Min(h*1.9, rem/8)
			}
			continue
		}
		h /= 2
	}
	return days
}

// Cycles returns the number of propagation cycles needed to grow from a0 to
// aStop under the full repeated daily spectrum.
func Cycles(g geometry.Geometry, blocks []spectrum.Block, c, m, a0, aStop float64) float64 {
	if aStop <= a0 {
		return 0
	}
	s, perDay := Intensity(blocks, m)
	days := integrateDays(g, c, m, s, a0, aStop)
	return days * perDay
}

// GrowthFactor returns T(a0,a1), the propagation TIME PER UNIT C (in days)
// accumulated over the spectrum blocks for C = 1:
//
//	T = elapsed_days / C,   so elapsed_days = C * T.
//
// It is used by the site-specific coefficient calibration: between two
// findings separated by dt days, the observed value is T = dt/C.
func GrowthFactor(g geometry.Geometry, blocks []spectrum.Block, m, a0, a1 float64) float64 {
	if a1 <= a0 {
		return 0
	}
	s, _ := Intensity(blocks, m)
	return integrateDays(g, 1, m, s, a0, a1)
}
