// Package geometry implements the two supported crack configurations of a
// monitored location:
//
//   - center_through_crack: through crack of total (physical) length 2a in a
//     finite-width plate; a is the half-length.
//   - edge_crack: single edge crack of physical length a in a finite-width
//     plate.
//
// The stress intensity factor is written uniformly as
//
//	K = sigma * sqrt(pi*a) * beta(a/W)
//
// where a is the "characteristic length" — exactly the length multiplied by pi
// inside the square root. Physical measured crack length L maps to a by
// PhysicalToCharacteristic.
package geometry

import (
	"errors"
	"math"
)

// Type identifies the supported geometry.
type Type string

const (
	// CenterThrough is a centre through-thickness crack, physical length 2a.
	CenterThrough Type = "center_through_crack"
	// Edge is a single edge crack, physical length a.
	Edge Type = "edge_crack"
)

// ParseType parses a geometry type string and rejects unknown values.
func ParseType(s string) (Type, error) {
	switch Type(s) {
	case CenterThrough:
		return CenterThrough, nil
	case Edge:
		return Edge, nil
	default:
		return "", errors.New("unknown geometry type: " + s)
	}
}

// EdgeBetaLimit is the a/W range over which the standard edge-crack
// polynomial (Tada/Anderson, 0.4% accuracy) is tabulated. Above it the
// polynomial is frozen at this ratio; fracture/net-yield roots in that
// range are flagged by the propagation package.
const EdgeBetaLimit = 0.65

// Beta returns the finite-width geometry correction factor beta(a/W).
//
// Center through crack (Isida/Feddersen secant form):
//
//	beta = sqrt(sec(pi*a/W / 2))
//
// Edge crack (Tada polynomial, valid a/W <= 0.65, frozen above):
//
//	beta = 1.12 - 0.231 x + 10.55 x^2 - 21.72 x^3 + 30.39 x^4
//
// x = a/W.
func Beta(t Type, a, width float64) float64 {
	if width <= 0 {
		panic("geometry: width must be positive")
	}
	x := a / width
	switch t {
	case CenterThrough:
		if x < 0 {
			panic("geometry: negative crack length")
		}
		if x >= 1 {
			// sec(pi/2) -> +Inf; clamp x so callers doing interval
			// arithmetic never get NaN.
			x = math.Nextafter(1, 0)
		}
		return math.Sqrt(1 / math.Cos(math.Pi*x/2))
	case Edge:
		if x < 0 {
			panic("geometry: negative crack length")
		}
		if x > EdgeBetaLimit {
			x = EdgeBetaLimit
		}
		return 1.12 - 0.231*x + 10.55*x*x - 21.72*x*x*x + 30.39*x*x*x*x
	default:
		panic("geometry: unknown geometry type")
	}
}

// StressIntensity returns K in MPa·sqrt(m) for stress in MPa and a, width in m.
func StressIntensity(t Type, stress, a, width float64) float64 {
	return stress * math.Sqrt(math.Pi*a) * Beta(t, a, width)
}

// PhysicalToCharacteristic maps the measured physical crack length L (m) to
// the characteristic length a used in K = sigma sqrt(pi a) beta:
//
//   - edge crack:        a = L
//   - centre crack:      a = L/2
func PhysicalToCharacteristic(t Type, physical float64) float64 {
	switch t {
	case CenterThrough:
		return physical / 2
	case Edge:
		return physical
	default:
		panic("geometry: unknown geometry type")
	}
}

// CharacteristicToPhysical is the inverse of PhysicalToCharacteristic.
func CharacteristicToPhysical(t Type, a float64) float64 {
	switch t {
	case CenterThrough:
		return 2 * a
	case Edge:
		return a
	default:
		panic("geometry: unknown geometry type")
	}
}

// RemainingLigament returns the uncracked net-section length (m):
//
//   - centre crack: W - 2a
//   - edge crack:   W - a
func RemainingLigament(t Type, a, width float64) float64 {
	switch t {
	case CenterThrough:
		return width - 2*a
	case Edge:
		return width - a
	default:
		panic("geometry: unknown geometry type")
	}
}

// MaxCharacteristic returns the largest physically admissible characteristic
// length (net section fully consumed).
func MaxCharacteristic(t Type, width float64) float64 {
	switch t {
	case CenterThrough:
		return width / 2
	case Edge:
		return width
	default:
		panic("geometry: unknown geometry type")
	}
}

// NetSectionStress returns the nominal tensile stress on the net (ligament)
// section, sigma_net = sigma * W / (W - physical crack length).
func NetSectionStress(t Type, sigma, a, width float64) float64 {
	lig := RemainingLigament(t, a, width)
	if lig <= 0 {
		return math.Inf(1)
	}
	return sigma * width / lig
}
