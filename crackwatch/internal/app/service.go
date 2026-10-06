package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"crackwatch/internal/domain/geometry"
	"crackwatch/internal/domain/material"
	"crackwatch/internal/domain/spectrum"
	"crackwatch/internal/model"
	"crackwatch/internal/store"
)

// Service is the application façade used by the HTTP layer.
type Service struct {
	st  store.Store
	now func() time.Time
	// tick advances the injected clock (tests); no-op in production.
	tick func()
}

// New constructs a service over the given store.
func New(st store.Store) *Service {
	return &Service{
		st:   st,
		now:  func() time.Time { return time.Now().UTC() },
		tick: func() {},
	}
}

// SetClock overrides the wall clock (tests inject a fixed clock).
func (s *Service) SetClock(f func() time.Time) { s.now = f }

// SetTicker overrides the clock-advance hook used by tests.
func (s *Service) SetTicker(f func()) { s.tick = f }

// Now returns the service clock time.
func (s *Service) Now() time.Time { return s.now() }

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// ---------------------------------------------------------------------------
// Materials
// ---------------------------------------------------------------------------

// CreateMaterialInput creates a grade and its first parameter version.
type CreateMaterialInput struct {
	Grade       string
	Params      material.Params
	EffectiveAt time.Time // zero = effective since the beginning of time
}

// validateMaterial names each invalid fracture-mechanics field.
func validateMaterial(p material.Params) FieldErrors {
	var errs FieldErrors
	if p.K1c <= 0 {
		addField(&errs, "k1c", "fracture toughness must be positive")
	}
	if p.Sy <= 0 {
		addField(&errs, "sy", "yield strength must be positive")
	}
	if p.M <= 0 {
		addField(&errs, "m", "Paris exponent must be positive")
	}
	if p.C <= 0 {
		addField(&errs, "c", "Paris coefficient must be positive")
	}
	return errs
}

// CreateMaterial stores a material with version 1.
func (s *Service) CreateMaterial(ctx context.Context, in CreateMaterialInput) (*model.Material, *model.MaterialVersion, error) {
	var errs FieldErrors
	if in.Grade == "" {
		addField(&errs, "grade", "must not be empty")
	}
	errs = append(errs, validateMaterial(in.Params)...)
	if len(errs) > 0 {
		return nil, nil, errs
	}
	now := s.now()
	m := &model.Material{ID: "mat_" + newID(), Grade: in.Grade, CreatedAt: now}
	v := &model.MaterialVersion{
		ID: "mv_" + newID(), MaterialID: m.ID, Version: 1,
		Params: in.Params, EffectiveAt: in.EffectiveAt.UTC(), CreatedAt: now,
	}
	err := s.st.WithLocationTx(ctx, "material:"+m.ID, func(tx store.Tx) error {
		if err := tx.CreateMaterial(ctx, m); err != nil {
			return err
		}
		return tx.CreateMaterialVersion(ctx, v)
	})
	if err != nil {
		return nil, nil, err
	}
	return m, v, nil
}

// AddMaterialVersion appends a new parameter version and returns the version
// together with the impact list: locations of this grade whose next
// inspection date moves earlier under the new parameters.
func (s *Service) AddMaterialVersion(ctx context.Context, materialID string, p material.Params, effectiveAt time.Time) (*model.MaterialVersion, []ImpactEntry, error) {
	if errs := validateMaterial(p); len(errs) > 0 {
		return nil, nil, errs
	}
	now := s.now()
	if effectiveAt.IsZero() {
		effectiveAt = now
	}
	var impact []ImpactEntry
	var v *model.MaterialVersion
	err := s.st.WithLocationTx(ctx, "material:"+materialID, func(tx store.Tx) error {
		old, err := tx.LatestMaterialVersion(ctx, materialID)
		if err != nil {
			return err
		}
		v = &model.MaterialVersion{
			ID: "mv_" + newID(), MaterialID: materialID, Version: old.Version + 1,
			Params: p, EffectiveAt: effectiveAt.UTC(), CreatedAt: now,
		}
		if err := tx.CreateMaterialVersion(ctx, v); err != nil {
			return err
		}
		impact, err = s.materialImpact(ctx, tx, materialID, old.Params, p)
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	return v, impact, nil
}

// ImpactEntry describes one location whose next inspection is brought
// forward by a material parameter change.
type ImpactEntry struct {
	LocationID        string    `json:"location_id"`
	Name              string    `json:"name"`
	OldNextInspection time.Time `json:"old_next_inspection_at"`
	NewNextInspection time.Time `json:"new_next_inspection_at"`
	EarlierDays       float64   `json:"earlier_days"`
}

// materialImpact compares plans for every location of a grade under old and
// new material parameters, holding everything else (findings, spectrum)
// fixed at what is currently known.
func (s *Service) materialImpact(ctx context.Context, r store.StoreReader, materialID string, oldP, newP material.Params) ([]ImpactEntry, error) {
	locs, err := r.ListLocations(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now()
	var out []ImpactEntry
	for _, loc := range locs {
		if loc.MaterialID != materialID {
			continue
		}
		oldPlan, err := projectWithMaterial(ctx, r, loc, now, oldP)
		if err != nil {
			return nil, err
		}
		newPlan, err := projectWithMaterial(ctx, r, loc, now, newP)
		if err != nil {
			return nil, err
		}
		// Locations with no finding yet, or already at critical, have no
		// calendar date to move.
		if oldPlan.Plan.NoFinding || oldPlan.Plan.NextInspectionAt.IsZero() {
			continue
		}
		if newPlan.Plan.NextInspectionAt.Before(oldPlan.Plan.NextInspectionAt) {
			out = append(out, ImpactEntry{
				LocationID: loc.ID, Name: loc.Name,
				OldNextInspection: oldPlan.Plan.NextInspectionAt,
				NewNextInspection: newPlan.Plan.NextInspectionAt,
				EarlierDays:       oldPlan.Plan.NextInspectionAt.Sub(newPlan.Plan.NextInspectionAt).Hours() / 24,
			})
		}
	}
	return out, nil
}

// GetMaterial / ListMaterials pass through to the store.
func (s *Service) GetMaterial(ctx context.Context, id string) (*model.Material, error) {
	return s.st.GetMaterial(ctx, id)
}
func (s *Service) ListMaterials(ctx context.Context) ([]model.Material, error) {
	return s.st.ListMaterials(ctx)
}

// ---------------------------------------------------------------------------
// Locations and spectra
// ---------------------------------------------------------------------------

// LocationInput is the create payload.
type LocationInput struct {
	Name           string
	Geometry       geometry.Kind
	WidthMM        float64
	MaterialID     string
	SafetyFactor   float64 // zero defaults to 2.0
	CommissionedAt time.Time
	Blocks         []spectrum.Block
}

// validateBlocks validates each spectrum block and names the exact failing
// field (blocks[i].field), plus a load_spectrum-level check that at least one
// active block exists.
func validateBlocks(blocks []spectrum.Block) FieldErrors {
	var errs FieldErrors
	active := 0.0
	for i, b := range blocks {
		pfx := fmt.Sprintf("blocks[%d].", i)
		if b.StressAmp < 0 {
			addField(&errs, pfx+"stress_amp", "must not be negative")
		}
		if b.StressAmp > 2*b.StressMax {
			addField(&errs, pfx+"stress_amp", "must not exceed 2 * stress_max")
		}
		if b.CyclesPerDay < 0 {
			addField(&errs, pfx+"cycles_per_day", "must not be negative")
		}
		if b.StressAmp > 0 && b.CyclesPerDay > 0 {
			active += b.CyclesPerDay
		}
	}
	if active == 0 {
		addField(&errs, "load_spectrum",
			"must contain at least one block with positive stress amplitude and cycles per day")
	}
	return errs
}

func (s *Service) validateLocation(ctx context.Context, in *LocationInput) FieldErrors {
	var errs FieldErrors
	if in.Name == "" {
		addField(&errs, "name", "must not be empty")
	}
	if in.WidthMM <= 0 {
		addField(&errs, "width_mm", "must be positive")
	}
	if _, err := geometry.New(in.Geometry, 1.0); err != nil {
		addField(&errs, "geometry", err.Error())
	}
	if in.CommissionedAt.IsZero() {
		addField(&errs, "commissioned_at", "must be provided")
	}
	if in.MaterialID == "" {
		addField(&errs, "material_id", "must be provided")
	} else if _, err := s.st.GetMaterial(ctx, in.MaterialID); err != nil {
		addField(&errs, "material_id", "unknown material")
	}
	if in.SafetyFactor == 0 {
		in.SafetyFactor = 2.0
	} else if in.SafetyFactor <= 0 {
		addField(&errs, "safety_factor", "must be positive")
	}
	errs = append(errs, validateBlocks(in.Blocks)...)
	return errs
}

// CreateLocation stores the location and its initial spectrum version.
func (s *Service) CreateLocation(ctx context.Context, in LocationInput) (*model.Location, *model.SpectrumVersion, error) {
	errs := s.validateLocation(ctx, &in)
	if len(errs) > 0 {
		return nil, nil, errs
	}
	now := s.now()
	loc := &model.Location{
		ID: "loc_" + newID(), Name: in.Name, Geometry: in.Geometry,
		WidthMM: in.WidthMM, MaterialID: in.MaterialID, SafetyFactor: in.SafetyFactor,
		CommissionedAt: in.CommissionedAt.UTC(), CreatedAt: now, UpdatedAt: now,
	}
	sp := &model.SpectrumVersion{
		ID: "sp_" + newID(), LocationID: loc.ID, Version: 1,
		Blocks: in.Blocks, EffectiveAt: in.CommissionedAt.UTC(), CreatedAt: now,
	}
	err := s.st.WithLocationTx(ctx, loc.ID, func(tx store.Tx) error {
		if err := tx.CreateLocation(ctx, loc); err != nil {
			return err
		}
		return tx.CreateSpectrumVersion(ctx, sp)
	})
	if err != nil {
		return nil, nil, err
	}
	return loc, sp, nil
}

// UpdateLocation changes mutable attributes (name, safety factor). Geometry
// and width are structural: a changed plate is a new monitored location.
func (s *Service) UpdateLocation(ctx context.Context, id string, name *string, safetyFactor *float64) (*model.Location, error) {
	var out *model.Location
	err := s.st.WithLocationTx(ctx, id, func(tx store.Tx) error {
		loc, err := tx.GetLocation(ctx, id)
		if err != nil {
			return err
		}
		var errs FieldErrors
		if name != nil {
			if *name == "" {
				addField(&errs, "name", "must not be empty")
			}
			loc.Name = *name
		}
		if safetyFactor != nil {
			if *safetyFactor <= 0 {
				addField(&errs, "safety_factor", "must be positive")
			}
			loc.SafetyFactor = *safetyFactor
		}
		if len(errs) > 0 {
			return errs
		}
		loc.UpdatedAt = s.now()
		if err := tx.UpdateLocation(ctx, loc); err != nil {
			return err
		}
		out = loc
		return nil
	})
	return out, err
}

// ReplaceSpectrum appends a new spectrum version (effective immediately
// unless an explicit time is given).
func (s *Service) ReplaceSpectrum(ctx context.Context, locID string, blocks []spectrum.Block, effectiveAt time.Time) (*model.SpectrumVersion, error) {
	if errs := validateBlocks(blocks); len(errs) > 0 {
		return nil, errs
	}
	now := s.now()
	if effectiveAt.IsZero() {
		effectiveAt = now
	}
	var sp *model.SpectrumVersion
	err := s.st.WithLocationTx(ctx, locID, func(tx store.Tx) error {
		if _, err := tx.GetLocation(ctx, locID); err != nil {
			return err
		}
		old, err := tx.LatestSpectrumVersion(ctx, locID)
		if err != nil {
			return err
		}
		sp = &model.SpectrumVersion{
			ID: "sp_" + newID(), LocationID: locID, Version: old.Version + 1,
			Blocks: blocks, EffectiveAt: effectiveAt.UTC(), CreatedAt: now,
		}
		return tx.CreateSpectrumVersion(ctx, sp)
	})
	return sp, err
}

func (s *Service) GetLocation(ctx context.Context, id string) (*model.Location, error) {
	return s.st.GetLocation(ctx, id)
}
func (s *Service) ListLocations(ctx context.Context) ([]model.Location, error) {
	return s.st.ListLocations(ctx)
}
func (s *Service) GetSpectrum(ctx context.Context, locID string) (*model.SpectrumVersion, error) {
	return s.st.LatestSpectrumVersion(ctx, locID)
}
func (s *Service) GetMaterialVersion(ctx context.Context, id string) (*model.MaterialVersion, error) {
	return s.st.GetMaterialVersion(ctx, id)
}
