// Package recordlog is the core domain service: it owns inspection-record
// submission/correction with append-only versioning, replays the plan as of
// any historical date, fits the location Paris coefficient from the record
// history, and lists the locations whose inspection moves earlier when a
// material parameter version changes.
//
// Determinism contract. Every plan is a pure function of
//
//   - the active records that existed at the replay instant (ordered by
//     inspect_date, then created_at, then id),
//   - the spectrum revision effective at that instant (in replay: per
//     interval; for the current plan only the current revision is used past
//     the base date),
//   - the material version effective at that instant.
//
// Because the fit (calibration package) is order-independent and the replay
// sorts records deterministically, backfilling an older record produces the
// exact same result that entering it in chronological order would have.
package recordlog

import (
	"time"

	"crackstation/internal/geometry"
	"crackstation/internal/spectrum"
	"crackstation/internal/store"
)

// FieldError points at the rejected field.
type FieldError struct {
	Field  string
	Reason string
}

func (e FieldError) Error() string { return "invalid " + e.Field + ": " + e.Reason }

// DateOnly truncates to UTC midnight; all date inputs are handled as dates.
func DateOnly(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

// ValidateRecord enforces the acceptance rules from the specification.
//   - found: crack length > 0 and < width (physical length)
//   - not found: detection limit present and > 0 and <= width
//   - inspection date >= commission date
func ValidateRecord(geo geometry.Type, width float64, commissioned, inspect time.Time,
	found bool, crackLength, detectionLimit *float64) error {
	inspect = DateOnly(inspect)
	if inspect.Before(DateOnly(commissioned)) {
		return FieldError{"inspect_date", "earlier than the location's commission date"}
	}
	if found {
		if crackLength == nil {
			return FieldError{"crack_length_m", "required for a found-crack record"}
		}
		if *crackLength <= 0 {
			return FieldError{"crack_length_m", "must be positive"}
		}
		if *crackLength >= width {
			return FieldError{"crack_length_m", "must be smaller than plate width"}
		}
	} else {
		if detectionLimit == nil {
			return FieldError{"detection_limit_m", "required for a not-found record"}
		}
		if *detectionLimit <= 0 {
			return FieldError{"detection_limit_m", "must be positive"}
		}
		if *detectionLimit > width {
			return FieldError{"detection_limit_m", "must not exceed plate width"}
		}
	}
	return nil
}

// ValidateBlocks enforces per-block rules:
//   - stress amplitude >= 0 and <= 2 * max stress (max stress must be >= 0)
//   - cycles per day >= 0
func ValidateBlocks(blocks []spectrum.Block) error {
	for i, b := range blocks {
		if b.StressAmp < 0 {
			return FieldError{Field: "blocks[" + itoa(i) + "].stress_amp_mpa", Reason: "negative stress amplitude"}
		}
		if b.MaxStress < 0 {
			return FieldError{Field: "blocks[" + itoa(i) + "].max_stress_mpa", Reason: "negative maximum stress"}
		}
		if b.StressAmp > 2*b.MaxStress {
			return FieldError{Field: "blocks[" + itoa(i) + "].stress_amp_mpa", Reason: "stress amplitude greater than twice the maximum stress"}
		}
		if b.CyclesPerDay < 0 {
			return FieldError{Field: "blocks[" + itoa(i) + "].cycles_per_day", Reason: "negative cycles per day"}
		}
	}
	return nil
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}

// ValidateMaterialVersion checks fracture toughness (and other material
// constants) are positive.
func ValidateMaterialVersion(v store.MaterialVersion) error {
	if v.FractureKIC <= 0 {
		return FieldError{"fracture_kic", "must be positive"}
	}
	if v.ParisM <= 0 {
		return FieldError{"paris_m", "must be positive"}
	}
	if v.ParisC <= 0 {
		return FieldError{"paris_c", "must be positive"}
	}
	if v.YieldStrength <= 0 {
		return FieldError{"yield_strength", "must be positive"}
	}
	return nil
}

// ValidateLocation checks geometry and width.
func ValidateLocation(geo geometry.Type, width float64) error {
	if width <= 0 {
		return FieldError{"width_m", "must be positive"}
	}
	return nil
}
