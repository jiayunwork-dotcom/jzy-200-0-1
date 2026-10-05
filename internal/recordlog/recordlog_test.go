package recordlog_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"crackstation/internal/geometry"
	"crackstation/internal/recordlog"
	"crackstation/internal/spectrum"
	"crackstation/internal/store"
)

type fixture struct {
	t   *testing.T
	svc *recordlog.Service
	rep *store.Memory
	ctx context.Context
	mu  sync.Mutex
	now time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	rep := store.NewMemory()
	svc := recordlog.NewService(rep)
	now := time.Date(2025, 1, 1, 9, 0, 0, 0, time.UTC)
	f := &fixture{t: t, svc: svc, rep: rep, ctx: context.Background(), now: now}
	// The repository and the service share one simulated clock.
	rep.Clock = f.clock
	svc.SetClock(f.clock)
	return f
}

func (f *fixture) clock() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fixture) clockAdd(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	f.mu.Unlock()
}

func (f *fixture) currentTime() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fixture) createLocation(geo geometry.Type, width float64) (store.Location, store.MaterialVersion) {
	comm := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	mat, v, err := f.rep.CreateMaterial(f.ctx, store.Material{Grade: "Q345", CreatedAt: comm}, store.MaterialVersion{
		ParisM: 3, ParisC: 1e-11, FractureKIC: 80, YieldStrength: 345, CreatedAt: comm,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	loc, err := f.rep.CreateLocation(f.ctx, store.Location{
		Name: "jib weld #1", Crane: "MQ-01", Geometry: geo, WidthM: width,
		MaterialID: mat.ID, CommissionedDate: comm, SafetyFactor: 2, CreatedAt: comm,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	_, err = f.rep.AddSpectrumRevision(f.ctx, store.SpectrumRevision{
		LocationID: loc.ID,
		Blocks:     []spectrum.Block{{StressAmp: 70, MaxStress: 100, CyclesPerDay: 300}},
		CreatedAt:  comm,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return loc, v
}

func ptrFloat(x float64) *float64 { return &x }

func (f *fixture) submitFound(locID int64, date time.Time, length float64, inspector string) store.Record {
	r, _, err := f.svc.Submit(f.ctx, store.NewRecordInput{
		LocationID: locID, InspectDate: date, Method: "MT", Found: true,
		CrackLengthM: ptrFloat(length), Inspector: inspector,
	})
	if err != nil {
		f.t.Fatalf("submit found %v: %v", date, err)
	}
	return r
}

func (f *fixture) submitNF(locID int64, date time.Time, limit float64) store.Record {
	r, _, err := f.svc.Submit(f.ctx, store.NewRecordInput{
		LocationID: locID, InspectDate: date, Method: "UT", Found: false,
		DetectionLimitM: ptrFloat(limit), Inspector: "B",
	})
	if err != nil {
		f.t.Fatalf("submit nf %v: %v", date, err)
	}
	return r
}

func TestRejectInvalidFields(t *testing.T) {
	f := newFixture(t)
	loc, _ := f.createLocation(geometry.Edge, 0.3)
	d := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		in   store.NewRecordInput
		want string
	}{
		{"non-positive length", store.NewRecordInput{LocationID: loc.ID, InspectDate: d, Found: true, CrackLengthM: ptrFloat(0)}, "crack_length_m"},
		{"length >= width", store.NewRecordInput{LocationID: loc.ID, InspectDate: d, Found: true, CrackLengthM: ptrFloat(0.3)}, "crack_length_m"},
		{"nf missing limit", store.NewRecordInput{LocationID: loc.ID, InspectDate: d, Found: false}, "detection_limit_m"},
		{"nf non-positive limit", store.NewRecordInput{LocationID: loc.ID, InspectDate: d, Found: false, DetectionLimitM: ptrFloat(-1)}, "detection_limit_m"},
		{"before commission", store.NewRecordInput{LocationID: loc.ID, InspectDate: time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC), Found: true, CrackLengthM: ptrFloat(0.01)}, "inspect_date"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := f.svc.Submit(f.ctx, tc.in)
			fe, ok := err.(recordlog.FieldError)
			if !ok {
				t.Fatalf("expected FieldError, got %T %v", err, err)
			}
			if fe.Field != tc.want {
				t.Fatalf("field=%q want %q (%s)", fe.Field, tc.want, fe.Reason)
			}
		})
	}

	// Block/material validation.
	err := recordlog.ValidateBlocks([]spectrum.Block{{StressAmp: -1}})
	if fe, _ := err.(recordlog.FieldError); fe.Field == "" {
		t.Fatal("expected block validation error")
	}
	err = recordlog.ValidateBlocks([]spectrum.Block{{StressAmp: 250, MaxStress: 100, CyclesPerDay: 1}})
	if fe, _ := err.(recordlog.FieldError); fe.Field == "" {
		t.Fatal("expected amp>2max error")
	}
	err = recordlog.ValidateMaterialVersion(store.MaterialVersion{FractureKIC: 0, ParisM: 3, ParisC: 1, YieldStrength: 1})
	if fe, _ := err.(recordlog.FieldError); fe.Field != "fracture_kic" {
		t.Fatalf("field=%q", fe.Field)
	}
}
