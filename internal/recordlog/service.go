package recordlog

import (
	"context"
	"sync"
	"time"

	"crackstation/internal/calibration"
	"crackstation/internal/geometry"
	"crackstation/internal/propagation"
	"crackstation/internal/schedule"
	"crackstation/internal/spectrum"
	"crackstation/internal/store"
)

// Service implements the domain use cases over a Repository.
type Service struct {
	repo store.Repository

	// One mutex per location serializes Submit/Correct and the resulting
	// recompute within this process; the Postgres implementation also takes
	// a per-location row lock for multi-process safety.
	muMap map[int64]*sync.Mutex
	mapMu sync.Mutex
	now   func() time.Time
}

// NewService constructs a Service.
func NewService(repo store.Repository) *Service {
	s := &Service{repo: repo, muMap: map[int64]*sync.Mutex{}, now: time.Now}
	if cs, ok := repo.(store.ClockSettable); ok {
		cs.SetClock(func() time.Time { return s.now() })
	}
	return s
}

// SetClock overrides time.Now (tests). It also propagates to repositories
// that stamp created_at from an injectable clock.
func (s *Service) SetClock(f func() time.Time) {
	s.now = f
	if cs, ok := s.repo.(store.ClockSettable); ok {
		cs.SetClock(f)
	}
}

func (s *Service) lockLoc(id int64) *sync.Mutex {
	s.mapMu.Lock()
	mu, ok := s.muMap[id]
	if !ok {
		mu = &sync.Mutex{}
		s.muMap[id] = mu
	}
	s.mapMu.Unlock()
	mu.Lock()
	return mu
}

// ReplayResult is the computed state at a given instant.
type ReplayResult struct {
	Plan                  schedule.Plan `json:"plan"`
	FittedC               float64       `json:"fitted_c"`
	CoefficientCalibrated bool          `json:"calibrated"`
	RecordVersionID       int64         `json:"record_version_id"`
	RecordVersionSeq      int           `json:"record_seq"`
	MaterialVersionID     int64         `json:"material_version_id"`
	SpectrumID            int64         `json:"spectrum_id"`
	NFCount               int           `json:"not_found_records"`
	FoundCount            int           `json:"found_records"`
}

// Submit appends a new inspection record, validates it, recomputes the plan.
func (s *Service) Submit(ctx context.Context, in store.NewRecordInput) (store.Record, ReplayResult, error) {
	return s.append(ctx, in)
}

// Correct appends a correction superseding a prior record, validates it,
// recomputes the plan. The superseded row is retained.
func (s *Service) Correct(ctx context.Context, in store.NewRecordInput) (store.Record, ReplayResult, error) {
	if in.SupersedesID == nil {
		return store.Record{}, ReplayResult{}, FieldError{"supersedes_id", "correction must reference a record"}
	}
	return s.append(ctx, in)
}

func (s *Service) append(ctx context.Context, in store.NewRecordInput) (store.Record, ReplayResult, error) {
	loc, err := s.repo.GetLocation(ctx, in.LocationID)
	if err != nil {
		return store.Record{}, ReplayResult{}, err
	}
	var crack, limit *float64
	if in.Found {
		crack = in.CrackLengthM
	} else {
		limit = in.DetectionLimitM
	}
	if err := ValidateRecord(loc.Geometry, loc.WidthM, loc.CommissionedDate, in.InspectDate,
		in.Found, crack, limit); err != nil {
		return store.Record{}, ReplayResult{}, err
	}
	if in.SupersedesID != nil {
		if _, err := s.repo.GetRecord(ctx, *in.SupersedesID); err != nil {
			return store.Record{}, ReplayResult{}, err
		}
	}
	in.InspectDate = DateOnly(in.InspectDate)

	mu := s.lockLoc(in.LocationID)
	defer mu.Unlock()

	rec, verID, err := s.repo.AppendRecord(ctx, in)
	if err != nil {
		return store.Record{}, ReplayResult{}, err
	}
	trigger := "submission"
	if in.SupersedesID != nil {
		trigger = "correction"
	}
	res, err := s.recomputeCurrent(ctx, in.LocationID, verID, trigger)
	if err != nil {
		return store.Record{}, ReplayResult{}, err
	}
	return rec, res, nil
}

// loadingBundle holds the resolved material/spectrum for a plan evaluation.
type loadingBundle struct {
	mat      store.MaterialVersion
	spectrum store.SpectrumRevision
}

func (s *Service) currentBundle(ctx context.Context, loc store.Location) (loadingBundle, error) {
	mat, err := s.repo.CurrentMaterialVersion(ctx, loc.MaterialID)
	if err != nil {
		return loadingBundle{}, err
	}
	spec, err := s.repo.LatestSpectrum(ctx, loc.ID)
	if err != nil {
		return loadingBundle{}, err
	}
	return loadingBundle{mat: mat, spectrum: spec}, nil
}

func (s *Service) recomputeCurrent(ctx context.Context, locID, verID int64, trigger string) (ReplayResult, error) {
	loc, err := s.repo.GetLocation(ctx, locID)
	if err != nil {
		return ReplayResult{}, err
	}
	bun, err := s.currentBundle(ctx, loc)
	if err != nil {
		return ReplayResult{}, err
	}
	recs, err := s.repo.ListActiveRecords(ctx, locID)
	if err != nil {
		return ReplayResult{}, err
	}
	plan, fitC, calibrated, nFound, nNF, err := s.buildPlan(ctx, loc, bun, recs, s.now())
	if err != nil {
		return ReplayResult{}, err
	}
	plan.RecordVersion = verID

	var specID = bun.spectrum.ID
	var verPtr = verID
	var base *time.Time
	if !plan.BaseDate.IsZero() {
		d := plan.BaseDate
		base = &d
	}
	var next *time.Time
	if !plan.NextDate.IsZero() {
		d := plan.NextDate
		next = &d
	}
	snap := store.PlanSnapshot{
		LocationID:            locID,
		RecordVersionID:       &verPtr,
		MaterialVersionID:     bun.mat.ID,
		SpectrumID:            &specID,
		BaseDate:              base,
		CurrentA:              plan.CurrentA,
		CurrentPhysicalL:      plan.CurrentPhysicalL,
		AssumedFromLimit:      plan.AssumedFromLimit,
		CriticalA:             plan.CriticalA,
		FractureA:             plan.FractureA,
		NetYieldA:             plan.NetYieldA,
		AlreadyCritical:       plan.AlreadyCritical,
		DaysToCritical:        plan.DaysToCritical,
		CyclesPerDay:          plan.CyclesPerDay,
		CyclesToCritical:      plan.CyclesToCritical,
		FittedC:               fitC,
		CoefficientCalibrated: calibrated,
		SafetyFactor:          plan.SafetyFactor,
		IntervalDays:          plan.IntervalDays,
		NextInspectDate:       next,
		Trigger:               trigger,
		ComputedAt:            s.now(),
	}
	if _, err := s.repo.AddPlanSnapshot(ctx, snap); err != nil {
		return ReplayResult{}, err
	}
	return ReplayResult{
		Plan: plan, FittedC: fitC, CoefficientCalibrated: calibrated,
		RecordVersionID: verID, MaterialVersionID: bun.mat.ID,
		SpectrumID: bun.spectrum.ID, NFCount: nNF, FoundCount: nFound,
	}, nil
}

// buildPlan fits C from the ordered records and constructs the plan.
//
// Exposure X for each observation after the first found anchor is
// accumulated piecewise over the spectrum revisions that were in force
// between consecutive inspection dates; intervals before the first stored
// revision use that first revision.
func (s *Service) buildPlan(ctx context.Context, loc store.Location, bun loadingBundle,
	recs []store.Record, evalAt time.Time) (plan schedule.Plan, fitC float64, calibrated bool, nFound, nNF int, err error) {
	m := bun.mat.ParisM
	priorC := bun.mat.ParisC

	revs, err := s.repo.ListSpectrumRevisions(ctx, loc.ID)
	if err != nil {
		return
	}

	obs := make([]calibration.Obs, 0, len(recs))
	anchorIdx := -1
	for i, r := range recs {
		switch {
		case r.Found:
			l := *r.CrackLengthM
			obs = append(obs, calibration.Obs{Found: true, A: geometry.PhysicalToCharacteristic(loc.Geometry, l)})
			if anchorIdx < 0 {
				anchorIdx = i
			}
			nFound++
		default:
			lim := *r.DetectionLimitM
			obs = append(obs, calibration.Obs{Found: false, LimitA: geometry.PhysicalToCharacteristic(loc.Geometry, lim)})
			nNF++
		}
	}

	xs := make([]float64, len(recs))
	for i := range xs {
		xs[i] = -1 // pre-anchor sentinel
	}
	if anchorIdx >= 0 {
		t0 := DateOnly(recs[anchorIdx].InspectDate)
		cum := 0.0
		xs[anchorIdx] = 0
		prev := t0
		for i := anchorIdx + 1; i < len(recs); i++ {
			ti := DateOnly(recs[i].InspectDate)
			days := ti.Sub(prev).Hours() / 24
			if days < 0 {
				days = 0
			}
			cum += weightedExposure(revs, m, prev, ti, bun.spectrum, days)
			xs[i] = cum
			prev = ti
		}
	}

	f := calibration.Fit(loc.Geometry, loc.WidthM, m, priorC, obs, xs)
	fitC = f.C
	calibrated = f.Calibrated

	baseDate := DateOnly(loc.CommissionedDate)
	currentPhys := 0.0005
	currentA := geometry.PhysicalToCharacteristic(loc.Geometry, currentPhys)
	assumed := true
	if nFound > 0 {
		var last store.Record
		for i := anchorIdx; i < len(recs); i++ {
			if recs[i].Found {
				last = recs[i]
			}
		}
		baseDate = DateOnly(last.InspectDate)
		currentPhys = *last.CrackLengthM
		currentA = geometry.PhysicalToCharacteristic(loc.Geometry, currentPhys)
		assumed = false
	} else if nNF > 0 {
		last := recs[len(recs)-1]
		baseDate = DateOnly(last.InspectDate)
		currentPhys = *last.DetectionLimitM
		currentA = geometry.PhysicalToCharacteristic(loc.Geometry, currentPhys)
		assumed = true
	}

	blocks := bun.spectrum.Blocks
	params := propagation.Params{
		Geo:   loc.Geometry,
		Width: loc.WidthM,
		Material: propagation.Material{
			ParisM: m, ParisC: fitC,
			FractureKIC: bun.mat.FractureKIC, YieldStrength: bun.mat.YieldStrength,
		},
		Blocks: blocks,
	}
	plan = schedule.Compute(schedule.Input{
		Params:                params,
		BaseDate:              baseDate,
		CurrentA:              currentA,
		CurrentPhysicalL:      currentPhys,
		AssumedFromLimit:      assumed,
		SafetyFactor:          loc.SafetyFactor,
		ParisC:                fitC,
		CoefficientCalibrated: calibrated,
		MaterialVersion:       bun.mat.Version,
		SpectrumID:            bun.spectrum.ID,
		CyclesPerDay:          spectrum.TotalCyclesPerDay(blocks),
		Now:                   evalAt,
	})
	return
}

// weightedExposure integrates W(t)*days over [from, to] across spectrum
// revisions. The first revision (or fall if none) covers the portion before
// it was created.
func weightedExposure(revs []store.SpectrumRevision, m float64, from, to time.Time,
	fall store.SpectrumRevision, totalDays float64) float64 {
	if totalDays <= 0 {
		return 0
	}
	if len(revs) == 0 {
		return propagation.SpectrumWeight(m, fall.Blocks) * totalDays
	}
	// Interior change points strictly between from and to.
	var cuts []time.Time
	for _, r := range revs {
		d := DateOnly(r.CreatedAt)
		if d.After(from) && d.Before(to) {
			cuts = append(cuts, d)
		}
	}
	expo := 0.0
	prev := from
	// spec on [prev, cut) is the revision effective at prev.
	apply := func(start, end time.Time) {
		days := end.Sub(start).Hours() / 24
		spec := revEffectiveAt(revs, start, revs[0])
		expo += propagation.SpectrumWeight(m, spec.Blocks) * days
	}
	for _, c := range cuts {
		apply(prev, c)
		prev = c
	}
	apply(prev, to)
	return expo
}

// revEffectiveAt returns the revision in force at instant t, i.e. the last
// one created no later than t; before the first, dflt is returned.
func revEffectiveAt(revs []store.SpectrumRevision, t time.Time, dflt store.SpectrumRevision) store.SpectrumRevision {
	chosen := dflt
	for _, r := range revs {
		if !DateOnly(r.CreatedAt).After(t) {
			chosen = r
		}
	}
	return chosen
}
