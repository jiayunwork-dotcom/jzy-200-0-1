package propagation_test

import (
	"testing"

	"crackstation/internal/geometry"
	"crackstation/internal/propagation"
	"crackstation/internal/spectrum"
)

func BenchmarkDaysToCriticalEdge(b *testing.B) {
	p := propagation.Params{
		Geo:      geometry.Edge,
		Width:    0.1,
		Material: propagation.Material{ParisM: 3, ParisC: 1e-11, FractureKIC: 60, YieldStrength: 345},
		Blocks:   []spectrum.Block{{StressAmp: 80, MaxStress: 120, CyclesPerDay: 300}},
	}
	aC := p.TargetCritical()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := propagation.DaysTo(p, 0.002, aC); err != nil {
			b.Fatal(err)
		}
	}
}
