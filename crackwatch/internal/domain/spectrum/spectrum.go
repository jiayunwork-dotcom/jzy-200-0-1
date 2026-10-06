// Package spectrum defines the block load spectrum and its validation.
package spectrum

import "errors"

// Block is one constant-amplitude block of a location's daily load spectrum.
type Block struct {
	StressAmp    float64 `json:"stress_amp"` // delta sigma [MPa]
	StressMax    float64 `json:"stress_max"` // maximum stress [MPa]
	CyclesPerDay float64 `json:"cycles_per_day"`
}

// Validate checks one block and returns a field-named error.
func (b Block) Validate() error {
	if b.StressAmp < 0 {
		return errors.New("stress_amp: must not be negative")
	}
	if b.StressAmp > 2*b.StressMax {
		// delta sigma > 2 sigma_max implies a tensile minimum stress
		// below -sigma_max, which this service does not model.
		return errors.New("stress_amp: must not exceed 2 * stress_max")
	}
	if b.CyclesPerDay < 0 {
		return errors.New("cycles_per_day: must not be negative")
	}
	return nil
}

// ValidateAll validates a full spectrum.
func ValidateAll(blocks []Block) error {
	active := 0.0
	for _, b := range blocks {
		if err := b.Validate(); err != nil {
			return err
		}
		if b.StressAmp > 0 && b.CyclesPerDay > 0 {
			active += b.CyclesPerDay
		}
	}
	if active == 0 {
		return errors.New("load_spectrum: must contain at least one block with positive stress amplitude and cycles per day")
	}
	return nil
}

// MaxStress returns the largest block maximum stress.
func MaxStress(blocks []Block) float64 {
	m := 0.0
	for _, b := range blocks {
		if b.StressMax > m {
			m = b.StressMax
		}
	}
	return m
}
