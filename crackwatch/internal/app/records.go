package app

import (
	"context"
	"time"

	"crackwatch/internal/model"
	"crackwatch/internal/store"
)

// RecordInput is a submitted inspection result.
type RecordInput struct {
	InspectedAt   time.Time
	Method        string
	Kind          model.ResultKind
	LengthMM      float64 // when Kind == found
	DetectLimitMM float64 // when Kind == not_detected
	Note          string
	Inspector     string
}

// validateRecord enforces the field-level acceptance rules.
func (s *Service) validateRecord(ctx context.Context, loc model.Location, in RecordInput) FieldErrors {
	var errs FieldErrors
	if in.InspectedAt.IsZero() {
		addField(&errs, "inspected_at", "must be provided")
	} else if in.InspectedAt.Before(loc.CommissionedAt) {
		addField(&errs, "inspected_at", "must not be earlier than the location commissioning date")
	}
	if in.Method == "" {
		addField(&errs, "method", "must not be empty")
	}
	switch in.Kind {
	case model.ResultFound:
		if in.LengthMM <= 0 {
			addField(&errs, "length_mm", "crack length must be positive")
		}
		if in.LengthMM >= loc.WidthMM {
			addField(&errs, "length_mm", "crack length must be smaller than the plate width")
		}
	case model.ResultNotDetected:
		if in.DetectLimitMM <= 0 {
			addField(&errs, "detect_limit_mm", "not-detected record requires the method detection minimum")
		}
		if in.DetectLimitMM >= loc.WidthMM {
			addField(&errs, "detect_limit_mm", "detection limit must be smaller than the plate width")
		}
	default:
		addField(&errs, "kind", "must be 'found' or 'not_detected'")
	}
	return errs
}

// SubmitResult appends a new inspection record and returns the stored event
// plus the recomputed current plan. Concurrent submissions for the same
// location are serialised on the per-location transaction lock, and since
// every derivation sorts by inspected_at, the outcome is identical to
// sequential entry.
func (s *Service) SubmitResult(ctx context.Context, locID string, in RecordInput) (*model.InspectionEvent, *projection, error) {
	var ev *model.InspectionEvent
	var proj *projection
	err := s.st.WithLocationTx(ctx, locID, func(tx store.Tx) error {
		loc, err := tx.GetLocation(ctx, locID)
		if err != nil {
			return err
		}
		if errs := s.validateRecord(ctx, *loc, in); len(errs) > 0 {
			return errs
		}
		ev = &model.InspectionEvent{
			ID: "ev_" + newID(), LocationID: locID,
			InspectedAt: in.InspectedAt.UTC(), Method: in.Method, Kind: in.Kind,
			LengthMM: in.LengthMM, DetectLimitMM: in.DetectLimitMM,
			Note: in.Note, Inspector: in.Inspector, RecordedAt: s.now(),
		}
		if ev.Seq, err = tx.AppendEvent(ctx, ev); err != nil {
			return err
		}
		proj, err = project(ctx, tx, *loc, s.now())
		if err != nil {
			return err
		}
		snap := &model.PlanSnapshot{Plan: proj.Plan, ComputedAt: s.now()}
		return tx.SavePlan(ctx, snap)
	})
	if err != nil {
		return nil, nil, err
	}
	return ev, proj, nil
}

// CorrectResult appends a corrected record that supersedes the named event
// (which must be an effective event of the location). The old event is
// retained. For a true deletion use VoidResult.
func (s *Service) CorrectResult(ctx context.Context, locID, supersedesID string, in RecordInput) (*model.InspectionEvent, *projection, error) {
	var ev *model.InspectionEvent
	var proj *projection
	err := s.st.WithLocationTx(ctx, locID, func(tx store.Tx) error {
		loc, err := tx.GetLocation(ctx, locID)
		if err != nil {
			return err
		}
		events, err := tx.ListEvents(ctx, locID)
		if err != nil {
			return err
		}
		if !isEffective(events, supersedesID) {
			add := FieldErrors{{Field: "supersedes_id", Reason: "target event not found or already superseded"}}
			return add
		}
		if errs := s.validateRecord(ctx, *loc, in); len(errs) > 0 {
			return errs
		}
		ev = &model.InspectionEvent{
			ID: "ev_" + newID(), LocationID: locID, SupersedesID: supersedesID,
			InspectedAt: in.InspectedAt.UTC(), Method: in.Method, Kind: in.Kind,
			LengthMM: in.LengthMM, DetectLimitMM: in.DetectLimitMM,
			Note: in.Note, Inspector: in.Inspector, RecordedAt: s.now(),
		}
		if ev.Seq, err = tx.AppendEvent(ctx, ev); err != nil {
			return err
		}
		proj, err = project(ctx, tx, *loc, s.now())
		if err != nil {
			return err
		}
		return tx.SavePlan(ctx, &model.PlanSnapshot{Plan: proj.Plan, ComputedAt: s.now()})
	})
	if err != nil {
		return nil, nil, err
	}
	return ev, proj, nil
}

// VoidResult appends a void event superseding the named event.
func (s *Service) VoidResult(ctx context.Context, locID, supersedesID, reason string) (*model.InspectionEvent, *projection, error) {
	var ev *model.InspectionEvent
	var proj *projection
	err := s.st.WithLocationTx(ctx, locID, func(tx store.Tx) error {
		loc, err := tx.GetLocation(ctx, locID)
		if err != nil {
			return err
		}
		events, err := tx.ListEvents(ctx, locID)
		if err != nil {
			return err
		}
		if !isEffective(events, supersedesID) {
			return FieldErrors{{Field: "supersedes_id", Reason: "target event not found or already superseded"}}
		}
		ev = &model.InspectionEvent{
			ID: "ev_" + newID(), LocationID: locID, SupersedesID: supersedesID,
			Kind: "void", Method: "void", Note: reason,
			Inspector: "system", RecordedAt: s.now(),
		}
		if ev.Seq, err = tx.AppendEvent(ctx, ev); err != nil {
			return err
		}
		proj, err = project(ctx, tx, *loc, s.now())
		if err != nil {
			return err
		}
		return tx.SavePlan(ctx, &model.PlanSnapshot{Plan: proj.Plan, ComputedAt: s.now()})
	})
	if err != nil {
		return nil, nil, err
	}
	return ev, proj, nil
}

// isEffective reports whether id names a live event: it exists and nothing
// currently supersedes it.
func isEffective(events []model.InspectionEvent, id string) bool {
	if id == "" {
		return false
	}
	found := false
	for _, e := range events {
		if e.ID == id {
			found = true
		}
		if e.SupersedesID == id {
			return false
		}
	}
	return found
}

// ListEvents returns the full append-only history of a location.
func (s *Service) ListEvents(ctx context.Context, locID string) ([]model.InspectionEvent, error) {
	return s.st.ListEvents(ctx, locID)
}
