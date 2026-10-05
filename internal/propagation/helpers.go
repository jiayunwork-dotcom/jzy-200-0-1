package propagation

import (
	"math"

	"crackstation/internal/spectrum"
)

// ConstantAmplitudeInfinitePlate evaluates the reference check case:
// beta ≡ 1, one constant-amplitude block with one cycle per day, Paris
// da/dN = C(Δσ√(πa))^m. The returned value is in cycles (== days here).
// Every adaptive cell is the closed-form power-law antiderivative with beta
// frozen at 1, so the result is exact up to machine precision — this both
// reproduces the 7.77e5 check and serves as the error-free baseline against
// which finite-width refinements are measured.
func ConstantAmplitudeInfinitePlate(m, c, stressAmp, a0, ac float64) (float64, error) {
	p := Params{
		Geo:               "", // unused when BetaOne
		Width:             1,
		Material:          Material{ParisM: m, ParisC: c},
		Blocks:            []spectrum.Block{{StressAmp: stressAmp, MaxStress: stressAmp, CyclesPerDay: 1}},
		OverrideCriticalA: ac,
		BetaOne:           true,
	}
	return DaysTo(p, a0, ac)
}

// SpectrumWeight returns W = π^(m/2) · Σ_j cyclesPerDay_j · Δσ_j^m, the
// stress/count part of da/day = C·W·a^(m/2)·β^m.
func SpectrumWeight(m float64, blocks []spectrum.Block) float64 {
	w := 0.0
	for _, b := range blocks {
		w += b.CyclesPerDay * math.Pow(b.StressAmp, m)
	}
	return math.Pow(math.Pi, m/2) * w
}
