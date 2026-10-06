// Package model holds the persistent domain entities: versioned material
// parameters, monitored locations with their load spectra, the append-only
// inspection-record event store and derived inspection plans.
package model

import (
	"time"

	"crackwatch/internal/domain/geometry"
	"crackwatch/internal/domain/material"
	"crackwatch/internal/domain/spectrum"
)

// Time layout used in JSON payloads.
const TimeLayout = time.RFC3339

// Material is a material grade with its immutable parameter versions.
type Material struct {
	ID        string            `json:"id"`
	Grade     string            `json:"grade"`
	CreatedAt time.Time         `json:"created_at"`
	Versions  []MaterialVersion `json:"versions,omitempty"`
}

// MaterialVersion is one immutable set of fracture-mechanics parameters.
type MaterialVersion struct {
	ID         string          `json:"id"`
	MaterialID string          `json:"material_id"`
	Version    int             `json:"version"`
	Params     material.Params `json:"params"`
	// EffectiveAt: records inspected on/after this instant use this version
	// when no version is explicitly specified (newest effective one wins).
	EffectiveAt time.Time `json:"effective_at"`
	CreatedAt   time.Time `json:"created_at"`
}

// Location is one monitored structural position.
type Location struct {
	ID             string        `json:"id"`
	Name           string        `json:"name"`
	Geometry       geometry.Kind `json:"geometry"`
	WidthMM        float64       `json:"width_mm"`
	MaterialID     string        `json:"material_id"`
	SafetyFactor   float64       `json:"safety_factor"`
	CommissionedAt time.Time     `json:"commissioned_at"`
	CreatedAt      time.Time     `json:"created_at"`
	UpdatedAt      time.Time     `json:"updated_at"`
}

// WidthM returns the plate width in metres.
func (l Location) WidthM() float64 { return l.WidthMM / 1000 }

// SpectrumVersion is an immutable snapshot of a location's daily load blocks.
type SpectrumVersion struct {
	ID          string           `json:"id"`
	LocationID  string           `json:"location_id"`
	Version     int              `json:"version"`
	Blocks      []spectrum.Block `json:"blocks"`
	EffectiveAt time.Time        `json:"effective_at"`
	CreatedAt   time.Time        `json:"created_at"`
}

// ResultKind distinguishes measured findings from "not detected" records.
type ResultKind string

const (
	ResultFound       ResultKind = "found"
	ResultNotDetected ResultKind = "not_detected"
)

// InspectionEvent is one immutable entry in the append-only record store.
//
// A correction is a new event whose SupersedesID names the event being
// corrected; a deletion is an event with Kind = "void". Backfilled records
// are ordinary events with an earlier InspectedAt: all derivations sort by
// InspectedAt, so insertion order never affects the result.
type InspectionEvent struct {
	ID            string     `json:"id"`
	LocationID    string     `json:"location_id"`
	Seq           int64      `json:"seq"` // per-location append order
	InspectedAt   time.Time  `json:"inspected_at"`
	Method        string     `json:"method"`
	Kind          ResultKind `json:"kind"`
	LengthMM      float64    `json:"length_mm,omitempty"`       // Kind == found
	DetectLimitMM float64    `json:"detect_limit_mm,omitempty"` // Kind == not_detected
	Note          string     `json:"note,omitempty"`
	SupersedesID  string     `json:"supersedes_id,omitempty"`
	Inspector     string     `json:"inspector,omitempty"`
	RecordedAt    time.Time  `json:"recorded_at"`
}

// Finding is a resolved effective observation for a location: an event that
// has not been superseded, converted to a characteristic-size observation.
type Finding struct {
	Event InspectionEvent
	// Characteristic size in metres for a finding; for not_detected this is
	// the detection-limit characteristic size (an upper bound on the truth).
	A float64
	// UpperBound is true for not_detected observations (a <= A).
	UpperBound bool
}

// FailureMode is re-exported for plan payloads.
type FailureMode = material.FailureMode

// Plan is the derived remaining-life assessment and inspection schedule.
type Plan struct {
	LocationID        string    `json:"location_id"`
	AsOf              time.Time `json:"as_of"`
	BasisEventSeq     int64     `json:"basis_event_seq"`
	MaterialVersionID string    `json:"material_version_id"`
	MaterialVersion   int       `json:"material_version"`
	SpectrumVersionID string    `json:"spectrum_version_id"`
	SpectrumVersion   int       `json:"spectrum_version"`

	CurrentLengthMM float64 `json:"current_length_mm"`
	CurrentIsBound  bool    `json:"current_is_bound"` // last effective record was not_detected
	CalibratedC     float64 `json:"calibrated_c"`
	CatalogC        float64 `json:"catalog_c"`
	CWasCalibrated  bool    `json:"c_was_calibrated"`

	CriticalLengthMM float64     `json:"critical_length_mm"`
	FailureMode      FailureMode `json:"failure_mode"`

	RemainingCycles        float64   `json:"remaining_cycles"`
	RemainingDays          float64   `json:"remaining_days"`
	SafetyFactor           float64   `json:"safety_factor"`
	NextInspectionAt       time.Time `json:"next_inspection_at"`
	InspectionIntervalDays float64   `json:"inspection_interval_days"`

	NoFinding bool `json:"no_finding,omitempty"` // no effective finding yet
}

// PlanSnapshot is a stored plan bound to the record and parameter versions it
// was computed from.
type PlanSnapshot struct {
	Plan
	ComputedAt time.Time `json:"computed_at"`
}
