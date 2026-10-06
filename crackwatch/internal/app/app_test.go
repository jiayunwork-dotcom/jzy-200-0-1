package app

import (
	"context"
	"testing"
	"time"

	"crackwatch/internal/domain/material"
	"crackwatch/internal/domain/spectrum"
	"crackwatch/internal/model"
	"crackwatch/internal/store"
)

func newTestService(t *testing.T) (*Service, *store.Memory, model.Location, model.MaterialVersion) {
	t.Helper()
	st := store.NewMemory()
	svc := New(st)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var clock time.Time = now
	svc.SetClock(func() time.Time { return clock })
	svc.tick = func() { clock = clock.Add(time.Minute) }

	ctx := context.Background()
	m, mv, err := svc.CreateMaterial(ctx, CreateMaterialInput{
		Grade: "Q345-test",
		Params: material.Params{
			Grade: "Q345-test", K1c: 330, Sy: 345, M: 3, C: 1e-11,
		},
		EffectiveAt: time.Time{},
	})
	if err != nil {
		t.Fatalf("create material: %v", err)
	}
	loc, _, err := svc.CreateLocation(ctx, LocationInput{
		Name:           "crane-01 boom joint A",
		Geometry:       "edge_crack",
		WidthMM:        300,
		MaterialID:     m.ID,
		SafetyFactor:   2,
		CommissionedAt: now.AddDate(-2, 0, 0),
		Blocks: []spectrum.Block{
			{StressAmp: 100, StressMax: 150, CyclesPerDay: 3000},
		},
	})
	if err != nil {
		t.Fatalf("create location: %v", err)
	}
	return svc, st, *loc, *mv
}

func found(at time.Time, mm float64) RecordInput {
	return RecordInput{InspectedAt: at, Method: "MT", Kind: model.ResultFound, LengthMM: mm}
}

func ndf(at time.Time, limit float64) RecordInput {
	return RecordInput{InspectedAt: at, Method: "UT", Kind: model.ResultNotDetected, DetectLimitMM: limit}
}

func TestCalibrationDrivesPlan(t *testing.T) {
	svc, _, loc, _ := newTestService(t)
	ctx := context.Background()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	if _, _, err := svc.SubmitResult(ctx, loc.ID, found(t0, 5)); err != nil {
		t.Fatal(err)
	}
	svc.tick()
	if _, _, err := svc.SubmitResult(ctx, loc.ID, found(t0.AddDate(0, 0, 30), 10)); err != nil {
		t.Fatal(err)
	}
	svc.tick()
	if _, _, err := svc.SubmitResult(ctx, loc.ID, found(t0.AddDate(0, 0, 60), 15)); err != nil {
		t.Fatal(err)
	}

	p, err := svc.CurrentPlan(ctx, loc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Plan.CWasCalibrated {
		t.Fatal("plan should use the site-calibrated C")
	}
	if p.Plan.CalibratedC <= 0 {
		t.Fatalf("calibrated C must be positive, got %.3e", p.Plan.CalibratedC)
	}
	if p.Plan.NextInspectionAt.IsZero() {
		t.Fatal("expected a scheduled next inspection")
	}
	if p.Plan.FailureMode == "" {
		t.Fatal("expected a failure mode")
	}
}

// TestBackfillEqualsChronological is the central order-independence
// requirement: entering an earlier record late must produce exactly the plan
// that entering everything in inspected-at order would have.
func TestBackfillEqualsChronological(t *testing.T) {
	ctx := context.Background()

	// Scenario A: chronological entry.
	svcA, _, locA, _ := newTestService(t)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	submit := func(svc *Service, id string, in RecordInput) {
		t.Helper()
		svc.tick()
		if _, _, err := svc.SubmitResult(ctx, id, in); err != nil {
			t.Fatalf("submit: %v", err)
		}
	}
	submit(svcA, locA.ID, found(t0, 5))
	submit(svcA, locA.ID, found(t0.AddDate(0, 0, 20), 9))
	submit(svcA, locA.ID, found(t0.AddDate(0, 0, 50), 14))
	planA, _ := svcA.CurrentPlan(ctx, locA.ID)

	// Scenario B: late backfill of the day-20 record after day 50 is known.
	svcB, _, locB, _ := newTestService(t)
	submit(svcB, locB.ID, found(t0, 5))
	submit(svcB, locB.ID, found(t0.AddDate(0, 0, 50), 14))
	submit(svcB, locB.ID, found(t0.AddDate(0, 0, 20), 9)) // backfilled
	planB, _ := svcB.CurrentPlan(ctx, locB.ID)

	assertPlansEqual(t, planA.Plan, planB.Plan)
	if len(planA.Findings) != 3 || len(planB.Findings) != 3 {
		t.Fatal("both scenarios must retain all three findings")
	}
}

// TestCorrectionsBackfillOrderIndependent mixes a correction with backfill.
func TestCorrectionsBackfillOrderIndependent(t *testing.T) {
	ctx := context.Background()
	svcA, _, locA, _ := newTestService(t)
	svcB, _, locB, _ := newTestService(t)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// A: record 5mm day0, 9mm day20, 14mm day50, then correct day20 to 11mm.
	_, p1, _ := svcA.SubmitResult(ctx, locA.ID, found(t0, 5))
	_ = p1
	svcA.tick()
	e20, _, _ := svcA.SubmitResult(ctx, locA.ID, found(t0.AddDate(0, 0, 20), 9))
	svcA.tick()
	svcA.SubmitResult(ctx, locA.ID, found(t0.AddDate(0, 0, 50), 14))
	svcA.tick()
	svcA.CorrectResult(ctx, locA.ID, e20.ID, found(t0.AddDate(0, 0, 20), 11))
	planA, _ := svcA.CurrentPlan(ctx, locA.ID)

	// B: same but the day-20 record was entered wrong-late, then corrected,
	// and the day-50 record is backfilled after the correction.
	_, _, _ = svcB.SubmitResult(ctx, locB.ID, found(t0, 5))
	svcB.tick()
	e20b, _, _ := svcB.SubmitResult(ctx, locB.ID, found(t0.AddDate(0, 0, 20), 9))
	svcB.tick()
	svcB.CorrectResult(ctx, locB.ID, e20b.ID, found(t0.AddDate(0, 0, 20), 11))
	svcB.tick()
	svcB.SubmitResult(ctx, locB.ID, found(t0.AddDate(0, 0, 50), 14)) // backfilled
	planB, _ := svcB.CurrentPlan(ctx, locB.ID)

	assertPlansEqual(t, planA.Plan, planB.Plan)
}

// TestHistoricalPlanAsOf reconstructs what was known at past instants.
func TestHistoricalPlanAsOf(t *testing.T) {
	svc, st, loc, _ := newTestService(t)
	ctx := context.Background()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	svc.tick()
	recorded1 := svc.Now()
	svc.SubmitResult(ctx, loc.ID, found(t0, 5))

	svc.tick()
	recorded2 := svc.Now()
	svc.SubmitResult(ctx, loc.ID, found(t0.AddDate(0, 0, 30), 10))

	svc.tick()
	svc.SubmitResult(ctx, loc.ID, found(t0.AddDate(0, 0, 60), 20))
	now := svc.Now()

	// As of just after the first record: exactly one finding, catalog C.
	at1, err := svc.PlanAt(ctx, loc.ID, recorded1)
	if err != nil {
		t.Fatal(err)
	}
	if len(at1.Findings) != 1 {
		t.Fatalf("at1 findings = %d, want 1", len(at1.Findings))
	}
	if at1.Plan.CWasCalibrated {
		t.Fatal("a single finding must not calibrate C")
	}
	if at1.Plan.BasisEventSeq != 1 {
		t.Fatalf("basis seq = %d, want 1", at1.Plan.BasisEventSeq)
	}

	// As of just after the second record: calibrated.
	at2, err := svc.PlanAt(ctx, loc.ID, recorded2)
	if err != nil {
		t.Fatal(err)
	}
	if len(at2.Findings) != 2 || !at2.Plan.CWasCalibrated {
		t.Fatalf("at2: findings=%d calibrated=%v", len(at2.Findings), at2.Plan.CWasCalibrated)
	}

	// The as-of plans are stable even though newer data exists now.
	current, _ := svc.CurrentPlan(ctx, loc.ID)
	if current.Plan.CalibratedC == at1.Plan.CalibratedC {
		t.Fatal("current and oldest-as-of plans should differ")
	}

	cmp, err := svc.ComparePlan(ctx, loc.ID, recorded1)
	if err != nil {
		t.Fatal(err)
	}
	if cmp.Historical.BasisEventSeq != 1 || cmp.Current.BasisEventSeq != 3 {
		t.Fatalf("comparison basis seqs %d vs %d", cmp.Historical.BasisEventSeq, cmp.Current.BasisEventSeq)
	}

	// Stored snapshots survive and are listable.
	snaps, err := svc.History(ctx, loc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 3 {
		t.Fatalf("stored snapshots = %d, want 3", len(snaps))
	}
	_ = now
	_ = st
}

// TestConcurrentSubmissions serialises two inspectors posting to one site and
// verifies both records survive and the final plan equals sequential entry.
func TestConcurrentSubmissions(t *testing.T) {
	ctx := context.Background()
	svc, _, loc, _ := newTestService(t)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	svc.tick()
	svc.SubmitResult(ctx, loc.ID, found(t0, 5))

	done := make(chan error, 2)
	go func() {
		_, _, e := svc.SubmitResult(ctx, loc.ID, found(t0.AddDate(0, 0, 20), 9))
		done <- e
	}()
	go func() {
		_, _, e := svc.SubmitResult(ctx, loc.ID, found(t0.AddDate(0, 0, 40), 14))
		done <- e
	}()
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	if e := <-done; e != nil {
		t.Fatal(e)
	}

	events, _ := svc.ListEvents(ctx, loc.ID)
	if len(events) != 3 {
		t.Fatalf("events = %d, want 3 (both submissions retained)", len(events))
	}
	p, _ := svc.CurrentPlan(ctx, loc.ID)
	if len(p.Findings) != 3 {
		t.Fatalf("findings = %d, want 3", len(p.Findings))
	}

	// Reference: the same three findings entered sequentially.
	svcR, _, locR, _ := newTestService(t)
	svcR.SubmitResult(ctx, locR.ID, found(t0, 5))
	svcR.tick()
	svcR.SubmitResult(ctx, locR.ID, found(t0.AddDate(0, 0, 20), 9))
	svcR.tick()
	svcR.SubmitResult(ctx, locR.ID, found(t0.AddDate(0, 0, 40), 14))
	pRef, _ := svcR.CurrentPlan(ctx, locR.ID)
	assertPlansEqual(t, pRef.Plan, p.Plan)
}

// TestMaterialUpdateImpact lists locations whose next inspection moves
// earlier when the grade parameters change, and later-moving sites are not
// listed. A single finding is used so the catalog C (rather than a calibrated
// C) governs the plan and reacts to the material update.
func TestMaterialUpdateImpact(t *testing.T) {
	ctx := context.Background()
	svc, _, loc, _ := newTestService(t)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	svc.tick()
	svc.SubmitResult(ctx, loc.ID, found(t0, 5))
	before, _ := svc.CurrentPlan(ctx, loc.ID)
	if before.Plan.CWasCalibrated {
		t.Fatal("precondition: a single finding must keep the catalog C")
	}
	if before.Plan.NoFinding || before.Plan.NextInspectionAt.IsZero() {
		t.Fatal("precondition: scheduled plan expected")
	}

	// Larger C (faster growth) must bring the inspection forward.
	svc.tick()
	mat, _ := svc.GetMaterial(ctx, loc.MaterialID)
	newParams := material.Params{Grade: mat.Grade, K1c: 330, Sy: 345, M: 3, C: 1e-10}
	v2, impact, err := svc.AddMaterialVersion(ctx, loc.MaterialID, newParams, svc.Now())
	if err != nil {
		t.Fatal(err)
	}
	if v2.Version != 2 {
		t.Fatalf("material version = %d, want 2", v2.Version)
	}
	if len(impact) != 1 || impact[0].LocationID != loc.ID || impact[0].EarlierDays <= 0 {
		t.Fatalf("impact = %+v, want one earlier entry for %s", impact, loc.ID)
	}
	after, _ := svc.CurrentPlan(ctx, loc.ID)
	if !after.Plan.NextInspectionAt.Before(before.Plan.NextInspectionAt) {
		t.Fatal("new plan should schedule earlier")
	}
	if after.Plan.MaterialVersion != 2 {
		t.Fatalf("plan should bind material version 2, got %d", after.Plan.MaterialVersion)
	}
}

func TestNotDetectedAcceptedAndBound(t *testing.T) {
	svc, _, loc, _ := newTestService(t)
	ctx := context.Background()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	svc.SubmitResult(ctx, loc.ID, found(t0, 5))
	svc.tick()
	_, p, err := svc.SubmitResult(ctx, loc.ID, ndf(t0.AddDate(0, 0, 10), 8))
	if err != nil {
		t.Fatalf("valid NDF rejected: %v", err)
	}
	if !p.Plan.CurrentIsBound {
		t.Fatal("last effective observation is an upper bound")
	}
}

func TestValidationRejections(t *testing.T) {
	svc, _, loc, _ := newTestService(t)
	ctx := context.Background()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name  string
		in    RecordInput
		field string
	}{
		{"non-positive length", found(t0, 0), "length_mm"},
		{"length >= width", found(t0, 300), "length_mm"},
		{"negative not relevant for date", RecordInput{}, "inspected_at"},
		{"date before commissioning", found(loc.CommissionedAt.AddDate(0, 0, -1), 5), "inspected_at"},
		{"NDF without limit", ndf(t0, 0), "detect_limit_mm"},
		{"NDF limit >= width", ndf(t0, 300), "detect_limit_mm"},
		{"bad kind", RecordInput{InspectedAt: t0, Method: "X", Kind: "maybe", LengthMM: 5}, "kind"},
		{"missing method", RecordInput{InspectedAt: t0, Kind: model.ResultFound, LengthMM: 5}, "method"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := svc.SubmitResult(ctx, loc.ID, tc.in)
			fe, ok := AsFieldErrors(err)
			if !ok {
				t.Fatalf("expected field errors, got %v", err)
			}
			if !containsField(fe, tc.field) {
				t.Fatalf("errors %v do not name field %q", fe, tc.field)
			}
		})
	}
}

func TestSpectrumValidationRejections(t *testing.T) {
	svc, _, _, mat := newTestService(t)
	ctx := context.Background()
	base := func(blocks []spectrum.Block) LocationInput {
		return LocationInput{
			Name: "x", Geometry: "edge_crack", WidthMM: 300, MaterialID: mat.MaterialID,
			SafetyFactor: 2, CommissionedAt: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
			Blocks: blocks,
		}
	}
	cases := []struct {
		name   string
		blocks []spectrum.Block
		field  string
	}{
		{"negative amplitude", []spectrum.Block{{StressAmp: -1, StressMax: 100, CyclesPerDay: 1}}, "blocks[0].stress_amp"},
		{"amp over 2 max", []spectrum.Block{{StressAmp: 201, StressMax: 100, CyclesPerDay: 1}}, "blocks[0].stress_amp"},
		{"negative cycles", []spectrum.Block{{StressAmp: 100, StressMax: 100, CyclesPerDay: -1}}, "blocks[0].cycles_per_day"},
		{"no active block", []spectrum.Block{{StressAmp: 0, StressMax: 100, CyclesPerDay: 1}}, "load_spectrum"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := svc.CreateLocation(ctx, base(tc.blocks))
			fe, ok := AsFieldErrors(err)
			if !ok || !containsField(fe, tc.field) {
				t.Fatalf("err = %v, want field %q", err, tc.field)
			}
		})
	}
}

func TestVoidRemovesRecordFromPlan(t *testing.T) {
	svc, _, loc, _ := newTestService(t)
	ctx := context.Background()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	e1, _, _ := svc.SubmitResult(ctx, loc.ID, found(t0, 5))
	svc.tick()
	svc.SubmitResult(ctx, loc.ID, found(t0.AddDate(0, 0, 30), 10))
	p, _ := svc.CurrentPlan(ctx, loc.ID)
	if len(p.Findings) != 2 {
		t.Fatalf("findings = %d", len(p.Findings))
	}
	svc.tick()
	ev, p2, err := svc.VoidResult(ctx, loc.ID, e1.ID, "entered against wrong site")
	if err != nil {
		t.Fatal(err)
	}
	if ev.Kind != "void" {
		t.Fatal("void event kind")
	}
	if len(p2.Findings) != 1 {
		t.Fatalf("after void findings = %d, want 1", len(p2.Findings))
	}
	if p2.Plan.CWasCalibrated {
		t.Fatal("one finding cannot calibrate C after voiding the other")
	}
	// The old event is retained in the append-only store.
	events, _ := svc.ListEvents(ctx, loc.ID)
	if len(events) != 3 || events[0].ID != e1.ID {
		t.Fatalf("events = %v, old version must be retained", events)
	}
}

func containsField(fe FieldErrors, field string) bool {
	for _, f := range fe {
		if f.Field == field {
			return true
		}
	}
	return false
}

func assertPlansEqual(t *testing.T, a, b model.Plan) {
	t.Helper()
	if a.CalibratedC != b.CalibratedC ||
		a.CWasCalibrated != b.CWasCalibrated ||
		a.CurrentLengthMM != b.CurrentLengthMM ||
		a.CriticalLengthMM != b.CriticalLengthMM ||
		a.RemainingCycles != b.RemainingCycles ||
		a.FailureMode != b.FailureMode ||
		!a.NextInspectionAt.Equal(b.NextInspectionAt) ||
		a.InspectionIntervalDays != b.InspectionIntervalDays {
		t.Fatalf("plans differ:\nA: %+v\nB: %+v", a, b)
	}
}
