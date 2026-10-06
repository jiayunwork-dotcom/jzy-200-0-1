package app

import (
	"context"
	"sort"
	"time"

	"crackwatch/internal/domain/calibration"
	"crackwatch/internal/domain/geometry"
	"crackwatch/internal/domain/material"
	"crackwatch/internal/domain/plan"
	"crackwatch/internal/model"
	"crackwatch/internal/store"
)

// resolveFindings reduces the raw append-only event list to the effective
// chronological observations known as of a given point.
//
// Resolution rules (order independent by construction):
//   - an event whose SupersedesID names an earlier event renders that
//     predecessor ineffective;
//   - void events themselves produce no observation but still suppress their
//     target (this is how deletions work);
//   - the remaining events are sorted by (inspected_at, seq), so a backfilled
//     older record ends up in exactly the same place as if it had been
//     entered chronologically.
func resolveFindings(g geometry.Geometry, events []model.InspectionEvent) []model.Finding {
	superseded := map[string]bool{}
	for _, e := range events {
		if e.SupersedesID != "" {
			superseded[e.SupersedesID] = true
		}
	}
	var out []model.Finding
	for _, e := range events {
		if superseded[e.ID] {
			continue
		}
		if e.Kind == "void" {
			continue
		}
		f := model.Finding{Event: e}
		switch e.Kind {
		case model.ResultFound:
			f.A = g.ToCharacteristic(e.LengthMM / 1000)
		case model.ResultNotDetected:
			f.A = g.ToCharacteristic(e.DetectLimitMM / 1000)
			f.UpperBound = true
		default:
			continue
		}
		out = append(out, f)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Event.InspectedAt.Equal(out[j].Event.InspectedAt) {
			return out[i].Event.Seq < out[j].Event.Seq
		}
		return out[i].Event.InspectedAt.Before(out[j].Event.InspectedAt)
	})
	return out
}

// toObservations converts findings for the calibration/plan packages.
func toObservations(fs []model.Finding) []calibration.Observation {
	obs := make([]calibration.Observation, 0, len(fs))
	for _, f := range fs {
		obs = append(obs, calibration.Observation{
			At: f.Event.InspectedAt, A: f.A, UpperBound: f.UpperBound,
		})
	}
	return obs
}

// projection is one fully derived plan for a location under the data known at
// a fixed instant.
type projection struct {
	Location model.Location
	Plan     model.Plan
	Events   []model.InspectionEvent
	Findings []model.Finding
	Material model.MaterialVersion
	Spectrum model.SpectrumVersion
}

// project computes the plan for loc using only events recorded before asOf
// and parameter versions effective at asOf.
func project(ctx context.Context, r store.StoreReader, loc model.Location, asOf time.Time) (*projection, error) {
	return projectFrom(ctx, r, loc, asOf, nil)
}

// projectWithMaterial is project with the material parameters overridden;
// used by material-update impact analysis so old/new plans differ only in
// the material.
func projectWithMaterial(ctx context.Context, r store.StoreReader, loc model.Location, asOf time.Time, matP material.Params) (*projection, error) {
	return projectFrom(ctx, r, loc, asOf, &matP)
}

func projectFrom(ctx context.Context, r store.StoreReader, loc model.Location, asOf time.Time, matOverride *material.Params) (*projection, error) {
	g, err := geometry.New(loc.Geometry, loc.WidthM())
	if err != nil {
		return nil, err
	}
	mv, err := r.MaterialVersionAt(ctx, loc.MaterialID, asOf)
	if err != nil {
		return nil, err
	}
	if matOverride != nil {
		cp := *mv
		cp.Params = *matOverride
		mv = &cp
	}
	sv, err := r.SpectrumVersionAt(ctx, loc.ID, asOf)
	if err != nil {
		return nil, err
	}
	events, err := r.EventsKnownAt(ctx, loc.ID, asOf)
	if err != nil {
		return nil, err
	}
	findings := resolveFindings(g, events)

	res := plan.Compute(plan.Input{
		Geometry:     g,
		Blocks:       sv.Blocks,
		Material:     mv.Params,
		SafetyFactor: loc.SafetyFactor,
		Findings:     toObservations(findings),
		Now:          asOf,
	})

	basisSeq := int64(0)
	for _, f := range findings {
		if f.Event.Seq > basisSeq {
			basisSeq = f.Event.Seq
		}
	}

	p := model.Plan{
		LocationID:             loc.ID,
		AsOf:                   asOf,
		BasisEventSeq:          basisSeq,
		MaterialVersionID:      mv.ID,
		MaterialVersion:        mv.Version,
		SpectrumVersionID:      sv.ID,
		SpectrumVersion:        sv.Version,
		CurrentLengthMM:        g.ToReported(res.CurrentA) * 1000,
		CurrentIsBound:         res.CurrentIsBound,
		CalibratedC:            res.CalibratedC,
		CatalogC:               res.CatalogC,
		CWasCalibrated:         res.CWasCalibrated,
		CriticalLengthMM:       g.ToReported(res.CriticalA) * 1000,
		FailureMode:            res.FailureMode,
		RemainingCycles:        res.RemainingCycles,
		RemainingDays:          res.RemainingDays,
		SafetyFactor:           loc.SafetyFactor,
		NextInspectionAt:       res.NextInspection,
		InspectionIntervalDays: res.IntervalDays,
		NoFinding:              res.NoFinding,
	}
	return &projection{Location: loc, Plan: p, Events: events, Findings: findings,
		Material: *mv, Spectrum: *sv}, nil
}
