package store_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"crackstation/internal/geometry"
	"crackstation/internal/spectrum"
	"crackstation/internal/store"
)

// These tests run only when CRACK_TEST_DSN points at a PostgreSQL 16
// server. The package uses a single scratch database (created lazily on
// first use); each test truncates it. Parallel package binaries get their
// own database name.
func pgRepo(t *testing.T) (*store.Postgres, func()) {
	adminDSN := os.Getenv("CRACK_TEST_DSN")
	if adminDSN == "" {
		t.Skip("set CRACK_TEST_DSN to run PostgreSQL-backed repository tests")
	}
	const dbName = "crackstation_storetest"
	testDSN := strings.Replace(adminDSN, "crackstation", dbName, 1)
	ctx := context.Background()
	pg, err := store.OpenPostgres(testDSN)
	if err != nil {
		admin, aerr := store.OpenPostgres(adminDSN)
		if aerr != nil {
			t.Fatalf("open admin: %v (open test: %v)", aerr, err)
		}
		if err := admin.Ping(ctx); err != nil {
			t.Fatalf("ping admin: %v", err)
		}
		if _, err := admin.DB().ExecContext(ctx, "DROP DATABASE IF EXISTS "+dbName); err != nil {
			t.Fatalf("drop db: %v", err)
		}
		if _, err := admin.DB().ExecContext(ctx, "CREATE DATABASE "+dbName); err != nil {
			t.Fatalf("create db: %v", err)
		}
		admin.DB().Close()
		pg, err = store.OpenPostgres(testDSN)
		if err != nil {
			t.Fatalf("open test db: %v", err)
		}
		if err := pg.ExecScript(ctx, migrationSQL()); err != nil {
			t.Fatalf("migrate test db: %v", err)
		}
	}
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
	return pg, func() { pg.DB().Close() }
}

func migrationSQL() string {
	raw, err := os.ReadFile("../../db/migrations/0001_init.sql")
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func TestPostgresRecordVersioningAndCorrection(t *testing.T) {
	pg, done := pgRepo(t)
	defer done()
	ctx := context.Background()

	comm := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	mat, mv, err := pg.CreateMaterial(ctx, store.Material{Grade: "PG-Q345"},
		store.MaterialVersion{ParisM: 3, ParisC: 1e-11, FractureKIC: 80, YieldStrength: 345})
	if err != nil {
		t.Fatal(err)
	}
	loc, err := pg.CreateLocation(ctx, store.Location{
		Name: "pg loc", Geometry: geometry.Edge, WidthM: 0.3,
		MaterialID: mat.ID, CommissionedDate: comm, SafetyFactor: 2})
	if err != nil {
		t.Fatal(err)
	}
	_, err = pg.AddSpectrumRevision(ctx, store.SpectrumRevision{LocationID: loc.ID,
		Blocks: []spectrum.Block{{StressAmp: 70, MaxStress: 100, CyclesPerDay: 300}}})
	if err != nil {
		t.Fatal(err)
	}

	l := 0.003
	r1, v1, err := pg.AppendRecord(ctx, store.NewRecordInput{
		LocationID: loc.ID, InspectDate: comm.AddDate(0, 1, 0),
		Method: "MT", Found: true, CrackLengthM: &l, Inspector: "A"})
	if err != nil {
		t.Fatal(err)
	}
	if r1.LogicalID != r1.ID || v1 == 0 {
		t.Fatalf("logical/version wrong: %+v ver=%d", r1, v1)
	}

	l2 := 0.004
	_, v2, err := pg.AppendRecord(ctx, store.NewRecordInput{
		LocationID: loc.ID, InspectDate: comm.AddDate(0, 4, 0),
		Method: "MT", Found: true, CrackLengthM: &l2, Inspector: "A"})
	if err != nil {
		t.Fatal(err)
	}
	if v2 <= v1 {
		t.Fatalf("version did not advance: %d -> %d", v1, v2)
	}

	// Correct r1: new row inherits logical id, old row gets superseded_by.
	l1c := 0.0035
	cr, _, err := pg.AppendRecord(ctx, store.NewRecordInput{
		LocationID: loc.ID, InspectDate: comm.AddDate(0, 1, 0),
		Method: "MT", Found: true, CrackLengthM: &l1c, Inspector: "A",
		SupersedesID: &r1.ID})
	if err != nil {
		t.Fatal(err)
	}
	if cr.LogicalID != r1.LogicalID {
		t.Fatalf("correction changed logical id: %d vs %d", cr.LogicalID, r1.LogicalID)
	}
	old, err := pg.GetRecord(ctx, r1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if old.SupersededBy == nil || *old.SupersededBy != cr.ID {
		t.Fatalf("old row not linked: %+v", old)
	}

	active, err := pg.ListActiveRecords(ctx, loc.ID)
	if err != nil || len(active) != 2 {
		t.Fatalf("active=%d err=%v", len(active), err)
	}
	hist, err := pg.ListRecordHistory(ctx, loc.ID)
	if err != nil || len(hist) != 3 {
		t.Fatalf("history=%d err=%v", len(hist), err)
	}
	cur, err := pg.CurrentMaterialVersion(ctx, mat.ID)
	if err != nil || cur.ID != mv.ID {
		t.Fatalf("current material version %+v err=%v", cur, err)
	}
}

func TestPostgresNotFound(t *testing.T) {
	pg, done := pgRepo(t)
	defer done()
	ctx := context.Background()
	if _, err := pg.GetLocation(ctx, 999999); err == nil {
		t.Fatal("expected not found")
	} else if _, ok := err.(store.ErrNotFound); !ok {
		t.Fatalf("wrong error type %T", err)
	}
}
