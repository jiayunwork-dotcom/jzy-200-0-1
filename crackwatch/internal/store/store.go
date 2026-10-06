// Package store defines persistence for the crackwatch service and provides
// an in-memory implementation for tests plus a PostgreSQL implementation.
package store

import (
	"context"
	"time"

	"crackwatch/internal/model"
)

// Tx is the transactional handle seen by callback functions. All operations
// inside one callback belong to one serialisable transaction; PostgreSQL
// implementations take a per-location advisory lock so concurrent event
// appends are serialised.
type Tx interface {
	StoreReader
	// AppendEvent serialises appends for one location and returns the
	// assigned per-location sequence number.
	AppendEvent(ctx context.Context, e *model.InspectionEvent) (int64, error)
	// SavePlan upserts the plan snapshot bound to (location, event seq,
	// material version, spectrum version).
	SavePlan(ctx context.Context, p *model.PlanSnapshot) error
	CreateMaterialVersion(ctx context.Context, v *model.MaterialVersion) error
	CreateSpectrumVersion(ctx context.Context, v *model.SpectrumVersion) error
}

// StoreReader is the read side of persistence.
type StoreReader interface {
	GetMaterial(ctx context.Context, id string) (*model.Material, error)
	ListMaterials(ctx context.Context) ([]model.Material, error)
	CreateMaterial(ctx context.Context, m *model.Material) error

	GetLocation(ctx context.Context, id string) (*model.Location, error)
	ListLocations(ctx context.Context) ([]model.Location, error)
	CreateLocation(ctx context.Context, l *model.Location) error
	UpdateLocation(ctx context.Context, l *model.Location) error

	MaterialVersionAt(ctx context.Context, materialID string, at time.Time) (*model.MaterialVersion, error)
	GetMaterialVersion(ctx context.Context, id string) (*model.MaterialVersion, error)
	LatestMaterialVersion(ctx context.Context, materialID string) (*model.MaterialVersion, error)

	SpectrumVersionAt(ctx context.Context, locationID string, at time.Time) (*model.SpectrumVersion, error)
	LatestSpectrumVersion(ctx context.Context, locationID string) (*model.SpectrumVersion, error)

	// ListEvents returns all events (including superseded/void) for a
	// location in append order.
	ListEvents(ctx context.Context, locationID string) ([]model.InspectionEvent, error)
	// EventsKnownAt returns events with RecordedAt <= at, in append order —
	// the basis for as-of historical queries ("what was known then").
	EventsKnownAt(ctx context.Context, locationID string, at time.Time) ([]model.InspectionEvent, error)

	ListPlans(ctx context.Context, locationID string) ([]model.PlanSnapshot, error)

	GetPlan(ctx context.Context, locationID string, eventSeq int64, materialVersionID, spectrumVersionID string) (*model.PlanSnapshot, error)
}

// Store is the full persistence interface.
type Store interface {
	StoreReader
	// WithLocationTx runs fn inside a transaction holding the location's
	// serialisation lock.
	WithLocationTx(ctx context.Context, locationID string, fn func(Tx) error) error
}

// ErrNotFound is returned for missing entities.
type ErrNotFound struct{ Entity string }

func (e ErrNotFound) Error() string { return e.Entity + " not found" }
