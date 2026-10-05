// Package spectrum models the load spectrum of a monitored location: an
// ordered collection of constant-amplitude load blocks that repeat every day.
package spectrum

import (
	"math"
)

// Block is one constant-amplitude load block.
//
// StressAmp is the cycle stress range half-width convention used by the
// Paris law here: stress amplitude Δσ = σmax - σmin is taken as the
// full range driving delta-K (see docs). MaxStress is the largest stress
// in the cycle (MPa). CyclesPerDay is the number of block cycles each day.
type Block struct {
	StressAmp    float64 `json:"stress_amp_mpa"`
	MaxStress    float64 `json:"max_stress_mpa"`
	CyclesPerDay float64 `json:"cycles_per_day"`
}

// DailyGrowthRate returns da per day at characteristic crack length a
// given the Paris integrand growth per cycle contributed by each block:
//
//	g(a) = Σ_j cyclesPerDay_j · perCycle_j(a)
//
// growthPerCycle has signature (block index, a) and already incorporates
// delta-K_j(a) = StressAmp_j sqrt(pi a) beta.
func DailyGrowthRate(blocks []Block, a float64, perCycle func(int, float64) float64) float64 {
	g := 0.0
	for j := range blocks {
		g += blocks[j].CyclesPerDay * perCycle(j, a)
	}
	return g
}

// MaxStress returns the largest maximum stress across blocks. It is the
// stress used when evaluating Kmax against fracture toughness (the block
// with the highest σmax reaches KIC first).
func MaxStress(blocks []Block) float64 {
	m := math.Inf(-1)
	for _, b := range blocks {
		if b.MaxStress > m {
			m = b.MaxStress
		}
	}
	return m
}

// TotalCyclesPerDay returns the sum over blocks (may be 0 for an idle site).
func TotalCyclesPerDay(blocks []Block) float64 {
	n := 0.0
	for _, b := range blocks {
		n += b.CyclesPerDay
	}
	return n
}
