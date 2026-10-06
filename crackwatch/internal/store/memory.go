package store

import (
	"context"
	"sort"
	"sync"
	"time"

	"crackwatch/internal/model"
)

// Memory is an in-memory Store, safe for concurrent use. It serves the unit
// tests (including concurrent submission) and mirrors PostgreSQL semantics.
type Memory struct {
	mu sync.Mutex

	materials map[string]*model.Material
	matVers   map[string][]model.MaterialVersion // materialID -> versions
	spVers    map[string][]model.SpectrumVersion // locationID -> versions

	locations map[string]*model.Location

	events map[string][]model.InspectionEvent // locationID -> append-ordered
	plans  map[string]model.PlanSnapshot

	locked map[string]struct{} // locations currently inside a tx
}

// NewMemory constructs an empty in-memory store.
func NewMemory() *Memory {
	return &Memory{
		materials: map[string]*model.Material{},
		matVers:   map[string][]model.MaterialVersion{},
		spVers:    map[string][]model.SpectrumVersion{},
		locations: map[string]*model.Location{},
		events:    map[string][]model.InspectionEvent{},
		plans:     map[string]model.PlanSnapshot{},
		locked:    map[string]struct{}{},
	}
}

func (s *Memory) CreateMaterial(_ context.Context, m *model.Material) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.materials[m.ID]; ok {
		return ErrAlreadyExists("material")
	}
	s.materials[m.ID] = m
	return nil
}

func (s *Memory) GetMaterial(_ context.Context, id string) (*model.Material, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.materials[id]
	if !ok {
		return nil, ErrNotFound{Entity: "material"}
	}
	cp := *m
	cp.Versions = append([]model.MaterialVersion(nil), s.matVers[id]...)
	return &cp, nil
}

func (s *Memory) ListMaterials(_ context.Context) ([]model.Material, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]model.Material, 0, len(s.materials))
	for _, m := range s.materials {
		cp := *m
		cp.Versions = append([]model.MaterialVersion(nil), s.matVers[m.ID]...)
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *Memory) CreateMaterialVersion(_ context.Context, v *model.MaterialVersion) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.materials[v.MaterialID]; !ok {
		return ErrNotFound{Entity: "material"}
	}
	if v.Version == 0 {
		v.Version = len(s.matVers[v.MaterialID]) + 1
	}
	s.matVers[v.MaterialID] = append(s.matVers[v.MaterialID], *v)
	return nil
}

func (s *Memory) CreateLocation(_ context.Context, l *model.Location) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.materials[l.MaterialID]; !ok {
		return ErrNotFound{Entity: "material"}
	}
	if _, ok := s.locations[l.ID]; ok {
		return ErrAlreadyExists("location")
	}
	cp := *l
	s.locations[l.ID] = &cp
	return nil
}

func (s *Memory) GetLocation(_ context.Context, id string) (*model.Location, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.locations[id]
	if !ok {
		return nil, ErrNotFound{Entity: "location"}
	}
	cp := *l
	return &cp, nil
}

func (s *Memory) ListLocations(_ context.Context) ([]model.Location, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]model.Location, 0, len(s.locations))
	for _, l := range s.locations {
		out = append(out, *l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *Memory) UpdateLocation(_ context.Context, l *model.Location) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.locations[l.ID]; !ok {
		return ErrNotFound{Entity: "location"}
	}
	cp := *l
	s.locations[l.ID] = &cp
	return nil
}

func (s *Memory) MaterialVersionAt(_ context.Context, materialID string, at time.Time) (*model.MaterialVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return latestEffective(s.matVers[materialID], at)
}

func (s *Memory) GetMaterialVersion(_ context.Context, id string) (*model.MaterialVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, vers := range s.matVers {
		for i := range vers {
			if vers[i].ID == id {
				v := vers[i]
				return &v, nil
			}
		}
	}
	return nil, ErrNotFound{Entity: "material_version"}
}

func (s *Memory) LatestMaterialVersion(_ context.Context, materialID string) (*model.MaterialVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	vers := s.matVers[materialID]
	if len(vers) == 0 {
		return nil, ErrNotFound{Entity: "material_version"}
	}
	v := vers[len(vers)-1]
	return &v, nil
}

func (s *Memory) CreateSpectrumVersion(_ context.Context, v *model.SpectrumVersion) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.locations[v.LocationID]; !ok {
		return ErrNotFound{Entity: "location"}
	}
	if v.Version == 0 {
		v.Version = len(s.spVers[v.LocationID]) + 1
	}
	s.spVers[v.LocationID] = append(s.spVers[v.LocationID], *v)
	return nil
}

func (s *Memory) SpectrumVersionAt(_ context.Context, locationID string, at time.Time) (*model.SpectrumVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	vers := s.spVers[locationID]
	var best *model.SpectrumVersion
	for i := range vers {
		if !vers[i].EffectiveAt.After(at) {
			v := vers[i]
			if best == nil || v.EffectiveAt.After(best.EffectiveAt) {
				best = &v
			}
		}
	}
	if best == nil {
		return nil, ErrNotFound{Entity: "spectrum_version"}
	}
	return best, nil
}

func (s *Memory) LatestSpectrumVersion(_ context.Context, locationID string) (*model.SpectrumVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	vers := s.spVers[locationID]
	if len(vers) == 0 {
		return nil, ErrNotFound{Entity: "spectrum_version"}
	}
	v := vers[len(vers)-1]
	return &v, nil
}

func (s *Memory) ListEvents(_ context.Context, locationID string) ([]model.InspectionEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]model.InspectionEvent(nil), s.events[locationID]...), nil
}

func (s *Memory) EventsKnownAt(_ context.Context, locationID string, at time.Time) ([]model.InspectionEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []model.InspectionEvent
	for _, e := range s.events[locationID] {
		if !e.RecordedAt.After(at) {
			out = append(out, e)
		}
	}
	return out, nil
}

func (s *Memory) ListPlans(_ context.Context, locationID string) ([]model.PlanSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []model.PlanSnapshot
	for _, p := range s.plans {
		if p.LocationID == locationID {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].BasisEventSeq != out[j].BasisEventSeq {
			return out[i].BasisEventSeq < out[j].BasisEventSeq
		}
		if out[i].MaterialVersion != out[j].MaterialVersion {
			return out[i].MaterialVersion < out[j].MaterialVersion
		}
		return out[i].SpectrumVersion < out[j].SpectrumVersion
	})
	return out, nil
}

func (s *Memory) GetPlan(_ context.Context, locationID string, eventSeq int64, matVerID, spVerID string) (*model.PlanSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.plans[snapshotKey(locationID, eventSeq, matVerID, spVerID)]; ok {
		return &p, nil
	}
	return nil, ErrNotFound{Entity: "plan"}
}

func latestEffective(vers []model.MaterialVersion, at time.Time) (*model.MaterialVersion, error) {
	var best *model.MaterialVersion
	for i := range vers {
		if !vers[i].EffectiveAt.After(at) {
			v := vers[i]
			if best == nil || v.EffectiveAt.After(best.EffectiveAt) {
				best = &v
			}
		}
	}
	if best == nil {
		return nil, ErrNotFound{Entity: "material_version"}
	}
	return best, nil
}

func snapshotKey(loc string, seq int64, mat, sp string) string {
	return loc + "|" + itoa(seq) + "|" + mat + "|" + sp
}

// WithLocationTx serialises transactions per location id using a condition
// variable: the second concurrent committer blocks until the first commits.
func (s *Memory) WithLocationTx(ctx context.Context, locationID string, fn func(Tx) error) error {
	s.mu.Lock()
	for {
		if _, busy := s.locked[locationID]; !busy {
			s.locked[locationID] = struct{}{}
			break
		}
		// Simple spin under lock would deadlock; wait via unlock/relock.
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Millisecond):
		}
		s.mu.Lock()
	}
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.locked, locationID)
		s.mu.Unlock()
	}()

	return fn(&memTx{s: s})
}

// memTx shares the memory store; the mutex plus coarse lock provides
// transactional semantics for the operations the service uses.
type memTx struct{ s *Memory }

func (t *memTx) AppendEvent(_ context.Context, e *model.InspectionEvent) (int64, error) {
	s := t.s
	s.mu.Lock()
	defer s.mu.Unlock()
	e.Seq = int64(len(s.events[e.LocationID]) + 1)
	s.events[e.LocationID] = append(s.events[e.LocationID], *e)
	return e.Seq, nil
}

func (t *memTx) SavePlan(_ context.Context, p *model.PlanSnapshot) error {
	s := t.s
	s.mu.Lock()
	defer s.mu.Unlock()
	s.plans[snapshotKey(p.LocationID, p.BasisEventSeq, p.MaterialVersionID, p.SpectrumVersionID)] = *p
	return nil
}

// Tx read/write methods delegate to the store methods.
func (t *memTx) GetMaterial(ctx context.Context, id string) (*model.Material, error) {
	return t.s.GetMaterial(ctx, id)
}
func (t *memTx) ListMaterials(ctx context.Context) ([]model.Material, error) {
	return t.s.ListMaterials(ctx)
}
func (t *memTx) CreateMaterial(ctx context.Context, m *model.Material) error {
	return t.s.CreateMaterial(ctx, m)
}
func (t *memTx) GetLocation(ctx context.Context, id string) (*model.Location, error) {
	return t.s.GetLocation(ctx, id)
}
func (t *memTx) ListLocations(ctx context.Context) ([]model.Location, error) {
	return t.s.ListLocations(ctx)
}
func (t *memTx) CreateLocation(ctx context.Context, l *model.Location) error {
	return t.s.CreateLocation(ctx, l)
}
func (t *memTx) UpdateLocation(ctx context.Context, l *model.Location) error {
	return t.s.UpdateLocation(ctx, l)
}
func (t *memTx) MaterialVersionAt(ctx context.Context, id string, at time.Time) (*model.MaterialVersion, error) {
	return t.s.MaterialVersionAt(ctx, id, at)
}
func (t *memTx) GetMaterialVersion(ctx context.Context, id string) (*model.MaterialVersion, error) {
	return t.s.GetMaterialVersion(ctx, id)
}
func (t *memTx) LatestMaterialVersion(ctx context.Context, id string) (*model.MaterialVersion, error) {
	return t.s.LatestMaterialVersion(ctx, id)
}
func (t *memTx) SpectrumVersionAt(ctx context.Context, id string, at time.Time) (*model.SpectrumVersion, error) {
	return t.s.SpectrumVersionAt(ctx, id, at)
}
func (t *memTx) LatestSpectrumVersion(ctx context.Context, id string) (*model.SpectrumVersion, error) {
	return t.s.LatestSpectrumVersion(ctx, id)
}
func (t *memTx) ListEvents(ctx context.Context, id string) ([]model.InspectionEvent, error) {
	return t.s.ListEvents(ctx, id)
}
func (t *memTx) EventsKnownAt(ctx context.Context, id string, at time.Time) ([]model.InspectionEvent, error) {
	return t.s.EventsKnownAt(ctx, id, at)
}
func (t *memTx) ListPlans(ctx context.Context, id string) ([]model.PlanSnapshot, error) {
	return t.s.ListPlans(ctx, id)
}
func (t *memTx) GetPlan(ctx context.Context, id string, seq int64, m, sp string) (*model.PlanSnapshot, error) {
	return t.s.GetPlan(ctx, id, seq, m, sp)
}
func (t *memTx) CreateMaterialVersion(ctx context.Context, v *model.MaterialVersion) error {
	return t.s.CreateMaterialVersion(ctx, v)
}
func (t *memTx) CreateSpectrumVersion(ctx context.Context, v *model.SpectrumVersion) error {
	return t.s.CreateSpectrumVersion(ctx, v)
}
