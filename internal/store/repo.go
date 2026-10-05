package store

import (
	"context"
	"time"
)

// Repository is the persistence contract used by the domain services. Two
// implementations exist: Postgres (production) and Memory (deterministic
// unit tests). Every record-mutating operation is serialized per location,
// which is what makes concurrent inspector submissions produce exactly the
// same final state as sequential date-ordered processing.
type Repository interface {
	// Locations
	CreateLocation(ctx context.Context, l Location) (Location, error)
	GetLocation(ctx context.Context, id int64) (Location, error)
	ListLocations(ctx context.Context) ([]Location, error)

	// Materials
	CreateMaterial(ctx context.Context, m Material, v MaterialVersion) (Material, MaterialVersion, error)
	GetMaterial(ctx context.Context, id int64) (Material, error)
	ListMaterials(ctx context.Context) ([]Material, error)
	AddMaterialVersion(ctx context.Context, v MaterialVersion) (MaterialVersion, error)
	GetMaterialVersion(ctx context.Context, id int64) (MaterialVersion, error)
	CurrentMaterialVersion(ctx context.Context, materialID int64) (MaterialVersion, error)
	// MaterialVersionAt returns the version effective at date t.
	MaterialVersionAt(ctx context.Context, materialID int64, t time.Time) (MaterialVersion, error)
	ListMaterialVersions(ctx context.Context, materialID int64) ([]MaterialVersion, error)
	ListLocationsByMaterial(ctx context.Context, materialID int64) ([]Location, error)

	// Spectra
	AddSpectrumRevision(ctx context.Context, s SpectrumRevision) (SpectrumRevision, error)
	LatestSpectrum(ctx context.Context, locationID int64) (SpectrumRevision, error)
	// SpectrumAt returns the revision effective at date t.
	SpectrumAt(ctx context.Context, locationID int64, t time.Time) (SpectrumRevision, error)
	// ListSpectrumRevisions returns all revisions of a location, oldest first.
	ListSpectrumRevisions(ctx context.Context, locationID int64) ([]SpectrumRevision, error)

	// Records. AppendRecord is atomic: insert the record, link a
	// correction when SupersedesID is set, bump the per-location version.
	AppendRecord(ctx context.Context, in NewRecordInput) (Record, int64, error)
	GetRecord(ctx context.Context, id int64) (Record, error)
	// ListActiveRecords returns non-superseded records ordered by
	// (inspect_date ASC, created_at ASC, id ASC).
	ListActiveRecords(ctx context.Context, locationID int64) ([]Record, error)
	// ListRecordHistory returns ALL rows incl. superseded, newest first.
	ListRecordHistory(ctx context.Context, locationID int64) ([]Record, error)
	CurrentRecordVersion(ctx context.Context, locationID int64) (int64, error)
	// RecordVersionAt returns the highest version existing at time t.
	RecordVersionAt(ctx context.Context, locationID int64, t time.Time) (int64, int, error)

	// Plan snapshots
	AddPlanSnapshot(ctx context.Context, p PlanSnapshot) (PlanSnapshot, error)
	// PlanSnapshotsBefore returns snapshots with computed_at <= t, newest first.
	PlanSnapshotsBefore(ctx context.Context, locationID int64, t time.Time) ([]PlanSnapshot, error)
	LatestPlanSnapshot(ctx context.Context, locationID int64) (PlanSnapshot, error)
}

// ErrNotFound is returned for missing rows.
type ErrNotFound struct{ What string }

func (e ErrNotFound) Error() string { return "not found: " + e.What }

// ClockSettable is implemented by repositories that can stamp their
// created_at values from an externally supplied clock (used by the domain
// service and deterministic tests). It is optional: production deployments
// that never call it get wall-clock timestamps.
type ClockSettable interface {
	SetClock(f func() time.Time)
}
