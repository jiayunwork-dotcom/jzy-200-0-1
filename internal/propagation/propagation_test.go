package propagation_test

import (
	"math"
	"testing"

	"crackstation/internal/geometry"
	"crackstation/internal/propagation"
)

// Reference check: beta ≡ 1 (infinite plate), m=3, C=1e-11, Δσ=100 MPa,
// characteristic length a from 1 mm to 10 mm, critical size fixed at 10 mm.
func TestAnalyticReferenceConstantAmplitude(t *testing.T) {
	// Closed form, beta=1: N = 2/(C (Δσ√π)^m) (1/sqrt(a0) - 1/sqrt(ac))
	const C, m, amp, a0, ac = 1e-11, 3.0, 100.0, 0.001, 0.01
	kUnit := amp * math.Sqrt(math.Pi)
	want := 2 / (C * math.Pow(kUnit, m)) * (1/math.Sqrt(a0) - 1/math.Sqrt(ac))
	wantRef := 7.77e5
	if math.Abs(want-wantRef)/wantRef > 0.01 {
		t.Fatalf("analytic value %v far from stated 7.77e5", want)
	}
	t.Logf("analytic N = %.4e (stated 7.77e5)", want)

	// Numerical integrator on the beta=1 kernel.
	got, err := propagation.ConstantAmplitudeInfinitePlate(m, C, amp, a0, ac)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(got-want)/want > 1e-3 {
		t.Fatalf("numerical N=%.4e, analytic %.4e, rel err %.2e", got, want, math.Abs(got-want)/want)
	}
	if math.Abs(got-7.77e5)/7.77e5 > 1e-3 {
		t.Fatalf("numerical N=%.4e not within 0.1%% of 7.77e5", got)
	}
}

func TestDoubleStressOneEighth(t *testing.T) {
	n1, err := propagation.ConstantAmplitudeInfinitePlate(3, 1e-11, 100, 0.001, 0.01)
	if err != nil {
		t.Fatal(err)
	}
	n2, err := propagation.ConstantAmplitudeInfinitePlate(3, 1e-11, 200, 0.001, 0.01)
	if err != nil {
		t.Fatal(err)
	}
	ratio := n2 / n1
	if math.Abs(ratio-0.125)/0.125 > 1e-10 {
		t.Fatalf("N(2Δσ)/N(Δσ)=%.12f, want 1/8", ratio)
	}
}

func TestBetaTrends(t *testing.T) {
	// Centre crack beta rises monotonically with a/W (secant).
	var last float64
	for i := 1; i <= 20; i++ {
		x := float64(i) * 0.04
		b := geometry.Beta(geometry.CenterThrough, x, 1)
		if b <= last {
			t.Fatalf("center beta not increasing at x=%v: %v <= %v", x, b, last)
		}
		last = b
	}
	if math.Abs(geometry.Beta(geometry.CenterThrough, 0, 1)-1) > 1e-12 {
		t.Fatal("center beta(0) must be 1")
	}

	// Edge polynomial: 1.12 at x→0 and increasing over its valid tabulated
	// range from x≈0.03 up to the freeze limit.
	if math.Abs(geometry.Beta(geometry.Edge, 0, 1)-1.12) > 1e-12 {
		t.Fatalf("edge beta(0)=%v, want 1.12", geometry.Beta(geometry.Edge, 0, 1))
	}
	last = 0
	for i := 5; i <= 60; i++ {
		x := float64(i) * 0.01
		b := geometry.Beta(geometry.Edge, x, 1)
		if b <= last {
			t.Fatalf("edge beta not increasing at x=%v: %v <= %v", x, b, last)
		}
		last = b
	}
}
