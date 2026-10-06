package app

import (
	"context"
	"time"

	"crackwatch/internal/model"
)

// CurrentPlan recomputes the plan for a location from all data known now.
func (s *Service) CurrentPlan(ctx context.Context, locID string) (*projection, error) {
	loc, err := s.st.GetLocation(ctx, locID)
	if err != nil {
		return nil, err
	}
	return project(ctx, s.st, *loc, s.now())
}

// PlanAt returns the plan exactly as it would have been computed just after
// asOf: only events with RecordedAt <= asOf and parameter versions effective
// at asOf are used.
func (s *Service) PlanAt(ctx context.Context, locID string, asOf time.Time) (*projection, error) {
	loc, err := s.st.GetLocation(ctx, locID)
	if err != nil {
		return nil, err
	}
	return project(ctx, s.st, *loc, asOf.UTC())
}

// PlanComparison holds the historical plan next to the current one.
type PlanComparison struct {
	At            time.Time  `json:"at"`
	Historical    model.Plan `json:"historical"`
	Current       model.Plan `json:"current"`
	NextDeltaDays float64    `json:"next_inspection_delta_days"` // current - historical
}

// ComparePlan returns the historical plan at asOf beside today's plan.
func (s *Service) ComparePlan(ctx context.Context, locID string, asOf time.Time) (*PlanComparison, error) {
	hist, err := s.PlanAt(ctx, locID, asOf)
	if err != nil {
		return nil, err
	}
	cur, err := s.CurrentPlan(ctx, locID)
	if err != nil {
		return nil, err
	}
	c := &PlanComparison{At: asOf.UTC(), Historical: hist.Plan, Current: cur.Plan}
	if !hist.Plan.NextInspectionAt.IsZero() && !cur.Plan.NextInspectionAt.IsZero() {
		c.NextDeltaDays = cur.Plan.NextInspectionAt.Sub(hist.Plan.NextInspectionAt).Hours() / 24
	}
	return c, nil
}

// History returns the stored plan snapshots for a location: every plan ever
// persisted, each bound to its record and parameter versions.
func (s *Service) History(ctx context.Context, locID string) ([]model.PlanSnapshot, error) {
	if _, err := s.st.GetLocation(ctx, locID); err != nil {
		return nil, err
	}
	return s.st.ListPlans(ctx, locID)
}
