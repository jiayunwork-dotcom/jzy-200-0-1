// Package geometry defines the monitored-section geometries and their
// stress-intensity-factor geometry correction coefficient beta.
//
// Every geometry uses the canonical expression
//
//	K = sigma * sqrt(pi * a) * beta(a/W)
//
// where a is the crack "characteristic size" (the length multiplied by pi):
//   - center crack in a finite-width plate: a is the HALF crack length,
//     W is the plate width, physical limit a -> W/2.
//   - single edge crack in a finite-width plate: a is the crack length,
//     W is the plate width, physical limit a -> W.
//
// All internal lengths are metres; the API accepts millimetres.
package geometry

import (
	"errors"
	"math"
)

// Kind identifies a supported geometry.
type Kind string

const (
	// CenterCrack: through-thickness central crack in a finite-width plate.
	CenterCrack Kind = "center_crack"
	// EdgeCrack: single through-thickness edge crack in a finite-width plate.
	EdgeCrack Kind = "edge_crack"
)

// Geometry is a plate geometry plus its SIF correction and net-section rules.
type Geometry interface {
	Kind() Kind
	// Width is the plate width W in metres.
	Width() float64
	// Beta is the geometry correction coefficient at characteristic size a (m).
	Beta(a float64) float64
	// NetStress is the nominal net-section tensile stress under gross stress
	// sigma at characteristic size a.
	NetStress(sigma, a float64) float64
	// MaxCharacteristic is the geometric upper bound of a (m):
	// W/2 for center cracks, W for edge cracks.
	MaxCharacteristic() float64
	// ToCharacteristic converts a reported total crack length (m) to a.
	ToCharacteristic(reportedLength float64) float64
	// ToReported converts characteristic size a back to reported total length.
	ToReported(a float64) float64
}

// New builds the geometry for a kind and plate width (metres).
func New(kind Kind, width float64) (Geometry, error) {
	if width <= 0 {
		return nil, errors.New("geometry: plate width must be positive")
	}
	switch kind {
	case CenterCrack:
		return center{w: width}, nil
	case EdgeCrack:
		return edge{w: width}, nil
	default:
		return nil, errors.New("geometry: unsupported geometry kind")
	}
}

// center: finite-width plate with a central through crack of total length 2a.
type center struct{ w float64 }

func (center) Kind() Kind                           { return CenterCrack }
func (c center) Width() float64                     { return c.w }
func (c center) MaxCharacteristic() float64         { return c.w / 2 }
func (c center) ToCharacteristic(l float64) float64 { return l / 2 }
func (c center) ToReported(a float64) float64       { return 2 * a }

// Beta uses the Feddersen secant correction
//
//	beta = sqrt( sec(pi*a/W) ),  0 < a/W < 1/2.
func (c center) Beta(a float64) float64 {
	r := a / c.w
	return math.Sqrt(1 / math.Cos(math.Pi*r))
}

// Net section: W - 2a, so sigma_net = sigma * W/(W-2a).
func (c center) NetStress(sigma, a float64) float64 {
	return sigma * c.w / (c.w - 2*a)
}

// edge: finite-width strip with a single edge crack of length a.
type edge struct{ w float64 }

func (edge) Kind() Kind                           { return EdgeCrack }
func (e edge) Width() float64                     { return e.w }
func (e edge) MaxCharacteristic() float64         { return e.w }
func (e edge) ToCharacteristic(l float64) float64 { return l }
func (e edge) ToReported(a float64) float64       { return a }

// Beta uses the Tada closed-form edge-crack correction (Tada, Paris &
// Irwin, "The Stress Analysis of Cracks Handbook"):
//
//	beta = (0.752 + 2.02 r + 0.37 (1-sin(pi r/2))^3) / cos(pi r/2)
//	     * sqrt( 2/(pi r) * tan(pi r/2) ),  r = a/W.
//
// It agrees with the Isida series to about 0.5 % over 0 <= a/W <= 0.6,
// tends to 1.122 as r -> 0 (single-edge free-surface factor) and rises
// monotonically to infinity as r -> 1.
func (e edge) Beta(a float64) float64 {
	r := a / e.w
	s := math.Sin(math.Pi * r / 2)
	g := 0.752 + 2.02*r + 0.37*math.Pow(1-s, 3)
	return g / math.Cos(math.Pi*r/2) *
		math.Sqrt(2/(math.Pi*r)*math.Tan(math.Pi*r/2))
}

// Net section: W - a, so sigma_net = sigma * W/(W-a).
func (e edge) NetStress(sigma, a float64) float64 {
	return sigma * e.w / (e.w - a)
}
