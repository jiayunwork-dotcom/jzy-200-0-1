package growth

import (
	"fmt"
	"math"
	"testing"

	"crackwatch/internal/domain/geometry"
	"crackwatch/internal/domain/spectrum"
)

func TestAccuracyReport(t *testing.T) {
	cases := []struct {
		name string
		g    geometry.Geometry
		a0   float64
		a1   float64
	}{
		{"edge 5-150mm", mustGeom(geometry.EdgeCrack, 0.3), 5e-3, 0.15},
		{"edge deep 2-270mm", mustGeom(geometry.EdgeCrack, 0.3), 2e-3, 0.27},
		{"center 3-140mm", mustGeom(geometry.CenterCrack, 0.3), 3e-3, 0.14},
	}
	for _, tc := range cases {
		blocks := []spectrum.Block{{StressAmp: 80, StressMax: 120, CyclesPerDay: 1}}
		got := Cycles(tc.g, blocks, 2e-12, 3, tc.a0, tc.a1)
		ref := simpsonGrowth(tc.g, 2e-12, 3, 80, tc.a0, tc.a1, 400000)
		fmt.Printf("%-18s relative error vs 400k-panel Simpson: %.2e\n",
			tc.name, math.Abs(got-ref)/ref)
	}
}
