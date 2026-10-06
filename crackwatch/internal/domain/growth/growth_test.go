package growth

import (
	"math"
	"testing"
	"time"

	"crackwatch/internal/domain/geometry"
	"crackwatch/internal/domain/spectrum"
)

// flatGeom is beta == 1 with an arbitrary width; used to reproduce the
// handbook reference example exactly.
type flatGeom struct{}

func (flatGeom) Kind() geometry.Kind                { return geometry.Kind("flat") }
func (flatGeom) Width() float64                     { return 1 }
func (flatGeom) Beta(float64) float64               { return 1 }
func (flatGeom) NetStress(s, _ float64) float64     { return s }
func (flatGeom) MaxCharacteristic() float64         { return 1 }
func (flatGeom) ToCharacteristic(l float64) float64 { return l }
func (flatGeom) ToReported(a float64) float64       { return a }

const referenceCycles = 7.77e5

func TestConstantAmplitudeReference(t *testing.T) {
	const mm = 1e-3
	blocks := []spectrum.Block{{StressAmp: 100, StressMax: 100, CyclesPerDay: 1}}
	start := time.Now()
	n := Cycles(flatGeom{}, blocks, 1e-11, 3, 1*mm, 10*mm)
	dt := time.Since(start)

	// Closed form for beta == 1, m == 3:
	// N = 2*(a0^-1/2 - a1^-1/2) / (C*(dSigma*sqrt(pi))^3)
	analytic := (2 * (math.Pow(mm, -0.5) - math.Pow(10*mm, -0.5))) /
		(1e-11 * math.Pow(100*math.Sqrt(math.Pi), 3))

	if rel := math.Abs(n-analytic) / analytic; rel > 1e-3 {
		t.Fatalf("N = %.6e, analytic %.6e, relative error %.3e > 0.1%%", n, analytic, rel)
	}
	// Stated handbook figure ~7.77e5.
	if math.Abs(n-referenceCycles)/referenceCycles > 1e-3 {
		t.Fatalf("N = %.4e not within 0.1%% of %.2e", n, float64(referenceCycles))
	}
	t.Logf("N=%.6e relative error vs analytic %.2e, integration time %v", n,
		math.Abs(n-analytic)/analytic, dt)
}

func TestDoublingStressOneEighth(t *testing.T) {
	const mm = 1e-3
	n1 := Cycles(flatGeom{},
		[]spectrum.Block{{StressAmp: 100, StressMax: 100, CyclesPerDay: 1}},
		1e-11, 3, 1*mm, 10*mm)
	n2 := Cycles(flatGeom{},
		[]spectrum.Block{{StressAmp: 200, StressMax: 200, CyclesPerDay: 1}},
		1e-11, 3, 1*mm, 10*mm)
	if ratio := n1 / n2; math.Abs(ratio-8) > 8e-3 {
		t.Fatalf("N(100)/N(200) = %.5f, want 8 (N2 should be one eighth)", ratio)
	}
}

// TestVariableAmplitudeAccumulation checks two equal-amplitude blocks give the
// same result as one block with twice the cycles, and that an added
// high-amplitude block accelerates growth in the expected m-power proportion.
func TestVariableAmplitudeAccumulation(t *testing.T) {
	const mm = 1e-3
	one := []spectrum.Block{{StressAmp: 100, StressMax: 100, CyclesPerDay: 1000}}
	two := []spectrum.Block{
		{StressAmp: 100, StressMax: 100, CyclesPerDay: 500},
		{StressAmp: 100, StressMax: 100, CyclesPerDay: 500},
	}
	n1 := Cycles(flatGeom{}, one, 1e-11, 3, 1*mm, 10*mm)
	n2 := Cycles(flatGeom{}, two, 1e-11, 3, 1*mm, 10*mm)
	if math.Abs(n1-n2)/n1 > 1e-12 {
		t.Fatalf("splitting blocks changed cycles: %.6e vs %.6e", n1, n2)
	}

	// Spectrum with half the cycles at 100 MPa and half at 200 MPa:
	// S doubles per day... per-day intensity 500*100^3 + 500*200^3 = 4.5e9,
	// vs 1000*100^3 = 1e9; total active cycles/day = 1000 in both, so
	// propagation cycles must be 1/4.5.
	mixed := []spectrum.Block{
		{StressAmp: 100, StressMax: 100, CyclesPerDay: 500},
		{StressAmp: 200, StressMax: 200, CyclesPerDay: 500},
	}
	nMix := Cycles(flatGeom{}, mixed, 1e-11, 3, 1*mm, 10*mm)
	if ratio := n1 / nMix; math.Abs(ratio-4.5) > 1e-9 {
		t.Fatalf("mixed-spectrum cycles ratio = %.6f, want 4.5", ratio)
	}
}

// simpsonGrowth integrates N = integral da / [C*(dS*sqrt(pi*a)*beta(a))^m]
// with a very fine Simpson grid, as an independent high-accuracy reference.
func simpsonGrowth(g geometry.Geometry, c, m, dSigma, a0, a1 float64, panels int) float64 {
	f := func(a float64) float64 {
		dk := dSigma * math.Sqrt(math.Pi*a) * g.Beta(a)
		return 1 / (c * math.Pow(dk, m))
	}
	h := (a1 - a0) / float64(panels)
	sum := f(a0) + f(a1)
	for i := 1; i < panels; i++ {
		x := a0 + float64(i)*h
		if i%2 == 0 {
			sum += 2 * f(x)
		} else {
			sum += 4 * f(x)
		}
	}
	return sum * h / 3
}

// TestGeometryIntegrationAccuracy verifies the adaptive integrator against an
// independent fine Simpson integration for both real geometries, well inside
// the 0.1 % requirement.
func TestGeometryIntegrationAccuracy(t *testing.T) {
	cases := []struct {
		name string
		g    geometry.Geometry
		a0   float64
		a1   float64
	}{
		{"edge", mustGeom(geometry.EdgeCrack, 0.3), 5e-3, 0.15},
		{"edge_deep", mustGeom(geometry.EdgeCrack, 0.3), 2e-3, 0.27},
		{"center", mustGeom(geometry.CenterCrack, 0.3), 3e-3, 0.14}, // a up to 0.47W
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			blocks := []spectrum.Block{{StressAmp: 80, StressMax: 120, CyclesPerDay: 1}}
			got := Cycles(tc.g, blocks, 2e-12, 3, tc.a0, tc.a1)
			ref := simpsonGrowth(tc.g, 2e-12, 3, 80, tc.a0, tc.a1, 400000)
			if rel := math.Abs(got-ref) / ref; rel > 1e-3 {
				t.Fatalf("%s: got %.6e ref %.6e rel %.3e", tc.name, got, ref, rel)
			}
		})
	}
}

func mustGeom(k geometry.Kind, w float64) geometry.Geometry {
	g, err := geometry.New(k, w)
	if err != nil {
		panic(err)
	}
	return g
}
