package store

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Memory is an in-process Repository for deterministic unit tests. It
// serializes record mutation per location exactly like the Postgres row lock.
type Memory struct {
	mu sync.Mutex

	locations map[int64]Location
	materials map[int64]Material
	matVers   map[int64][]MaterialVersion
	spectra   map[int64][]SpectrumRevision // per location
	records   map[int64]Record
	byLoc     map[int64][]int64 // record ids per location (append order)
	logicals  map[int64]int64   // first record id -> logical id; and logical counter
	recVerSeq map[int64][]RecordVersion
	snapshots map[int64][]PlanSnapshot // per location

	nextLoc, nextMat, nextMatVer int64
	nextSpec, nextRec, nextLog   int64
	nextRecVer, nextSnap         int64

	// Clock stamps created_at; defaults to wall time, tests may override.
	Clock func() time.Time
}

// NewMemory constructs an empty memory repository.
func NewMemory() *Memory {
	return &Memory{
		locations: map[int64]Location{},
		materials: map[int64]Material{},
		matVers:   map[int64][]MaterialVersion{},
		spectra:   map[int64][]SpectrumRevision{},
		records:   map[int64]Record{},
		byLoc:     map[int64][]int64{},
		logicals:  map[int64]int64{},
		recVerSeq: map[int64][]RecordVersion{},
		snapshots: map[int64][]PlanSnapshot{},
		Clock:     time.Now,
	}
}

func (m *Memory) now() time.Time {
	if m.Clock != nil {
		return m.Clock()
	}
	return time.Now()
}

// SetClock installs the function used for created_at stamps.
func (m *Memory) SetClock(f func() time.Time) { m.Clock = f }

// lockMu exposes the global mutex so the domain layer's per-location
// serialization is observable in tests if needed.
func (m *Memory) lockMu() *sync.Mutex { return &m.mu }

func (m *Memory) CreateLocation(_ context.Context, l Location) (Location, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextLoc++
	l.ID = m.nextLoc
	if l.CreatedAt.IsZero() {
		l.CreatedAt = m.now()
	}
	m.locations[l.ID] = l
	return l, nil
}

func (m *Memory) GetLocation(_ context.Context, id int64) (Location, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.locations[id]
	if !ok {
		return Location{}, ErrNotFound{"location"}
	}
	return l, nil
}

func (m *Memory) ListLocations(_ context.Context) ([]Location, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Location, 0, len(m.locations))
	for _, l := range m.locations {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (m *Memory) CreateMaterial(_ context.Context, mat Material, v MaterialVersion) (Material, MaterialVersion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextMat++
	mat.ID = m.nextMat
	if mat.CreatedAt.IsZero() {
		mat.CreatedAt = m.now()
	}
	m.materials[mat.ID] = mat
	v.MaterialID = mat.ID
	m.nextMatVer++
	v.ID = m.nextMatVer
	v.Version = 1
	if v.CreatedAt.IsZero() {
		v.CreatedAt = m.now()
	}
	m.matVers[mat.ID] = []MaterialVersion{v}
	return mat, v, nil
}

func (m *Memory) GetMaterial(_ context.Context, id int64) (Material, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mat, ok := m.materials[id]
	if !ok {
		return Material{}, ErrNotFound{"material"}
	}
	return mat, nil
}

func (m *Memory) ListMaterials(_ context.Context) ([]Material, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Material, 0, len(m.materials))
	for _, mat := range m.materials {
		out = append(out, mat)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (m *Memory) AddMaterialVersion(_ context.Context, v MaterialVersion) (MaterialVersion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	vs, ok := m.matVers[v.MaterialID]
	if !ok {
		return MaterialVersion{}, ErrNotFound{"material"}
	}
	m.nextMatVer++
	v.ID = m.nextMatVer
	v.Version = len(vs) + 1
	if v.CreatedAt.IsZero() {
		v.CreatedAt = m.now()
	}
	m.matVers[v.MaterialID] = append(vs, v)
	return v, nil
}

func (m *Memory) GetMaterialVersion(_ context.Context, id int64) (MaterialVersion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, vs := range m.matVers {
		for _, v := range vs {
			if v.ID == id {
				return v, nil
			}
		}
	}
	return MaterialVersion{}, ErrNotFound{"material_version"}
}

func (m *Memory) CurrentMaterialVersion(_ context.Context, materialID int64) (MaterialVersion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	vs := m.matVers[materialID]
	if len(vs) == 0 {
		return MaterialVersion{}, ErrNotFound{"material_version"}
	}
	return vs[len(vs)-1], nil
}

func (m *Memory) MaterialVersionAt(_ context.Context, materialID int64, t time.Time) (MaterialVersion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	vs := m.matVers[materialID]
	var found *MaterialVersion
	for i := range vs {
		if !vs[i].CreatedAt.After(t) {
			found = &vs[i]
		}
	}
	if found == nil {
		return MaterialVersion{}, ErrNotFound{"material_version at date"}
	}
	return *found, nil
}

func (m *Memory) ListMaterialVersions(_ context.Context, materialID int64) ([]MaterialVersion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	vs := m.matVers[materialID]
	out := make([]MaterialVersion, len(vs))
	copy(out, vs)
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

func (m *Memory) ListLocationsByMaterial(_ context.Context, materialID int64) ([]Location, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Location
	for _, l := range m.locations {
		if l.MaterialID == materialID {
			out = append(out, l)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (m *Memory) AddSpectrumRevision(_ context.Context, s SpectrumRevision) (SpectrumRevision, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextSpec++
	s.ID = m.nextSpec
	if s.CreatedAt.IsZero() {
		s.CreatedAt = m.now()
	}
	m.spectra[s.LocationID] = append(m.spectra[s.LocationID], s)
	return s, nil
}

func (m *Memory) LatestSpectrum(_ context.Context, locID int64) (SpectrumRevision, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ss := m.spectra[locID]
	if len(ss) == 0 {
		return SpectrumRevision{}, ErrNotFound{"spectrum"}
	}
	return ss[len(ss)-1], nil
}

func (m *Memory) ListSpectrumRevisions(_ context.Context, locID int64) ([]SpectrumRevision, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ss := m.spectra[locID]
	out := make([]SpectrumRevision, len(ss))
	copy(out, ss)
	return out, nil
}

func (m *Memory) SpectrumAt(_ context.Context, locID int64, t time.Time) (SpectrumRevision, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ss := m.spectra[locID]
	var found *SpectrumRevision
	for i := range ss {
		if !ss[i].CreatedAt.After(t) {
			found = &ss[i]
		}
	}
	if found == nil {
		return SpectrumRevision{}, ErrNotFound{"spectrum at date"}
	}
	return *found, nil
}

// AppendRecord appends a record, links corrections, bumps the version.
func (m *Memory) AppendRecord(_ context.Context, in NewRecordInput) (Record, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	m.nextRec++
	id := m.nextRec
	r := Record{
		ID:              id,
		LogicalID:       id,
		LocationID:      in.LocationID,
		InspectDate:     in.InspectDate,
		Method:          in.Method,
		Found:           in.Found,
		CrackLengthM:    in.CrackLengthM,
		DetectionLimitM: in.DetectionLimitM,
		Inspector:       in.Inspector,
		SupersedesID:    in.SupersedesID,
		CreatedAt:       now,
	}
	if in.SupersedesID != nil {
		old, ok := m.records[*in.SupersedesID]
		if !ok {
			return Record{}, 0, ErrNotFound{"record to supersede"}
		}
		r.LogicalID = old.LogicalID
		old.SupersededBy = &id
		m.records[old.ID] = old
	}
	m.records[id] = r
	m.byLoc[in.LocationID] = append(m.byLoc[in.LocationID], id)

	m.nextRecVer++
	ver := RecordVersion{
		ID:         m.nextRecVer,
		LocationID: in.LocationID,
		Seq:        len(m.recVerSeq[in.LocationID]) + 1,
		EventID:    id,
		CreatedAt:  now,
	}
	m.recVerSeq[in.LocationID] = append(m.recVerSeq[in.LocationID], ver)
	return r, ver.ID, nil
}

func (m *Memory) GetRecord(_ context.Context, id int64) (Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.records[id]
	if !ok {
		return Record{}, ErrNotFound{"record"}
	}
	return r, nil
}

func (m *Memory) activeRowsLocked(locID int64) []Record {
	var out []Record
	for _, id := range m.byLoc[locID] {
		r := m.records[id]
		if r.SupersededBy == nil {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].InspectDate.Equal(out[j].InspectDate) {
			return out[i].InspectDate.Before(out[j].InspectDate)
		}
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func (m *Memory) ListActiveRecords(_ context.Context, locID int64) ([]Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.activeRowsLocked(locID), nil
}

func (m *Memory) ListRecordHistory(_ context.Context, locID int64) ([]Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Record
	for _, id := range m.byLoc[locID] {
		out = append(out, m.records[id])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

func (m *Memory) CurrentRecordVersion(_ context.Context, locID int64) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	vs := m.recVerSeq[locID]
	if len(vs) == 0 {
		return 0, nil
	}
	return vs[len(vs)-1].ID, nil
}

// RecordVersionAt returns the highest version row existing at time t.
func (m *Memory) RecordVersionAt(_ context.Context, locID int64, t time.Time) (int64, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	vs := m.recVerSeq[locID]
	var found *RecordVersion
	for i := range vs {
		if !vs[i].CreatedAt.After(t) {
			found = &vs[i]
		}
	}
	if found == nil {
		return 0, 0, ErrNotFound{"record version at date"}
	}
	return found.ID, found.Seq, nil
}

func (m *Memory) AddPlanSnapshot(_ context.Context, p PlanSnapshot) (PlanSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextSnap++
	p.ID = m.nextSnap
	if p.ComputedAt.IsZero() {
		p.ComputedAt = m.now()
	}
	m.snapshots[p.LocationID] = append(m.snapshots[p.LocationID], p)
	return p, nil
}

func (m *Memory) PlanSnapshotsBefore(_ context.Context, locID int64, t time.Time) ([]PlanSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []PlanSnapshot
	for _, s := range m.snapshots[locID] {
		if !s.ComputedAt.After(t) {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ComputedAt.After(out[j].ComputedAt) })
	return out, nil
}

func (m *Memory) LatestPlanSnapshot(_ context.Context, locID int64) (PlanSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ss := m.snapshots[locID]
	if len(ss) == 0 {
		return PlanSnapshot{}, ErrNotFound{"plan snapshot"}
	}
	return ss[len(ss)-1], nil
}
