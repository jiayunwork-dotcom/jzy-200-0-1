package calibration

import (
	"math"
	"testing"
	"time"

	"crackwatch/internal/domain/geometry"
	"crackwatch/internal/domain/growth"
	"crackwatch/internal/domain/spectrum"
)

func testSetup() (geometry.Geometry, []spectrum.Block, float64) {
	g, _ := geometry.New(geometry.EdgeCrack, 0.3)
	blocks := []spectrum.Block{{StressAmp: 100, StressMax: 150, CyclesPerDay: 500}}
	return g, blocks, 3
}

// growA is the forward model: characteristic size reached after days of
// propagation from a0 with the given true coefficient.
func growA(g geometry.Geometry, blocks []spectrum.Block, m, a0, days, cTrue float64) float64 {
	target := days * cTrue // elapsed days = G/C -> G(a0,a1) = days*C
	// Bound the search well inside the geometric limit where beta stays
	// finite; the forward cases used here stay far below it.
	hi := a0 * 2
	limit := g.MaxCharacteristic() * 0.95
	for growth.GrowthFactor(g, blocks, m, a0, hi) < target {
		hi *= 2
		if hi >= limit {
			if growth.GrowthFactor(g, blocks, m, a0, limit) < target {
				panic("forward case reaches the geometric bound")
			}
			hi = limit
			break
		}
	}
	lo := a0
	for i := 0; i < 100; i++ {
		mid := (lo + hi) / 2
		if growth.GrowthFactor(g, blocks, m, a0, mid) < target {
			lo = mid
		} else {
			hi = mid
		}
	}
	return (lo + hi) / 2
}

func TestRecoversSyntheticCoefficient(t *testing.T) {
	g, blocks, m := testSetup()
	cCatalog, cTrue := 1e-11, 1.5e-11

	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a0 := 0.005
	a50 := growA(g, blocks, m, a0, 50, cTrue)
	a120 := growA(g, blocks, m, a0, 120, cTrue)

	obs := []Observation{
		{At: t0, A: a0},
		{At: t0.AddDate(0, 0, 50), A: a50},
		{At: t0.AddDate(0, 0, 120), A: a120},
	}
	res := Estimate(g, blocks, m, cCatalog, obs)
	if !res.Calibrated {
		t.Fatal("expected calibration from 3 positive findings")
	}
	if rel := math.Abs(res.C-cTrue) / cTrue; rel > 1e-9 {
		t.Fatalf("calibrated C = %.6e, true %.6e, rel %.2e", res.C, cTrue, rel)
	}
}

func TestCatalogCWithOneFinding(t *testing.T) {
	g, blocks, m := testSetup()
	res := Estimate(g, blocks, m, 1e-11, []Observation{{At: time.Now(), A: 0.005}})
	if res.Calibrated || res.C != 1e-11 {
		t.Fatalf("single finding must keep catalog C, got %+v", res)
	}
}

func TestNotDetectedLooseConstraintDoesNotBind(t *testing.T) {
	g, blocks, m := testSetup()
	cTrue := 1.5e-11
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a0 := 0.005
	a120 := growA(g, blocks, m, a0, 120, cTrue)

	// NDF at day 60 with a generous limit well above the true crack size.
	a60 := growA(g, blocks, m, a0, 60, cTrue)
	obs := []Observation{
		{At: t0, A: a0},
		{At: t0.AddDate(0, 0, 60), A: a60 * 3, UpperBound: true},
		{At: t0.AddDate(0, 0, 120), A: a120},
	}
	res := Estimate(g, blocks, m, 1e-11, obs)
	if res.ClampedByNDF {
		t.Fatal("a loose NDF limit must not clamp the measured estimate")
	}
	if rel := math.Abs(res.C-cTrue) / cTrue; rel > 1e-9 {
		t.Fatalf("C = %.3e vs true %.3e", res.C, cTrue)
	}
}

func TestNotDetectedTightConstraintClampsDownward(t *testing.T) {
	g, blocks, m := testSetup()
	cTrue := 1.5e-11
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a0 := 0.005
	a120 := growA(g, blocks, m, a0, 120, cTrue)
	a60 := growA(g, blocks, m, a0, 60, cTrue)

	// NDF limit just below the true size at day 60: the censored observation
	// forces C down (slower growth, later inspections — documented direction).
	obs := []Observation{
		{At: t0, A: a0},
		{At: t0.AddDate(0, 0, 60), A: a60 * 0.8, UpperBound: true},
		{At: t0.AddDate(0, 0, 120), A: a120},
	}
	res := Estimate(g, blocks, m, 1e-11, obs)
	if !res.ClampedByNDF {
		t.Fatal("tight NDF limit should clamp C")
	}
	if res.C >= cTrue {
		t.Fatalf("clamped C %.3e must be below true C %.3e", res.C, cTrue)
	}
}

func TestNotDetectedBelowPriorFindingFlaggedInconsistent(t *testing.T) {
	g, blocks, m := testSetup()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	obs := []Observation{
		{At: t0, A: 0.01}, // 10 mm measured
		{At: t0.AddDate(0, 0, 5), A: 0.003, UpperBound: true}, // NDF limit 3 mm
	}
	res := Estimate(g, blocks, m, 1e-11, obs)
	if !res.Inconsistent {
		t.Fatal("NDF limit below an earlier measured crack is inconsistent data")
	}
}
