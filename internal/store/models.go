package store

import (
	"time"

	"crackstation/internal/geometry"
	"crackstation/internal/spectrum"
)

// Location is a monitored crack site on a crane.
type Location struct {
	ID               int64         `json:"id"`
	Name             string        `json:"name"`
	Crane            string        `json:"crane"`
	Geometry         geometry.Type `json:"geometry"`
	WidthM           float64       `json:"width_m"`
	MaterialID       int64         `json:"material_id"`
	CommissionedDate time.Time     `json:"commissioned_date"` // date only
	SafetyFactor     float64       `json:"safety_factor"`
	CreatedAt        time.Time     `json:"created_at"`
}

// Material is a grade with versioned parameters.
type Material struct {
	ID        int64     `json:"id"`
	Grade     string    `json:"grade"`
	CreatedAt time.Time `json:"created_at"`
}

// MaterialVersion is one immutable parameter version of a grade.
type MaterialVersion struct {
	ID            int64     `json:"id"`
	MaterialID    int64     `json:"material_id"`
	Version       int       `json:"version"`
	ParisM        float64   `json:"paris_m"`
	ParisC        float64   `json:"paris_c"`
	FractureKIC   float64   `json:"fracture_kic_mpa_sqrt_m"`
	YieldStrength float64   `json:"yield_strength_mpa"`
	CreatedBy     string    `json:"created_by"`
	Note          string    `json:"note"`
	CreatedAt     time.Time `json:"created_at"`
}

// SpectrumRevision is one immutable revision of a location's load spectrum.
type SpectrumRevision struct {
	ID         int64            `json:"id"`
	LocationID int64            `json:"location_id"`
	Blocks     []spectrum.Block `json:"blocks"`
	CreatedBy  string           `json:"created_by"`
	CreatedAt  time.Time        `json:"created_at"`
}

// Record is one inspection record event. LogicalID stays stable across
// corrections; a correction appends a new row with SupersedesID set and
// flips the old row's SupersededBy.
type Record struct {
	ID              int64     `json:"id"`
	LogicalID       int64     `json:"logical_id"`
	LocationID      int64     `json:"location_id"`
	InspectDate     time.Time `json:"inspect_date"` // date only
	Method          string    `json:"method"`
	Found           bool      `json:"found"`
	CrackLengthM    *float64  `json:"crack_length_m,omitempty"`
	DetectionLimitM *float64  `json:"detection_limit_m,omitempty"`
	Inspector       string    `json:"inspector"`
	SupersedesID    *int64    `json:"supersedes_id,omitempty"`
	SupersededBy    *int64    `json:"superseded_by,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

// RecordVersion is the per-location append-only counter of record changes.
type RecordVersion struct {
	ID         int64     `json:"id"`
	LocationID int64     `json:"location_id"`
	Seq        int       `json:"seq"`
	EventID    int64     `json:"event_id"`
	CreatedAt  time.Time `json:"created_at"`
}

// PlanSnapshot is one stored plan bound to exact data versions.
type PlanSnapshot struct {
	ID                    int64      `json:"id"`
	LocationID            int64      `json:"location_id"`
	RecordVersionID       *int64     `json:"record_version_id"`
	MaterialVersionID     int64      `json:"material_version_id"`
	SpectrumID            *int64     `json:"spectrum_id"`
	BaseDate              *time.Time `json:"base_date"`
	CurrentA              float64    `json:"current_a"`
	CurrentPhysicalL      float64    `json:"current_physical_l"`
	AssumedFromLimit      bool       `json:"assumed_from_limit"`
	CriticalA             float64    `json:"critical_a"`
	FractureA             float64    `json:"fracture_a"`
	NetYieldA             float64    `json:"net_yield_a"`
	AlreadyCritical       bool       `json:"already_critical"`
	DaysToCritical        float64    `json:"days_to_critical"`
	CyclesPerDay          float64    `json:"cycles_per_day"`
	CyclesToCritical      float64    `json:"cycles_to_critical"`
	FittedC               float64    `json:"fitted_c"`
	CoefficientCalibrated bool       `json:"coefficient_calibrated"`
	SafetyFactor          float64    `json:"safety_factor"`
	IntervalDays          float64    `json:"interval_days"`
	NextInspectDate       *time.Time `json:"next_inspect_date"`
	Trigger               string     `json:"trigger"`
	ComputedAt            time.Time  `json:"computed_at"`
}

// NewRecordInput is the payload for appending (or correcting) a record.
type NewRecordInput struct {
	LocationID      int64     `json:"location_id"`
	InspectDate     time.Time `json:"inspect_date"`
	Method          string    `json:"method"`
	Found           bool      `json:"found"`
	CrackLengthM    *float64  `json:"crack_length_m"`
	DetectionLimitM *float64  `json:"detection_limit_m"`
	Inspector       string    `json:"inspector"`
	SupersedesID    *int64    `json:"supersedes_id"`
}
