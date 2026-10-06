package material

import (
	"math"
	"testing"

	"crackwatch/internal/domain/geometry"
)

func TestCriticalFracture(t *testing.T) {
	g, _ := geometry.New(geometry.EdgeCrack, 0.5)
	// Fracture-toughness-limited: modest K1c, high yield stress so net yield
	// comes much later.
	mat := Params{Grade: "steel", K1c: 330, Sy: 350, M: 3, C: 1e-11}
	ac, mode := CriticalCharacteristic(g, mat, 150)
	if mode != FailureFracture {
		t.Fatalf("mode = %s, want fracture", mode)
	}
	// At ac, Kmax must equal K1c within the bisection tolerance.
	if k := Kmax(g, 150, ac); math.Abs(k-330)/330 > 1e-9 {
		t.Fatalf("Kmax(ac)=%.4f, want 330", k)
	}
}

func TestCriticalYield(t *testing.T) {
	g, _ := geometry.New(geometry.EdgeCrack, 0.5)
	// Very tough, low yield: net-section yield wins.
	mat := Params{Grade: "soft", K1c: 1e5, Sy: 200, M: 3, C: 1e-11}
	ac, mode := CriticalCharacteristic(g, mat, 150)
	if mode != FailureYield {
		t.Fatalf("mode = %s, want net_yield", mode)
	}
	// Net stress at ac equals Sy.
	if ns := g.NetStress(150, ac); math.Abs(ns-200)/200 > 1e-9 {
		t.Fatalf("net stress = %.3f, want 200", ns)
	}
}

func TestCriticalSmallerWithHigherStress(t *testing.T) {
	g, _ := geometry.New(geometry.CenterCrack, 0.6)
	mat := Params{Grade: "steel", K1c: 250, Sy: 300, M: 3, C: 1e-11}
	a1, _ := CriticalCharacteristic(g, mat, 100)
	a2, _ := CriticalCharacteristic(g, mat, 180)
	if !(a2 < a1) {
		t.Fatalf("critical size should shrink with stress: %v vs %v", a2, a1)
	}
}

func TestParamsValidate(t *testing.T) {
	for _, p := range []Params{
		{K1c: 0, Sy: 300, M: 3, C: 1e-11},
		{K1c: 2500, Sy: -1, M: 3, C: 1e-11},
		{K1c: 2500, Sy: 300, M: 0, C: 1e-11},
		{K1c: 2500, Sy: 300, M: 3, C: 0},
	} {
		if err := p.Validate(); err == nil {
			t.Fatalf("expected validation error for %+v", p)
		}
	}
}
