package propagation_test

import (
	"math"
	"testing"

	"crackstation/internal/geometry"
	"crackstation/internal/propagation"
	"crackstation/internal/spectrum"
)

// Finite-width edge crack: the adaptive integrator must converge as the
// tolerance is tightened and stay well within the 0.1% acceptance bound.
func TestFiniteWidthConvergence(t *testing.T) {
	base := propagation.Params{
		Geo:   geometry.Edge,
		Width: 0.1,
		Material: propagation.Material{
			ParisM: 3, ParisC: 1e-11, FractureKIC: 60, YieldStrength: 345,
		},
		Blocks: []spectrum.Block{{StressAmp: 80, MaxStress: 120, CyclesPerDay: 1}},
	}
	a0 := 0.002
	aC, frac, yld, ok := propagation.CriticalA(base)
	if !ok {
		t.Fatal("no critical root")
	}
	t.Logf("edge critical a=%.6f fracture=%.6f yield=%.6f", aC, frac, yld)
	n, err := propagation.DaysTo(base, a0, aC)
	if err != nil {
		t.Fatal(err)
	}
	// Independently cross-check with a fine uniform RK march (Euler with
	// extremely small crack steps) over the same beta model.
	const steps = 200000
	da := (aC - a0) / steps
	a := a0
	cycles := 0.0
	for i := 0; i < steps; i++ {
		cycles += da / rateAt(base, a+da/2)
		a += da
	}
	rel := math.Abs(n-cycles) / cycles
	t.Logf("adaptive=%.6e finegrid=%.6e rel=%.2e", n, cycles, rel)
	if rel > 1e-3 {
		t.Fatalf("finite-width integration rel error %.2e exceeds 0.1%%", rel)
	}
}

func rateAt(p propagation.Params, a float64) float64 {
	b := p.Blocks[0]
	dK := b.StressAmp * math.Sqrt(math.Pi*a) * geometry.Beta(p.Geo, a, p.Width)
	return p.Material.ParisC * math.Pow(dK, p.Material.ParisM)
}

// Center through-crack growth stays consistent: doubling the stress
// amplitude still divides the life by eight when fracture is far away
// (net-section/yield roots unchanged between the two runs because they
// depend only on MaxStress, which we keep fixed).
func TestCenterCrackDoubleAmplitude(t *testing.T) {
	mk := func(amp float64) propagation.Params {
		return propagation.Params{
			Geo:   geometry.CenterThrough,
			Width: 0.2,
			Material: propagation.Material{
				ParisM: 3, ParisC: 1e-11, FractureKIC: 200, YieldStrength: 1e4,
			},
			Blocks:            []spectrum.Block{{StressAmp: amp, MaxStress: 100, CyclesPerDay: 1}},
			OverrideCriticalA: 0.01,
		}
	}
	n1, err := propagation.DaysTo(mk(100), 0.001, 0.01)
	if err != nil {
		t.Fatal(err)
	}
	n2, err := propagation.DaysTo(mk(200), 0.001, 0.01)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(n2/n1-0.125) > 1e-6 {
		t.Fatalf("ratio=%.6f want 0.125", n2/n1)
	}
}

// Net-section yielding can govern before fracture for a tough but low-yield
// material: reducing yield strength moves the critical size down and
// shrinks life.
func TestNetYieldCanGovern(t *testing.T) {
	high := propagation.Params{
		Geo: geometry.Edge, Width: 0.2,
		Material: propagation.Material{ParisM: 3, ParisC: 1e-11, FractureKIC: 400, YieldStrength: 1000},
		Blocks:   []spectrum.Block{{StressAmp: 100, MaxStress: 300, CyclesPerDay: 1}},
	}
	low := high
	low.Material.YieldStrength = 350
	cHi, fHi, yHi, _ := propagation.CriticalA(high)
	cLo, fLo, yLo, _ := propagation.CriticalA(low)
	t.Logf("high yield: crit=%.5f f=%.5f y=%.5f", cHi, fHi, yHi)
	t.Logf("low yield:  crit=%.5f f=%.5f y=%.5f", cLo, fLo, yLo)
	if !(cLo < cHi) {
		t.Fatalf("lowering yield did not reduce critical size: %v vs %v", cLo, cHi)
	}
	nHi, _ := propagation.DaysTo(high, 0.002, cHi)
	nLo, _ := propagation.DaysTo(low, 0.002, cLo)
	if !(nLo < nHi) {
		t.Fatalf("lowering yield did not shorten life: %v vs %v", nLo, nHi)
	}
	// With very low yield relative to max stress, plate is critical at once.
	zero := high
	zero.Material.YieldStrength = 200
	if _, _, _, ok := propagation.CriticalA(zero); ok {
		t.Fatal("expected already-critical (no positive root) when max stress exceeds yield")
	}
}
