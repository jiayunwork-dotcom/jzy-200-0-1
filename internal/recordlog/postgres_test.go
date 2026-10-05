package recordlog_test

// PostgreSQL-backed behavior tests. They share the acceptance scenarios with
// the in-memory suite but run every write/query through real SQL. Enabled by
// CRACK_TEST_DSN; skipped otherwise so plain `go test ./...` needs no DB.

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"crackstation/internal/geometry"
	"crackstation/internal/recordlog"
	"crackstation/internal/spectrum"
	"crackstation/internal/store"
)

func pgService(t *testing.T) (*recordlog.Service, *store.Postgres, context.Context) {
	adminDSN := os.Getenv("CRACK_TEST_DSN")
	if adminDSN == "" {
		t.Skip("set CRACK_TEST_DSN to run PostgreSQL-backed behavior tests")
	}
	const dbName = "crackstation_recordlogtest"
	testDSN := strings.Replace(adminDSN, "crackstation", dbName, 1)
	pg, err := store.OpenPostgres(testDSN)
	if err != nil {
		// First test in the package: create and migrate the scratch DB.
		admin, aerr := store.OpenPostgres(adminDSN)
		if aerr != nil {
			t.Fatalf("open admin: %v (open test: %v)", aerr, err)
		}
		ctx0 := context.Background()
		if err := admin.Ping(ctx0); err != nil {
			t.Fatalf("ping admin: %v", err)
		}
		if _, err := admin.DB().ExecContext(ctx0, "DROP DATABASE IF EXISTS "+dbName); err != nil {
			t.Fatalf("drop db: %v", err)
		}
		if _, err := admin.DB().ExecContext(ctx0, "CREATE DATABASE "+dbName); err != nil {
			t.Fatalf("create db: %v", err)
		}
		admin.DB().Close()
		raw, rerr := os.ReadFile("../../db/migrations/0001_init.sql")
		if rerr != nil {
			t.Fatal(rerr)
		}
		pg, err = store.OpenPostgres(testDSN)
		if err != nil {
			t.Fatalf("open test db: %v", err)
		}
		if err := pg.ExecScript(context.Background(), string(raw)); err != nil {
			t.Fatalf("migrate: %v", err)
		}
	}
	ctx := context.Background()
	if err := pg.Ping(ctx); err != nil {
		t.Fatalf("ping test db: %v", err)
	}
	for _, tbl := range []string{
		"plan_snapshots", "record_versions", "records", "spectrum_revisions",
		"locations", "material_versions", "materials",
	} {
		if _, err := pg.DB().ExecContext(ctx, "TRUNCATE "+tbl+" RESTART IDENTITY CASCADE"); err != nil {
			t.Fatalf("truncate %s: %v", tbl, err)
		}
	}
	svc := recordlog.NewService(pg)
	return svc, pg, ctx
}

type pgEnv struct {
	t    *testing.T
	svc  *recordlog.Service
	pg   *store.Postgres
	ctx  context.Context
	now  time.Time
	comm time.Time
}

func newPGEnv(t *testing.T) *pgEnv {
	svc, pg, ctx := pgService(t)
	now := time.Date(2025, 1, 1, 9, 0, 0, 0, time.UTC)
	e := &pgEnv{t: t, svc: svc, pg: pg, ctx: ctx, now: now,
		comm: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)}
	svc.SetClock(func() time.Time { return e.now })
	return e
}

func (e *pgEnv) advance(d time.Duration) { e.now = e.now.Add(d) }

func (e *pgEnv) seedLocation() (store.Location, store.MaterialVersion) {
	mat, mv, err := e.pg.CreateMaterial(e.ctx, store.Material{Grade: "PGQ", CreatedAt: e.comm},
		store.MaterialVersion{ParisM: 3, ParisC: 1e-11, FractureKIC: 80, YieldStrength: 345, CreatedAt: e.comm})
	if err != nil {
		e.t.Fatal(err)
	}
	loc, err := e.pg.CreateLocation(e.ctx, store.Location{
		Name: "jib", Geometry: geometry.Edge, WidthM: 0.3, MaterialID: mat.ID,
		CommissionedDate: e.comm, SafetyFactor: 2, CreatedAt: e.comm})
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.pg.AddSpectrumRevision(e.ctx, store.SpectrumRevision{
		LocationID: loc.ID,
		Blocks:     []spectrum.Block{{StressAmp: 70, MaxStress: 100, CyclesPerDay: 300}},
		CreatedAt:  e.comm,
	}); err != nil {
		e.t.Fatal(err)
	}
	return loc, mv
}

func (e *pgEnv) found(locID int64, date time.Time, l float64) store.Record {
	r, _, err := e.svc.Submit(e.ctx, store.NewRecordInput{
		LocationID: locID, InspectDate: date, Method: "MT", Found: true,
		CrackLengthM: ptrFloat(l), Inspector: "A"})
	if err != nil {
		e.t.Fatalf("found %v: %v", date, err)
	}
	return r
}

func (e *pgEnv) nf(locID int64, date time.Time, lim float64) store.Record {
	r, _, err := e.svc.Submit(e.ctx, store.NewRecordInput{
		LocationID: locID, InspectDate: date, Method: "UT", Found: false,
		DetectionLimitM: ptrFloat(lim), Inspector: "B"})
	if err != nil {
		e.t.Fatalf("nf %v: %v", date, err)
	}
	return r
}

func TestPGBackfillMatchesChronological(t *testing.T) {
	dates := []time.Time{
		time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2024, 8, 1, 0, 0, 0, 0, time.UTC),
	}
	lengths := []float64{0.003, 0.0042, 0.006}

	a := newPGEnv(t)
	locA, _ := a.seedLocation()
	a.nf(locA.ID, time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC), 0.0025)
	for i := range dates {
		a.advance(time.Hour)
		a.found(locA.ID, dates[i], lengths[i])
	}
	resA, err := a.svc.Current(a.ctx, locA.ID)
	if err != nil {
		t.Fatal(err)
	}

	b := newPGEnv(t)
	locB, _ := b.seedLocation()
	for i := range dates {
		b.advance(time.Hour)
		b.found(locB.ID, dates[i], lengths[i])
	}
	b.advance(time.Hour)
	b.nf(locB.ID, time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC), 0.0025)
	resB, err := b.svc.Current(b.ctx, locB.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resA.FittedC != resB.FittedC {
		t.Fatalf("C chron %.12e != backfill %.12e", resA.FittedC, resB.FittedC)
	}
	if !resA.Plan.NextDate.Equal(resB.Plan.NextDate) {
		t.Fatalf("next %v != %v", resA.Plan.NextDate, resB.Plan.NextDate)
	}
}

func TestPGCorrectionHistoryAndReplay(t *testing.T) {
	e := newPGEnv(t)
	loc, _ := e.seedLocation()
	d1 := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	d2 := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	e.found(loc.ID, d1, 0.003)
	t1 := e.now.Add(time.Minute)
	e.advance(24 * time.Hour)
	e.found(loc.ID, d2, 0.006)

	then, err := e.svc.AsOf(e.ctx, loc.ID, t1)
	if err != nil {
		t.Fatal(err)
	}
	if then.FoundCount != 1 || then.CoefficientCalibrated {
		t.Fatalf("as-of: found=%d calib=%v", then.FoundCount, then.CoefficientCalibrated)
	}
	cmp, err := e.svc.CompareAt(e.ctx, loc.ID, t1)
	if err != nil || cmp.ThenRecordSeq != 1 {
		t.Fatalf("compare: %+v err=%v", cmp, err)
	}

	// Correct the first record; as-of t1 must now see the corrected value
	// only if the correction existed then — it does not, so state unchanged.
	first, _ := e.pg.ListActiveRecords(e.ctx, loc.ID)
	e.advance(time.Hour)
	if _, _, err := e.svc.Correct(e.ctx, store.NewRecordInput{
		LocationID: loc.ID, InspectDate: d1, Method: "MT", Found: true,
		CrackLengthM: ptrFloat(0.0032), Inspector: "A", SupersedesID: &first[0].ID}); err != nil {
		t.Fatal(err)
	}
	hist, err := e.pg.ListRecordHistory(e.ctx, loc.ID)
	if err != nil || len(hist) != 3 {
		t.Fatalf("history=%d err=%v", len(hist), err)
	}
	then2, err := e.svc.AsOf(e.ctx, loc.ID, t1)
	if err != nil {
		t.Fatal(err)
	}
	if then2.FoundCount != 1 {
		t.Fatalf("as-of after correction found=%d", then2.FoundCount)
	}
}

func TestPGMaterialImpact(t *testing.T) {
	e := newPGEnv(t)
	loc, mv := e.seedLocation()
	e.found(loc.ID, time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC), 0.003)
	e.advance(time.Hour)
	e.found(loc.ID, time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC), 0.004)
	before, _ := e.svc.Current(e.ctx, loc.ID)
	e.advance(time.Hour)
	nv, impacts, err := e.svc.AddMaterialVersion(e.ctx, store.MaterialVersion{
		MaterialID: mv.MaterialID, ParisM: 3, ParisC: mv.ParisC,
		FractureKIC: mv.FractureKIC / 2, YieldStrength: mv.YieldStrength / 2})
	if err != nil {
		t.Fatal(err)
	}
	if nv.Version != 2 || len(impacts) != 1 || impacts[0].LocationID != loc.ID {
		t.Fatalf("nv=%+v impacts=%+v", nv, impacts)
	}
	after, _ := e.svc.Current(e.ctx, loc.ID)
	if !after.Plan.NextDate.Before(before.Plan.NextDate) {
		t.Fatalf("next did not move earlier: %v vs %v", after.Plan.NextDate, before.Plan.NextDate)
	}
}

func TestPGConcurrentSubmissions(t *testing.T) {
	e := newPGEnv(t)
	loc, _ := e.seedLocation()
	e.found(loc.ID, time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC), 0.003)
	d1 := time.Date(2024, 4, 1, 0, 0, 0, 0, time.UTC)
	d2 := time.Date(2024, 7, 1, 0, 0, 0, 0, time.UTC)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _, err := e.svc.Submit(e.ctx, store.NewRecordInput{
			LocationID: loc.ID, InspectDate: d1, Method: "MT", Found: true,
			CrackLengthM: ptrFloat(0.004), Inspector: "A"})
		errs <- err
	}()
	go func() {
		defer wg.Done()
		_, _, err := e.svc.Submit(e.ctx, store.NewRecordInput{
			LocationID: loc.ID, InspectDate: d2, Method: "UT", Found: true,
			CrackLengthM: ptrFloat(0.0055), Inspector: "B"})
		errs <- err
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	active, err := e.pg.ListActiveRecords(e.ctx, loc.ID)
	if err != nil || len(active) != 3 {
		t.Fatalf("active=%d err=%v", len(active), err)
	}
	// Version sequence must be gap-free 1,2,3.
	ver, err := e.pg.CurrentRecordVersion(e.ctx, loc.ID)
	if err != nil || ver != 3 {
		t.Fatalf("current version=%d err=%v", ver, err)
	}
}
