package recordlog_test

import (
	"sync"
	"testing"
	"time"

	"crackstation/internal/geometry"
	"crackstation/internal/store"
)

// Backfilling an older-dated record must give the same current plan as if
// all records had been entered in date order.
func TestBackfillMatchesChronologicalEntry(t *testing.T) {
	dates := []time.Time{
		time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2024, 8, 1, 0, 0, 0, 0, time.UTC),
	}
	lengths := []float64{0.003, 0.0042, 0.006}
	limits := []float64{0.0025, 0.0025, 0.0025}

	// Run A: chronological, including an NF before the first found.
	a := newFixture(t)
	locA, _ := a.createLocation(geometry.Edge, 0.3)
	a.submitNF(locA.ID, time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC), limits[0])
	for i := range dates {
		a.clockAdd(time.Hour)
		a.submitFound(locA.ID, dates[i], lengths[i], "A")
	}
	resA, err := a.svc.Current(a.ctx, locA.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Run B: submit the found records in order first, then backfill the NF
	// (older inspection date) last.
	b := newFixture(t)
	locB, _ := b.createLocation(geometry.Edge, 0.3)
	for i := range dates {
		b.clockAdd(time.Hour)
		b.submitFound(locB.ID, dates[i], lengths[i], "A")
	}
	b.clockAdd(time.Hour)
	b.submitNF(locB.ID, time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC), limits[0])
	resB, err := b.svc.Current(b.ctx, locB.ID)
	if err != nil {
		t.Fatal(err)
	}

	if !eqFloat(resA.FittedC, resB.FittedC, 1e-12) {
		t.Fatalf("fitted C differs: chron %.12e vs backfill %.12e", resA.FittedC, resB.FittedC)
	}
	if !resA.Plan.NextDate.Equal(resB.Plan.NextDate) {
		t.Fatalf("next date differs: %v vs %v", resA.Plan.NextDate, resB.Plan.NextDate)
	}
	if !eqFloat(resA.Plan.DaysToCritical, resB.Plan.DaysToCritical, 1e-9) {
		t.Fatalf("life differs: %v vs %v", resA.Plan.DaysToCritical, resB.Plan.DaysToCritical)
	}
}

func eqFloat(a, b, rel float64) bool {
	den := b
	if den == 0 {
		den = 1
	}
	d := a - b
	if d < 0 {
		d = -d
	}
	return d/den <= rel
}

// Correction replaces a value; the old row is retained and the new plan uses
// the corrected length.
func TestCorrectionRetainsOldAndRecomputes(t *testing.T) {
	f := newFixture(t)
	loc, _ := f.createLocation(geometry.Edge, 0.3)
	d := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	wrong := f.submitFound(loc.ID, d, 0.004, "A")
	r1, _ := f.svc.Current(f.ctx, loc.ID)

	f.clockAdd(time.Hour)
	corr, _, err := f.svc.Correct(f.ctx, store.NewRecordInput{
		LocationID: loc.ID, InspectDate: d, Method: "MT", Found: true,
		CrackLengthM: ptrFloat(0.005), Inspector: "A",
		SupersedesID: &wrong.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if corr.LogicalID != wrong.LogicalID {
		t.Fatalf("logical id changed: %d vs %d", corr.LogicalID, wrong.LogicalID)
	}
	r2, _ := f.svc.Current(f.ctx, loc.ID)
	// Longer measured crack -> shorter remaining life.
	if !(r2.Plan.DaysToCritical < r1.Plan.DaysToCritical) {
		t.Fatalf("life did not shorten: %v vs %v", r2.Plan.DaysToCritical, r1.Plan.DaysToCritical)
	}
	// Old record retained but inactive.
	hist, err := f.rep.ListRecordHistory(f.ctx, loc.ID)
	if err != nil || len(hist) != 2 {
		t.Fatalf("history len=%d err=%v", len(hist), err)
	}
	active, err := f.rep.ListActiveRecords(f.ctx, loc.ID)
	if err != nil || len(active) != 1 {
		t.Fatalf("active len=%d err=%v", len(active), err)
	}
	if *active[0].CrackLengthM != 0.005 {
		t.Fatalf("active length=%v", *active[0].CrackLengthM)
	}
}

// As-of replay reflects only the data existing at that instant.
func TestHistoryDateQuery(t *testing.T) {
	f := newFixture(t)
	loc, _ := f.createLocation(geometry.Edge, 0.3)
	d1 := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	d2 := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	f.submitFound(loc.ID, d1, 0.003, "A")
	t1 := f.currentTime().Add(time.Minute) // just after the first record existed
	f.clockAdd(24 * time.Hour)
	f.submitFound(loc.ID, d2, 0.006, "A")

	// As of t1: only one found record exists -> prior C, uncalibrated.
	then, err := f.svc.AsOf(f.ctx, loc.ID, t1)
	if err != nil {
		t.Fatal(err)
	}
	if then.FoundCount != 1 || then.CoefficientCalibrated {
		t.Fatalf("as-of state wrong: found=%d calibrated=%v", then.FoundCount, then.CoefficientCalibrated)
	}
	now2, err := f.svc.Current(f.ctx, loc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !now2.CoefficientCalibrated || now2.FoundCount != 2 {
		t.Fatalf("current state wrong: found=%d calibrated=%v", now2.FoundCount, now2.CoefficientCalibrated)
	}
	cmp, err := f.svc.CompareAt(f.ctx, loc.ID, t1)
	if err != nil {
		t.Fatal(err)
	}
	if cmp.ThenRecordSeq != 1 {
		t.Fatalf("then seq=%d", cmp.ThenRecordSeq)
	}
}

// A stricter material (lower KIC, lower yield) moves inspection earlier.
func TestMaterialUpdateImpact(t *testing.T) {
	f := newFixture(t)
	loc, matv := f.createLocation(geometry.Edge, 0.3)
	d1 := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	d2 := time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC)
	f.submitFound(loc.ID, d1, 0.003, "A")
	f.clockAdd(time.Hour)
	f.submitFound(loc.ID, d2, 0.004, "A")
	before, _ := f.svc.Current(f.ctx, loc.ID)

	nv, impacts, err := f.svc.AddMaterialVersion(f.ctx, store.MaterialVersion{
		MaterialID: matv.MaterialID, ParisM: 3, ParisC: matv.ParisC,
		FractureKIC: matv.FractureKIC / 2, YieldStrength: matv.YieldStrength / 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if nv.Version != 2 {
		t.Fatalf("version=%d", nv.Version)
	}
	found := false
	for _, im := range impacts {
		if im.LocationID == loc.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("location not listed among earlier inspections; before next=%v impacts=%+v", before.Plan.NextDate, impacts)
	}
	after, _ := f.svc.Current(f.ctx, loc.ID)
	if !after.Plan.NextDate.Before(before.Plan.NextDate) {
		t.Fatalf("next date did not move earlier: %v vs %v", after.Plan.NextDate, before.Plan.NextDate)
	}
}

// Two inspectors submitting concurrently: both rows retained; final plan
// equals a sequential date-ordered processing of the same records.
func TestConcurrentSubmissions(t *testing.T) {
	f := newFixture(t)
	loc, _ := f.createLocation(geometry.Edge, 0.3)
	d0 := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	f.submitFound(loc.ID, d0, 0.003, "A")

	d1 := time.Date(2024, 4, 1, 0, 0, 0, 0, time.UTC)
	d2 := time.Date(2024, 7, 1, 0, 0, 0, 0, time.UTC)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		f.clockAdd(time.Hour)
		_, _, err := f.svc.Submit(f.ctx, store.NewRecordInput{
			LocationID: loc.ID, InspectDate: d1, Method: "MT", Found: true,
			CrackLengthM: ptrFloat(0.004), Inspector: "A",
		})
		if err != nil {
			t.Error(err)
		}
	}()
	go func() {
		defer wg.Done()
		f.clockAdd(2 * time.Hour)
		_, _, err := f.svc.Submit(f.ctx, store.NewRecordInput{
			LocationID: loc.ID, InspectDate: d2, Method: "UT", Found: true,
			CrackLengthM: ptrFloat(0.0055), Inspector: "B",
		})
		if err != nil {
			t.Error(err)
		}
	}()
	wg.Wait()

	active, err := f.rep.ListActiveRecords(f.ctx, loc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 3 {
		t.Fatalf("active records=%d, want 3", len(active))
	}

	// Sequential reference with the same three dates/lengths.
	g := newFixture(t)
	locG, _ := g.createLocation(geometry.Edge, 0.3)
	g.submitFound(locG.ID, d0, 0.003, "A")
	g.clockAdd(time.Hour)
	g.submitFound(locG.ID, d1, 0.004, "A")
	g.clockAdd(time.Hour)
	g.submitFound(locG.ID, d2, 0.0055, "B")
	rf, _ := f.svc.Current(f.ctx, loc.ID)
	rg, _ := g.svc.Current(g.ctx, locG.ID)
	if !eqFloat(rf.FittedC, rg.FittedC, 1e-12) {
		t.Fatalf("fitted C concurrent %.10e != sequential %.10e", rf.FittedC, rg.FittedC)
	}
	if !rf.Plan.NextDate.Equal(rg.Plan.NextDate) {
		t.Fatalf("next date concurrent %v != sequential %v", rf.Plan.NextDate, rg.Plan.NextDate)
	}
}
