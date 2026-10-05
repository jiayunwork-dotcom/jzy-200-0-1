// Package schedule turns remaining-life integration results into inspection
// plans: the next inspection date is the latest inspection date plus
// (days to critical)/safety factor.
package schedule

import (
	"math"
	"time"

	"crackstation/internal/propagation"
)

// DefaultSafetyFactor is applied when a location specifies none.
const DefaultSafetyFactor = 2.0

// Plan is one computed inspection plan, bound to the data versions used.
type Plan struct {
	BaseDate         time.Time `json:"base_date"`
	CurrentA         float64   `json:"current_a"`
	CurrentPhysicalL float64   `json:"current_physical_length_m"`
	AssumedFromLimit bool      `json:"assumed_from_detection_limit"`

	CriticalA       float64 `json:"critical_a"`
	FractureA       float64 `json:"fracture_a"`
	NetYieldA       float64 `json:"net_yield_a"`
	AlreadyCritical bool    `json:"already_critical"`

	DaysToCritical   float64 `json:"days_to_critical"`
	CyclesPerDay     float64 `json:"cycles_per_day"`
	CyclesToCritical float64 `json:"cycles_to_critical"`

	SafetyFactor float64   `json:"safety_factor"`
	IntervalDays float64   `json:"interval_days"`
	NextDate     time.Time `json:"next_inspect_date"`

	ParisC                float64   `json:"paris_c_used"`
	CoefficientCalibrated bool      `json:"coefficient_calibrated"`
	MaterialGrade         string    `json:"material_grade"`
	MaterialVersion       int       `json:"material_version"`
	SpectrumID            int64     `json:"spectrum_id"`
	RecordVersion         int64     `json:"record_version_id"`
	ComputedAt            time.Time `json:"computed_at"`
}

// Input bundles everything Compute needs.
type Input struct {
	Params                propagation.Params
	BaseDate              time.Time
	CurrentA              float64
	CurrentPhysicalL      float64
	AssumedFromLimit      bool
	SafetyFactor          float64
	ParisC                float64
	CoefficientCalibrated bool
	MaterialGrade         string
	MaterialVersion       int
	SpectrumID            int64
	RecordVersion         int64
	CyclesPerDay          float64
	Now                   time.Time
}

// Compute evaluates the plan.
func Compute(in Input) Plan {
	sf := in.SafetyFactor
	if sf <= 0 {
		sf = DefaultSafetyFactor
	}
	aC, fracA, yieldA, rootOK := propagation.CriticalA(in.Params)
	if !rootOK {
		aC = in.CurrentA
	}

	p := Plan{
		BaseDate:              in.BaseDate,
		CurrentA:              in.CurrentA,
		CurrentPhysicalL:      in.CurrentPhysicalL,
		AssumedFromLimit:      in.AssumedFromLimit,
		CriticalA:             aC,
		FractureA:             fracA,
		NetYieldA:             yieldA,
		SafetyFactor:          sf,
		ParisC:                in.ParisC,
		CoefficientCalibrated: in.CoefficientCalibrated,
		MaterialGrade:         in.MaterialGrade,
		MaterialVersion:       in.MaterialVersion,
		SpectrumID:            in.SpectrumID,
		RecordVersion:         in.RecordVersion,
		CyclesPerDay:          in.CyclesPerDay,
	}

	if !rootOK || in.CurrentA >= aC {
		p.AlreadyCritical = true
		p.NextDate = in.BaseDate
		p.ComputedAt = in.Now
		return p
	}

	days, err := propagation.DaysTo(in.Params, in.CurrentA, aC)
	if err != nil {
		p.AlreadyCritical = true
		p.NextDate = in.BaseDate
		p.ComputedAt = in.Now
		return p
	}
	p.DaysToCritical = days
	p.CyclesToCritical = days * in.CyclesPerDay
	p.IntervalDays = days / sf
	if math.IsInf(p.IntervalDays, 0) {
		p.IntervalDays = 0
	} else {
		p.NextDate = in.BaseDate.AddDate(0, 0, int(math.Round(p.IntervalDays)))
	}
	p.ComputedAt = in.Now
	return p
}

// Compare summarises the difference between two plans.
type Compare struct {
	ThenNextDate        time.Time `json:"then_next_inspect_date"`
	NowNextDate         time.Time `json:"now_next_inspect_date"`
	DaysDelta           int       `json:"days_delta"`
	MovedEarlier        bool      `json:"moved_earlier"`
	ThenRecordVersion   int64     `json:"then_record_version_id"`
	NowRecordVersion    int64     `json:"now_record_version_id"`
	ThenMaterialVersion int       `json:"then_material_version"`
	NowMaterialVersion  int       `json:"now_material_version"`
}

// Diff compares a historical ("then") plan with the current one.
func Diff(then, now Plan) Compare {
	c := Compare{
		ThenNextDate:        then.NextDate,
		NowNextDate:         now.NextDate,
		ThenRecordVersion:   then.RecordVersion,
		NowRecordVersion:    now.RecordVersion,
		ThenMaterialVersion: then.MaterialVersion,
		NowMaterialVersion:  now.MaterialVersion,
	}
	if !then.NextDate.IsZero() && !now.NextDate.IsZero() {
		c.DaysDelta = int(now.NextDate.Sub(then.NextDate).Hours() / 24)
		c.MovedEarlier = now.NextDate.Before(then.NextDate)
	}
	return c
}
