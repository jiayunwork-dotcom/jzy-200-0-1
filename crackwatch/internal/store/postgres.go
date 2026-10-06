package store

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"crackwatch/internal/domain/geometry"
	"crackwatch/internal/model"
)

//go:embed migrations/schema.sql
var schemaSQL string

// querier is satisfied by both the pool and a transaction.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Postgres is the PostgreSQL 16 backed Store.
type Postgres struct {
	pool *pgxpool.Pool
}

// OpenPostgres connects, verifies the connection and applies the schema.
func OpenPostgres(ctx context.Context, dsn string) (*Postgres, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	p := &Postgres{pool: pool}
	if _, err := pool.Exec(ctx, schemaSQL); err != nil {
		pool.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return p, nil
}

// Close releases the pool.
func (p *Postgres) Close() { p.pool.Close() }

// WithLocationTx runs fn in a SERIALIZABLE transaction holding an advisory
// lock derived from the location id, so concurrent record submissions for the
// same location execute one after another (and therefore converge to the
// same result as chronological entry).
func (p *Postgres) WithLocationTx(ctx context.Context, locationID string, fn func(Tx) error) error {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", locationID); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	if err := fn(&pgTx{pgDB: pgDB{q: tx}}); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}

// pgDB is the StoreReader implementation shared by pool and tx.
type pgDB struct{ q querier }

// pgTx adds the write methods to pgDB to satisfy Tx.
type pgTx struct{ pgDB }

func (p *Postgres) db() pgDB { return pgDB{q: p.pool} }

const materialVersionCols = `id, material_id, version, k1c, sy, m, c, grade, effective_at, created_at`

func scanMaterialVersion(row pgx.Row) (*model.MaterialVersion, error) {
	var v model.MaterialVersion
	err := row.Scan(&v.ID, &v.MaterialID, &v.Version,
		&v.Params.K1c, &v.Params.Sy, &v.Params.M, &v.Params.C,
		&v.Params.Grade, &v.EffectiveAt, &v.CreatedAt)
	if err != nil {
		return nil, mapErr(err, "material_version")
	}
	return &v, nil
}

func (d pgDB) CreateMaterial(ctx context.Context, m *model.Material) error {
	_, err := d.q.Exec(ctx,
		`INSERT INTO materials(id, grade, created_at) VALUES($1,$2,$3)`,
		m.ID, m.Grade, m.CreatedAt)
	return mapErr(err, "material")
}

func (d pgDB) GetMaterial(ctx context.Context, id string) (*model.Material, error) {
	row := d.q.QueryRow(ctx, `SELECT id, grade, created_at FROM materials WHERE id=$1`, id)
	var m model.Material
	if err := row.Scan(&m.ID, &m.Grade, &m.CreatedAt); err != nil {
		return nil, mapErr(err, "material")
	}
	rows, err := d.q.Query(ctx,
		`SELECT `+materialVersionCols+` FROM material_versions WHERE material_id=$1 ORDER BY version`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		v, err := scanMaterialVersion(rows)
		if err != nil {
			return nil, err
		}
		m.Versions = append(m.Versions, *v)
	}
	return &m, rows.Err()
}

func (d pgDB) ListMaterials(ctx context.Context) ([]model.Material, error) {
	rows, err := d.q.Query(ctx, `SELECT id, grade, created_at FROM materials ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Material
	for rows.Next() {
		var m model.Material
		if err := rows.Scan(&m.ID, &m.Grade, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		vrows, err := d.q.Query(ctx,
			`SELECT `+materialVersionCols+` FROM material_versions WHERE material_id=$1 ORDER BY version`, out[i].ID)
		if err != nil {
			return nil, err
		}
		for vrows.Next() {
			v, err := scanMaterialVersion(vrows)
			if err != nil {
				vrows.Close()
				return nil, err
			}
			out[i].Versions = append(out[i].Versions, *v)
		}
		vrows.Close()
		if err := vrows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (d pgDB) nextMaterialVersion(ctx context.Context, materialID string) (int, error) {
	var n int
	err := d.q.QueryRow(ctx,
		`SELECT COALESCE(MAX(version),0)+1 FROM material_versions WHERE material_id=$1`,
		materialID).Scan(&n)
	return n, err
}

func (d pgDB) CreateMaterialVersion(ctx context.Context, v *model.MaterialVersion) error {
	if v.Version == 0 {
		n, err := d.nextMaterialVersion(ctx, v.MaterialID)
		if err != nil {
			return err
		}
		v.Version = n
	}
	_, err := d.q.Exec(ctx, `
		INSERT INTO material_versions(id, material_id, version, k1c, sy, m, c, grade, effective_at, created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		v.ID, v.MaterialID, v.Version, v.Params.K1c, v.Params.Sy, v.Params.M, v.Params.C,
		v.Params.Grade, v.EffectiveAt, v.CreatedAt)
	return mapErr(err, "material_version")
}

func (d pgDB) MaterialVersionAt(ctx context.Context, materialID string, at time.Time) (*model.MaterialVersion, error) {
	return scanMaterialVersion(d.q.QueryRow(ctx,
		`SELECT `+materialVersionCols+` FROM material_versions
		 WHERE material_id=$1 AND effective_at <= $2 ORDER BY version DESC LIMIT 1`,
		materialID, at))
}

func (d pgDB) GetMaterialVersion(ctx context.Context, id string) (*model.MaterialVersion, error) {
	return scanMaterialVersion(d.q.QueryRow(ctx,
		`SELECT `+materialVersionCols+` FROM material_versions WHERE id=$1`, id))
}

func (d pgDB) LatestMaterialVersion(ctx context.Context, materialID string) (*model.MaterialVersion, error) {
	return scanMaterialVersion(d.q.QueryRow(ctx,
		`SELECT `+materialVersionCols+` FROM material_versions
		 WHERE material_id=$1 ORDER BY version DESC LIMIT 1`, materialID))
}

const locationCols = `id, name, geometry, width_mm, material_id, safety_factor, commissioned_at, created_at, updated_at`

func scanLocation(row pgx.Row) (*model.Location, error) {
	var l model.Location
	var geom string
	err := row.Scan(&l.ID, &l.Name, &geom, &l.WidthMM, &l.MaterialID, &l.SafetyFactor,
		&l.CommissionedAt, &l.CreatedAt, &l.UpdatedAt)
	if err != nil {
		return nil, mapErr(err, "location")
	}
	l.Geometry = geometry.Kind(geom)
	return &l, nil
}

func (d pgDB) CreateLocation(ctx context.Context, l *model.Location) error {
	_, err := d.q.Exec(ctx, `
		INSERT INTO locations(id, name, geometry, width_mm, material_id, safety_factor,
			commissioned_at, created_at, updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		l.ID, l.Name, string(l.Geometry), l.WidthMM, l.MaterialID, l.SafetyFactor,
		l.CommissionedAt, l.CreatedAt, l.UpdatedAt)
	return mapErr(err, "location")
}

func (d pgDB) GetLocation(ctx context.Context, id string) (*model.Location, error) {
	return scanLocation(d.q.QueryRow(ctx,
		`SELECT `+locationCols+` FROM locations WHERE id=$1`, id))
}

func (d pgDB) ListLocations(ctx context.Context) ([]model.Location, error) {
	rows, err := d.q.Query(ctx, `SELECT `+locationCols+` FROM locations ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Location
	for rows.Next() {
		l, err := scanLocation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *l)
	}
	return out, rows.Err()
}

func (d pgDB) UpdateLocation(ctx context.Context, l *model.Location) error {
	_, err := d.q.Exec(ctx,
		`UPDATE locations SET name=$2, safety_factor=$3, updated_at=$4 WHERE id=$1`,
		l.ID, l.Name, l.SafetyFactor, l.UpdatedAt)
	return mapErr(err, "location")
}

const spectrumCols = `id, location_id, version, blocks, effective_at, created_at`

func (d pgDB) nextSpectrumVersion(ctx context.Context, locID string) (int, error) {
	var n int
	err := d.q.QueryRow(ctx,
		`SELECT COALESCE(MAX(version),0)+1 FROM spectrum_versions WHERE location_id=$1`,
		locID).Scan(&n)
	return n, err
}

func (d pgDB) CreateSpectrumVersion(ctx context.Context, v *model.SpectrumVersion) error {
	if v.Version == 0 {
		n, err := d.nextSpectrumVersion(ctx, v.LocationID)
		if err != nil {
			return err
		}
		v.Version = n
	}
	raw, err := json.Marshal(v.Blocks)
	if err != nil {
		return err
	}
	_, err = d.q.Exec(ctx, `
		INSERT INTO spectrum_versions(id, location_id, version, blocks, effective_at, created_at)
		VALUES($1,$2,$3,$4::jsonb,$5,$6)`,
		v.ID, v.LocationID, v.Version, string(raw), v.EffectiveAt, v.CreatedAt)
	return mapErr(err, "spectrum_version")
}

func scanSpectrum(row pgx.Row) (*model.SpectrumVersion, error) {
	var v model.SpectrumVersion
	var raw []byte
	err := row.Scan(&v.ID, &v.LocationID, &v.Version, &raw, &v.EffectiveAt, &v.CreatedAt)
	if err != nil {
		return nil, mapErr(err, "spectrum_version")
	}
	if err := json.Unmarshal(raw, &v.Blocks); err != nil {
		return nil, err
	}
	return &v, nil
}

func (d pgDB) SpectrumVersionAt(ctx context.Context, locID string, at time.Time) (*model.SpectrumVersion, error) {
	return scanSpectrum(d.q.QueryRow(ctx,
		`SELECT `+spectrumCols+` FROM spectrum_versions
		 WHERE location_id=$1 AND effective_at <= $2 ORDER BY version DESC LIMIT 1`,
		locID, at))
}

func (d pgDB) LatestSpectrumVersion(ctx context.Context, locID string) (*model.SpectrumVersion, error) {
	return scanSpectrum(d.q.QueryRow(ctx,
		`SELECT `+spectrumCols+` FROM spectrum_versions
		 WHERE location_id=$1 ORDER BY version DESC LIMIT 1`, locID))
}

const eventCols = `id, location_id, seq, inspected_at, method, kind, length_mm,
	detect_limit_mm, note, supersedes_id, inspector, recorded_at`

func scanEvent(row pgx.Row) (model.InspectionEvent, error) {
	var e model.InspectionEvent
	var kind string
	err := row.Scan(&e.ID, &e.LocationID, &e.Seq, &e.InspectedAt, &e.Method, &kind,
		&e.LengthMM, &e.DetectLimitMM, &e.Note, &e.SupersedesID, &e.Inspector, &e.RecordedAt)
	e.Kind = model.ResultKind(kind)
	return e, mapErr(err, "inspection_event")
}

func (d pgDB) AppendEvent(ctx context.Context, e *model.InspectionEvent) (int64, error) {
	if err := d.q.QueryRow(ctx,
		`SELECT COALESCE(MAX(seq),0)+1 FROM inspection_events WHERE location_id=$1 FOR UPDATE`,
		e.LocationID).Scan(&e.Seq); err != nil {
		return 0, err
	}
	_, err := d.q.Exec(ctx, `
		INSERT INTO inspection_events(id, location_id, seq, inspected_at, method, kind,
			length_mm, detect_limit_mm, note, supersedes_id, inspector, recorded_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		e.ID, e.LocationID, e.Seq, e.InspectedAt, e.Method, string(e.Kind),
		e.LengthMM, e.DetectLimitMM, e.Note, e.SupersedesID, e.Inspector, e.RecordedAt)
	if err != nil {
		return 0, mapErr(err, "inspection_event")
	}
	return e.Seq, nil
}

func (d pgDB) ListEvents(ctx context.Context, locID string) ([]model.InspectionEvent, error) {
	rows, err := d.q.Query(ctx,
		`SELECT `+eventCols+` FROM inspection_events WHERE location_id=$1 ORDER BY seq`, locID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.InspectionEvent
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (d pgDB) EventsKnownAt(ctx context.Context, locID string, at time.Time) ([]model.InspectionEvent, error) {
	rows, err := d.q.Query(ctx,
		`SELECT `+eventCols+` FROM inspection_events
		 WHERE location_id=$1 AND recorded_at <= $2 ORDER BY seq`, locID, at)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.InspectionEvent
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (d pgDB) SavePlan(ctx context.Context, p *model.PlanSnapshot) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = d.q.Exec(ctx, `
		INSERT INTO plan_snapshots(location_id, basis_event_seq, material_version_id,
			spectrum_version_id, payload, computed_at)
		VALUES($1,$2,$3,$4,$5::jsonb,$6)
		ON CONFLICT (location_id, basis_event_seq, material_version_id, spectrum_version_id)
		DO UPDATE SET payload=EXCLUDED.payload, computed_at=EXCLUDED.computed_at`,
		p.LocationID, p.BasisEventSeq, p.MaterialVersionID, p.SpectrumVersionID,
		string(raw), p.ComputedAt)
	return mapErr(err, "plan_snapshot")
}

const planCols = `location_id, basis_event_seq, material_version_id, spectrum_version_id, payload, computed_at`

func (d pgDB) GetPlan(ctx context.Context, locID string, seq int64, mv, sv string) (*model.PlanSnapshot, error) {
	var raw []byte
	var loc, mvID, svID string
	var s int64
	var computed time.Time
	err := d.q.QueryRow(ctx,
		`SELECT `+planCols+` FROM plan_snapshots
		 WHERE location_id=$1 AND basis_event_seq=$2 AND material_version_id=$3 AND spectrum_version_id=$4`,
		locID, seq, mv, sv).
		Scan(&loc, &s, &mvID, &svID, &raw, &computed)
	if err != nil {
		return nil, mapErr(err, "plan")
	}
	var p model.PlanSnapshot
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

func (d pgDB) ListPlans(ctx context.Context, locID string) ([]model.PlanSnapshot, error) {
	rows, err := d.q.Query(ctx,
		`SELECT payload FROM plan_snapshots WHERE location_id=$1
		 ORDER BY basis_event_seq, material_version_id, spectrum_version_id`, locID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.PlanSnapshot
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var p model.PlanSnapshot
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Store-level readers delegate to the pool-backed pgDB.
func (p *Postgres) CreateMaterial(ctx context.Context, m *model.Material) error {
	return p.db().CreateMaterial(ctx, m)
}
func (p *Postgres) GetMaterial(ctx context.Context, id string) (*model.Material, error) {
	return p.db().GetMaterial(ctx, id)
}
func (p *Postgres) ListMaterials(ctx context.Context) ([]model.Material, error) {
	return p.db().ListMaterials(ctx)
}
func (p *Postgres) CreateLocation(ctx context.Context, l *model.Location) error {
	return p.db().CreateLocation(ctx, l)
}
func (p *Postgres) GetLocation(ctx context.Context, id string) (*model.Location, error) {
	return p.db().GetLocation(ctx, id)
}
func (p *Postgres) ListLocations(ctx context.Context) ([]model.Location, error) {
	return p.db().ListLocations(ctx)
}
func (p *Postgres) UpdateLocation(ctx context.Context, l *model.Location) error {
	return p.db().UpdateLocation(ctx, l)
}
func (p *Postgres) MaterialVersionAt(ctx context.Context, id string, at time.Time) (*model.MaterialVersion, error) {
	return p.db().MaterialVersionAt(ctx, id, at)
}
func (p *Postgres) GetMaterialVersion(ctx context.Context, id string) (*model.MaterialVersion, error) {
	return p.db().GetMaterialVersion(ctx, id)
}
func (p *Postgres) LatestMaterialVersion(ctx context.Context, id string) (*model.MaterialVersion, error) {
	return p.db().LatestMaterialVersion(ctx, id)
}
func (p *Postgres) SpectrumVersionAt(ctx context.Context, id string, at time.Time) (*model.SpectrumVersion, error) {
	return p.db().SpectrumVersionAt(ctx, id, at)
}
func (p *Postgres) LatestSpectrumVersion(ctx context.Context, id string) (*model.SpectrumVersion, error) {
	return p.db().LatestSpectrumVersion(ctx, id)
}
func (p *Postgres) ListEvents(ctx context.Context, id string) ([]model.InspectionEvent, error) {
	return p.db().ListEvents(ctx, id)
}
func (p *Postgres) EventsKnownAt(ctx context.Context, id string, at time.Time) ([]model.InspectionEvent, error) {
	return p.db().EventsKnownAt(ctx, id, at)
}
func (p *Postgres) GetPlan(ctx context.Context, id string, seq int64, m, sp string) (*model.PlanSnapshot, error) {
	return p.db().GetPlan(ctx, id, seq, m, sp)
}
func (p *Postgres) ListPlans(ctx context.Context, id string) ([]model.PlanSnapshot, error) {
	return p.db().ListPlans(ctx, id)
}

func mapErr(err error, entity string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound{Entity: entity}
	}
	return err
}

var _ Store = (*Postgres)(nil)
var _ Tx = (*pgTx)(nil)
