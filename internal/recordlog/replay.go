package recordlog

import (
	"context"
	"sort"
	"time"

	"crackstation/internal/geometry"
	"crackstation/internal/schedule"
	"crackstation/internal/store"
)

// AsOf replays the plan exactly as it was knowable at instant t:
//
//   - only records created at or before t and not already superseded by t;
//   - the material version created at or before t;
//   - spectrum revisions created at or before t;
//   - the record version seq in force at t (0 if none yet).
//
// The returned ReplayResult is NOT persisted (replay is read-only).
func (s *Service) AsOf(ctx context.Context, locID int64, t time.Time) (ReplayResult, error) {
	t = t.UTC()
	loc, err := s.repo.GetLocation(ctx, locID)
	if err != nil {
		return ReplayResult{}, err
	}
	mat, err := s.repo.MaterialVersionAt(ctx, loc.MaterialID, t)
	if err != nil {
		return ReplayResult{}, err
	}
	spec, err := s.repo.SpectrumAt(ctx, locID, t)
	if err != nil {
		return ReplayResult{}, err
	}
	all, err := s.repo.ListRecordHistory(ctx, locID)
	if err != nil {
		return ReplayResult{}, err
	}
	// Active at t: row created <= t and either never superseded or its
	// superseding row did not yet exist at t.
	byID := map[int64]store.Record{}
	for _, r := range all {
		byID[r.ID] = r
	}
	var recs []store.Record
	for _, r := range all {
		if r.CreatedAt.After(t) {
			continue
		}
		active := r.SupersededBy == nil
		if !active {
			if repl, ok := byID[*r.SupersededBy]; !ok || repl.CreatedAt.After(t) {
				active = true // correction did not exist yet at t
			}
		}
		if active {
			recs = append(recs, r)
		}
	}
	sort.Slice(recs, func(i, j int) bool {
		if !recs[i].InspectDate.Equal(recs[j].InspectDate) {
			return recs[i].InspectDate.Before(recs[j].InspectDate)
		}
		if !recs[i].CreatedAt.Equal(recs[j].CreatedAt) {
			return recs[i].CreatedAt.Before(recs[j].CreatedAt)
		}
		return recs[i].ID < recs[j].ID
	})

	bun := loadingBundle{mat: mat, spectrum: spec}
	plan, fitC, calibrated, nFound, nNF, err := s.buildPlan(ctx, loc, bun, recs, t)
	if err != nil {
		return ReplayResult{}, err
	}
	verID, seq, _ := s.repo.RecordVersionAt(ctx, locID, t)
	plan.RecordVersion = verID
	return ReplayResult{
		Plan: plan, FittedC: fitC, CoefficientCalibrated: calibrated,
		RecordVersionID: verID, RecordVersionSeq: seq,
		MaterialVersionID: mat.ID, SpectrumID: spec.ID,
		NFCount: nNF, FoundCount: nFound,
	}, nil
}

// Current returns the plan under the data existing right now. Since the
// record history is append-only and plans are pure functions of the active
// records plus current material/spectrum versions, this equals the latest
// stored snapshot; stored snapshots remain available for audit/version
// inspection via the repository.
func (s *Service) Current(ctx context.Context, locID int64) (ReplayResult, error) {
	verID, err := s.repo.CurrentRecordVersion(ctx, locID)
	if err != nil {
		return ReplayResult{}, err
	}
	if verID == 0 {
		// No records yet: plan from commission assumptions.
		loc, err := s.repo.GetLocation(ctx, locID)
		if err != nil {
			return ReplayResult{}, err
		}
		bun, err := s.currentBundle(ctx, loc)
		if err != nil {
			return ReplayResult{}, err
		}
		plan, fitC, calibrated, nFound, nNF, err := s.buildPlan(ctx, loc, bun, nil, s.now())
		if err != nil {
			return ReplayResult{}, err
		}
		return ReplayResult{Plan: plan, FittedC: fitC, CoefficientCalibrated: calibrated,
			MaterialVersionID: bun.mat.ID, SpectrumID: bun.spectrum.ID, NFCount: nNF, FoundCount: nFound}, nil
	}
	return s.AsOf(ctx, locID, s.now())
}

// RemainingLife is the current plan's remaining-life numbers from today:
// days and cycles to critical measured from the controlling inspection
// state, plus the critical crack lengths.
type RemainingLife struct {
	DaysToCritical   float64 `json:"days_to_critical"`
	CyclesToCritical float64 `json:"cycles_to_critical"`
	CyclesPerDay     float64 `json:"cycles_per_day"`
	CriticalPhysical float64 `json:"critical_physical_length_m"`
	FracturePhysical float64 `json:"fracture_physical_length_m"`
	NetYieldPhysical float64 `json:"net_yield_physical_length_m"`
	GoverningMode    string  `json:"governing_mode"`
	AlreadyCritical  bool    `json:"already_critical"`
	CurrentPhysical  float64 `json:"current_physical_length_m"`
}

// RemainingLifeOf returns remaining life under the current data.
func (s *Service) RemainingLifeOf(ctx context.Context, locID int64) (RemainingLife, error) {
	loc, err := s.repo.GetLocation(ctx, locID)
	if err != nil {
		return RemainingLife{}, err
	}
	res, err := s.Current(ctx, locID)
	if err != nil {
		return RemainingLife{}, err
	}
	p := res.Plan
	rl := RemainingLife{
		DaysToCritical:   p.DaysToCritical,
		CyclesToCritical: p.CyclesToCritical,
		CyclesPerDay:     p.CyclesPerDay,
		CriticalPhysical: geometry.CharacteristicToPhysical(loc.Geometry, p.CriticalA),
		FracturePhysical: physicalOrInf(loc, p.FractureA),
		NetYieldPhysical: physicalOrInf(loc, p.NetYieldA),
		AlreadyCritical:  p.AlreadyCritical,
		CurrentPhysical:  p.CurrentPhysicalL,
	}
	if !isInfOrNaN(p.FractureA) && p.FractureA <= p.NetYieldA {
		rl.GoverningMode = "fracture"
	} else if !isInfOrNaN(p.NetYieldA) {
		rl.GoverningMode = "net_yield"
	}
	return rl, nil
}

func physicalOrInf(loc store.Location, a float64) float64 {
	if isInfOrNaN(a) {
		return a
	}
	return geometry.CharacteristicToPhysical(loc.Geometry, a)
}

func isInfOrNaN(a float64) bool {
	return a != a || a > 1e300 || a < -1e300
}

// CompareAt compares the plan as-of t with the current plan.
func (s *Service) CompareAt(ctx context.Context, locID int64, t time.Time) (HistoricalComparison, error) {
	then, err := s.AsOf(ctx, locID, t)
	if err != nil {
		return HistoricalComparison{}, err
	}
	now, err := s.Current(ctx, locID)
	if err != nil {
		return HistoricalComparison{}, err
	}
	d := schedule.Diff(then.Plan, now.Plan)
	return HistoricalComparison{
		AsOf: t, Diff: d,
		ThenNextDate: d.ThenNextDate, NowNextDate: d.NowNextDate,
		ThenRecordSeq: then.RecordVersionSeq, ThenMaterialVersion: then.Plan.MaterialVersion,
		NowMaterialVersion: now.Plan.MaterialVersion,
		ThenIntervalDays:   then.Plan.IntervalDays, NowIntervalDays: now.Plan.IntervalDays,
	}, nil
}

// HistoricalComparison is the diff payload.
type HistoricalComparison struct {
	AsOf                time.Time        `json:"as_of"`
	Diff                schedule.Compare `json:"diff"`
	ThenNextDate        time.Time        `json:"-"`
	NowNextDate         time.Time        `json:"-"`
	ThenRecordSeq       int              `json:"then_record_seq"`
	ThenMaterialVersion int              `json:"then_material_version"`
	NowMaterialVersion  int              `json:"now_material_version"`
	ThenIntervalDays    float64          `json:"then_interval_days"`
	NowIntervalDays     float64          `json:"now_interval_days"`
}

// MaterialImpactEntry is one location affected by a material parameter update.
type MaterialImpactEntry struct {
	LocationID       int64     `json:"location_id"`
	OldNextDate      time.Time `json:"old_next_inspect_date"`
	NewNextDate      time.Time `json:"new_next_inspect_date"`
	DaysDelta        int       `json:"days_delta"`
	MovedEarlier     bool      `json:"moved_earlier"`
	OldMaterialVerID int64     `json:"old_material_version_id"`
	NewMaterialVerID int64     `json:"new_material_version_id"`
}

// AddMaterialVersion validates and stores the new version, recomputes plans
// for every location using the grade, and lists those whose next inspection
// moved earlier.
func (s *Service) AddMaterialVersion(ctx context.Context, v store.MaterialVersion) (store.MaterialVersion, []MaterialImpactEntry, error) {
	if err := ValidateMaterialVersion(v); err != nil {
		return store.MaterialVersion{}, nil, err
	}
	locs, err := s.repo.ListLocationsByMaterial(ctx, v.MaterialID)
	if err != nil {
		return store.MaterialVersion{}, nil, err
	}
	type before struct {
		plan schedule.Plan
		ver  int64
	}
	befores := map[int64]before{}
	for _, loc := range locs {
		res, err := s.Current(ctx, loc.ID)
		if err != nil {
			continue // locations without spectrum yet cannot be planned
		}
		befores[loc.ID] = before{plan: res.Plan, ver: res.MaterialVersionID}
	}

	nv, err := s.repo.AddMaterialVersion(ctx, v)
	if err != nil {
		return store.MaterialVersion{}, nil, err
	}

	var impacts []MaterialImpactEntry
	for _, loc := range locs {
		mu := s.lockLoc(loc.ID)
		verID, err := s.repo.CurrentRecordVersion(ctx, loc.ID)
		if err != nil || verID == 0 {
			mu.Unlock()
			continue
		}
		res, err := s.recomputeCurrent(ctx, loc.ID, verID, "material_update")
		mu.Unlock()
		if err != nil {
			continue
		}
		b, ok := befores[loc.ID]
		if !ok {
			continue
		}
		if !b.plan.NextDate.IsZero() && !res.Plan.NextDate.IsZero() &&
			res.Plan.NextDate.Before(b.plan.NextDate) {
			impacts = append(impacts, MaterialImpactEntry{
				LocationID:       loc.ID,
				OldNextDate:      b.plan.NextDate,
				NewNextDate:      res.Plan.NextDate,
				DaysDelta:        int(res.Plan.NextDate.Sub(b.plan.NextDate).Hours() / 24),
				MovedEarlier:     true,
				OldMaterialVerID: b.ver,
				NewMaterialVerID: nv.ID,
			})
		}
	}
	sort.Slice(impacts, func(i, j int) bool { return impacts[i].LocationID < impacts[j].LocationID })
	return nv, impacts, nil
}

// RecomputeSpectrumChange is called after a new spectrum revision is stored;
// it recomputes the plan snapshot for the location.
func (s *Service) RecomputeSpectrumChange(ctx context.Context, locID int64) (ReplayResult, error) {
	mu := s.lockLoc(locID)
	defer mu.Unlock()
	verID, err := s.repo.CurrentRecordVersion(ctx, locID)
	if err != nil || verID == 0 {
		return ReplayResult{}, err
	}
	return s.recomputeCurrent(ctx, locID, verID, "spectrum_update")
}

func snapshotToResult(snap store.PlanSnapshot) ReplayResult {
	var base time.Time
	if snap.BaseDate != nil {
		base = *snap.BaseDate
	}
	var next *time.Time = snap.NextInspectDate
	p := schedule.Plan{
		BaseDate: base,
		CurrentA: snap.CurrentA, CurrentPhysicalL: snap.CurrentPhysicalL,
		AssumedFromLimit: snap.AssumedFromLimit,
		CriticalA:        snap.CriticalA, FractureA: snap.FractureA, NetYieldA: snap.NetYieldA,
		AlreadyCritical: snap.AlreadyCritical,
		DaysToCritical:  snap.DaysToCritical, CyclesPerDay: snap.CyclesPerDay,
		CyclesToCritical: snap.CyclesToCritical,
		SafetyFactor:     snap.SafetyFactor, IntervalDays: snap.IntervalDays,
		ParisC: snap.FittedC, CoefficientCalibrated: snap.CoefficientCalibrated,
		ComputedAt: snap.ComputedAt,
	}
	if snap.RecordVersionID != nil {
		p.RecordVersion = *snap.RecordVersionID
	}
	if next != nil {
		p.NextDate = *next
	}
	return ReplayResult{
		Plan: p, FittedC: snap.FittedC,
		CoefficientCalibrated: snap.CoefficientCalibrated,
		RecordVersionID:       p.RecordVersion, MaterialVersionID: snap.MaterialVersionID,
		SpectrumID: ptrVal(snap.SpectrumID),
	}
}

func ptrVal(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}
