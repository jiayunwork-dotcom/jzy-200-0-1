package store

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"crackwatch/internal/domain/material"
	"crackwatch/internal/domain/spectrum"
	"crackwatch/internal/model"
)

// pgTestStore opens a PostgreSQL store when CRACKWATCH_TEST_DSN is set
// (the compose stack provides postgres://crackwatch:crackwatch@localhost:5432/crackwatch?sslmode=disable).
// Without it the store-level integration tests are skipped; the domain and
// application logic is covered by the in-memory store tests.
func pgTestStore(t *testing.T) *Postgres {
	t.Helper()
	dsn := os.Getenv("CRACKWATCH_TEST_DSN")
	if dsn == "" {
		t.Skip("set CRACKWATCH_TEST_DSN to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	st, err := OpenPostgres(ctx, dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	t.Cleanup(st.Close)
	return st
}

func TestPostgresRoundTrip(t *testing.T) {
	st := pgTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	uid := "mat_it_" + time.Now().Format("150405.000000")

	m := &model.Material{ID: uid, Grade: "IT-steel", CreatedAt: now}
	if err := st.CreateMaterial(ctx, m); err != nil {
		t.Fatal(err)
	}
	mv := model.MaterialVersion{
		ID: "mv_" + uid, MaterialID: uid,
		Params:      material.Params{Grade: "IT-steel", K1c: 330, Sy: 345, M: 3, C: 1e-11},
		EffectiveAt: now.AddDate(-5, 0, 0), CreatedAt: now,
	}
	if err := st.WithLocationTx(ctx, "material:"+uid, func(tx Tx) error {
		return tx.CreateMaterialVersion(ctx, &mv)
	}); err != nil {
		t.Fatal(err)
	}
	got, err := st.MaterialVersionAt(ctx, uid, now)
	if err != nil || got.Version != 1 || got.Params.K1c != 330 {
		t.Fatalf("round trip: %+v err %v", got, err)
	}
}

func TestPostgresLocationSpectrumAndEvents(t *testing.T) {
	st := pgTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	suf := time.Now().Format("150405.000000")
	matID := "mat_l_" + suf
	locID := "loc_l_" + suf

	m := &model.Material{ID: matID, Grade: "IT", CreatedAt: now}
	mv := &model.MaterialVersion{
		ID: "mv_" + suf, MaterialID: matID, Version: 1,
		Params:      material.Params{Grade: "IT", K1c: 330, Sy: 345, M: 3, C: 1e-11},
		EffectiveAt: now.AddDate(-5, 0, 0), CreatedAt: now,
	}
	loc := &model.Location{
		ID: locID, Name: "IT-loc", Geometry: "edge_crack", WidthMM: 300,
		MaterialID: matID, SafetyFactor: 2, CommissionedAt: now.AddDate(-2, 0, 0),
		CreatedAt: now, UpdatedAt: now,
	}
	sp := &model.SpectrumVersion{
		ID: "sp_" + suf, LocationID: locID, Version: 1,
		Blocks:      []spectrum.Block{{StressAmp: 100, StressMax: 150, CyclesPerDay: 500}},
		EffectiveAt: now.AddDate(-2, 0, 0), CreatedAt: now,
	}
	err := st.WithLocationTx(ctx, locID, func(tx Tx) error {
		if err := tx.CreateMaterial(ctx, m); err != nil {
			return err
		}
		if err := tx.CreateMaterialVersion(ctx, mv); err != nil {
			return err
		}
		if err := tx.CreateLocation(ctx, loc); err != nil {
			return err
		}
		return tx.CreateSpectrumVersion(ctx, sp)
	})
	if err != nil {
		t.Fatal(err)
	}

	// Two concurrent transactions on the same location must both append and
	// get consecutive sequence numbers.
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- st.WithLocationTx(ctx, locID, func(tx Tx) error {
				ev := &model.InspectionEvent{
					ID: "ev_" + suf + "_" + string(rune('a'+i)), LocationID: locID,
					InspectedAt: now.AddDate(0, 0, i+1), Method: "MT",
					Kind: "found", LengthMM: float64(5 + i), RecordedAt: now,
				}
				_, e := tx.AppendEvent(ctx, ev)
				return e
			})
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	events, err := st.ListEvents(ctx, locID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Seq != 1 || events[1].Seq != 2 {
		t.Fatalf("events = %+v, want two with seq 1,2", events)
	}

	// Historical cut: events recorded strictly before a later instant.
	known, err := st.EventsKnownAt(ctx, locID, now.Add(-time.Hour))
	if err != nil || len(known) != 0 {
		t.Fatalf("expected zero events before recording, got %+v err %v", known, err)
	}
	known, _ = st.EventsKnownAt(ctx, locID, now)
	if len(known) != 2 {
		t.Fatalf("expected both events known at now, got %d", len(known))
	}

	// Plan snapshot upsert and read.
	snap := &model.PlanSnapshot{
		Plan: model.Plan{
			LocationID: locID, AsOf: now, BasisEventSeq: 2,
			MaterialVersionID: mv.ID, MaterialVersion: 1,
			SpectrumVersionID: sp.ID, SpectrumVersion: 1,
		},
		ComputedAt: now,
	}
	if err := st.WithLocationTx(ctx, locID, func(tx Tx) error {
		return tx.SavePlan(ctx, snap)
	}); err != nil {
		t.Fatal(err)
	}
	read, err := st.GetPlan(ctx, locID, 2, mv.ID, sp.ID)
	if err != nil || read.BasisEventSeq != 2 {
		t.Fatalf("plan read = %+v err %v", read, err)
	}
}
