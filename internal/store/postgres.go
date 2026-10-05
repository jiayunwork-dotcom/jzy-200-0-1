package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"crackstation/internal/geometry"
	"crackstation/internal/spectrum"
)

// Postgres implements Repository over PostgreSQL 16.
type Postgres struct {
	db    *sql.DB
	clock func() time.Time
}

// SetClock installs the function used for created_at stamps. The value is
// applied per write through the session GUC app.now (see migration); when
// unset, the database default now() is used.
func (p *Postgres) SetClock(f func() time.Time) { p.clock = f }

// stamp sets app.now on the given connection handle (a tx or the pool).
func (p *Postgres) stamp(ctx context.Context, exec interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}) error {
	if p.clock == nil {
		return nil
	}
	_, err := exec.ExecContext(ctx, `SELECT set_config('app.now', $1, false)`,
		p.clock().UTC().Format(time.RFC3339Nano))
	return err
}

// stampTx sets app.now scoped to the transaction (reset at COMMIT/ROLLBACK).
func (p *Postgres) stampTx(ctx context.Context, tx *sql.Tx) error {
	if p.clock == nil {
		return nil
	}
	_, err := tx.ExecContext(ctx, `SELECT set_config('app.now', $1, true)`,
		p.clock().UTC().Format(time.RFC3339Nano))
	return err
}

// OpenPostgres opens the pool and verifies connectivity.
func OpenPostgres(dsn string) (*Postgres, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(time.Hour)
	p := &Postgres{db: db}
	return p, nil
}

// DB exposes the handle (migrations, health checks).
func (p *Postgres) DB() *sql.DB { return p.db }

// Ping checks connectivity.
func (p *Postgres) Ping(ctx context.Context) error { return p.db.PingContext(ctx) }

// ExecScript runs a multi-statement migration script.
func (p *Postgres) ExecScript(ctx context.Context, script string) error {
	_, err := p.db.ExecContext(ctx, script)
	return err
}

func marshalBlocks(bs []spectrum.Block) ([]byte, error) { return json.Marshal(bs) }
func unmarshalBlocks(raw []byte) ([]spectrum.Block, error) {
	var bs []spectrum.Block
	if len(raw) == 0 {
		return nil, nil
	}
	if err := json.Unmarshal(raw, &bs); err != nil {
		return nil, err
	}
	return bs, nil
}

func notFoundOnNoRows(err error, what string) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound{what}
	}
	return err
}

// ---------- locations ----------

func (p *Postgres) CreateLocation(ctx context.Context, l Location) (Location, error) {
	if err := p.stamp(ctx, p.db); err != nil {
		return Location{}, err
	}
	row := p.db.QueryRowContext(ctx, `
		INSERT INTO locations (name, crane, geometry, width_m, material_id, commissioned_date, safety_factor)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		RETURNING id, created_at`,
		l.Name, l.Crane, string(l.Geometry), l.WidthM, l.MaterialID,
		DateToValue(l.CommissionedDate), nz(l.SafetyFactor, 2))
	if err := row.Scan(&l.ID, &l.CreatedAt); err != nil {
		return Location{}, err
	}
	return l, nil
}

func nz(v, d float64) float64 {
	if v <= 0 {
		return d
	}
	return v
}

// DateToValue normalizes to date.
func DateToValue(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

func scanLocation(row interface{ Scan(...any) error }) (Location, error) {
	var l Location
	var geo string
	if err := row.Scan(&l.ID, &l.Name, &l.Crane, &geo, &l.WidthM, &l.MaterialID,
		&l.CommissionedDate, &l.SafetyFactor, &l.CreatedAt); err != nil {
		return Location{}, notFoundOnNoRows(err, "location")
	}
	l.Geometry = geometry.Type(geo)
	return l, nil
}

const locCols = `id, name, crane, geometry, width_m, material_id, commissioned_date, safety_factor, created_at`

func (p *Postgres) GetLocation(ctx context.Context, id int64) (Location, error) {
	row := p.db.QueryRowContext(ctx, `SELECT `+locCols+` FROM locations WHERE id=$1`, id)
	return scanLocation(row)
}

func (p *Postgres) ListLocations(ctx context.Context) ([]Location, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT `+locCols+` FROM locations ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Location
	for rows.Next() {
		l, err := scanLocation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ---------- materials ----------

func (p *Postgres) CreateMaterial(ctx context.Context, m Material, v MaterialVersion) (Material, MaterialVersion, error) {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return m, v, err
	}
	defer tx.Rollback()
	if err := p.stampTx(ctx, tx); err != nil {
		return m, v, err
	}
	row := tx.QueryRowContext(ctx,
		`INSERT INTO materials (grade) VALUES ($1) RETURNING id, created_at`, m.Grade)
	if err := row.Scan(&m.ID, &m.CreatedAt); err != nil {
		return m, v, err
	}
	row = tx.QueryRowContext(ctx, `
		INSERT INTO material_versions
		  (material_id, version, paris_m, paris_c, fracture_kic, yield_strength, created_by, note)
		VALUES ($1,1,$2,$3,$4,$5,$6,$7)
		RETURNING id, created_at`,
		m.ID, v.ParisM, v.ParisC, v.FractureKIC, v.YieldStrength, v.CreatedBy, v.Note)
	if err := row.Scan(&v.ID, &v.CreatedAt); err != nil {
		return m, v, err
	}
	v.MaterialID = m.ID
	v.Version = 1
	return m, v, tx.Commit()
}

const matVerCols = `id, material_id, version, paris_m, paris_c, fracture_kic, yield_strength, created_by, note, created_at`

func scanMatVer(row interface{ Scan(...any) error }) (MaterialVersion, error) {
	var v MaterialVersion
	err := row.Scan(&v.ID, &v.MaterialID, &v.Version, &v.ParisM, &v.ParisC,
		&v.FractureKIC, &v.YieldStrength, &v.CreatedBy, &v.Note, &v.CreatedAt)
	return v, notFoundOnNoRows(err, "material_version")
}

func (p *Postgres) AddMaterialVersion(ctx context.Context, v MaterialVersion) (MaterialVersion, error) {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return MaterialVersion{}, err
	}
	defer tx.Rollback()
	if err := p.stampTx(ctx, tx); err != nil {
		return MaterialVersion{}, err
	}
	row := tx.QueryRowContext(ctx, `
		INSERT INTO material_versions
		  (material_id, version, paris_m, paris_c, fracture_kic, yield_strength, created_by, note)
		SELECT $1, COALESCE(MAX(version),0)+1, $2,$3,$4,$5,$6,$7
		FROM material_versions WHERE material_id=$1
		RETURNING id, version, created_at`,
		v.MaterialID, v.ParisM, v.ParisC, v.FractureKIC, v.YieldStrength, v.CreatedBy, v.Note)
	if err := row.Scan(&v.ID, &v.Version, &v.CreatedAt); err != nil {
		return MaterialVersion{}, err
	}
	return v, tx.Commit()
}

func (p *Postgres) GetMaterial(ctx context.Context, id int64) (Material, error) {
	row := p.db.QueryRowContext(ctx, `SELECT id, grade, created_at FROM materials WHERE id=$1`, id)
	var m Material
	err := row.Scan(&m.ID, &m.Grade, &m.CreatedAt)
	return m, notFoundOnNoRows(err, "material")
}

func (p *Postgres) ListMaterials(ctx context.Context) ([]Material, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT id, grade, created_at FROM materials ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Material
	for rows.Next() {
		var m Material
		if err := rows.Scan(&m.ID, &m.Grade, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (p *Postgres) GetMaterialVersion(ctx context.Context, id int64) (MaterialVersion, error) {
	return scanMatVer(p.db.QueryRowContext(ctx, `SELECT `+matVerCols+` FROM material_versions WHERE id=$1`, id))
}

func (p *Postgres) CurrentMaterialVersion(ctx context.Context, materialID int64) (MaterialVersion, error) {
	return scanMatVer(p.db.QueryRowContext(ctx,
		`SELECT `+matVerCols+` FROM material_versions WHERE material_id=$1 ORDER BY version DESC LIMIT 1`, materialID))
}

func (p *Postgres) MaterialVersionAt(ctx context.Context, materialID int64, t time.Time) (MaterialVersion, error) {
	return scanMatVer(p.db.QueryRowContext(ctx,
		`SELECT `+matVerCols+` FROM material_versions
		 WHERE material_id=$1 AND created_at <= $2 ORDER BY version DESC LIMIT 1`, materialID, t))
}

func (p *Postgres) ListMaterialVersions(ctx context.Context, materialID int64) ([]MaterialVersion, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT `+matVerCols+` FROM material_versions WHERE material_id=$1 ORDER BY version`, materialID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MaterialVersion
	for rows.Next() {
		v, err := scanMatVer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (p *Postgres) ListLocationsByMaterial(ctx context.Context, materialID int64) ([]Location, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT `+locCols+` FROM locations WHERE material_id=$1 ORDER BY id`, materialID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Location
	for rows.Next() {
		l, err := scanLocation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ---------- spectra ----------

func (p *Postgres) AddSpectrumRevision(ctx context.Context, s SpectrumRevision) (SpectrumRevision, error) {
	raw, err := marshalBlocks(s.Blocks)
	if err != nil {
		return s, err
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return s, err
	}
	defer tx.Rollback()
	if err := p.stampTx(ctx, tx); err != nil {
		return s, err
	}
	row := tx.QueryRowContext(ctx, `
		INSERT INTO spectrum_revisions (location_id, blocks, created_by)
		VALUES ($1,$2,$3) RETURNING id, created_at`,
		s.LocationID, raw, s.CreatedBy)
	if err := row.Scan(&s.ID, &s.CreatedAt); err != nil {
		return s, err
	}
	return s, tx.Commit()
}

func scanSpec(row interface{ Scan(...any) error }) (SpectrumRevision, error) {
	var s SpectrumRevision
	var raw []byte
	err := row.Scan(&s.ID, &s.LocationID, &raw, &s.CreatedBy, &s.CreatedAt)
	if err != nil {
		return s, notFoundOnNoRows(err, "spectrum")
	}
	s.Blocks, err = unmarshalBlocks(raw)
	return s, err
}

const specCols = `id, location_id, blocks, created_by, created_at`

func (p *Postgres) LatestSpectrum(ctx context.Context, locID int64) (SpectrumRevision, error) {
	return scanSpec(p.db.QueryRowContext(ctx,
		`SELECT `+specCols+` FROM spectrum_revisions WHERE location_id=$1 ORDER BY id DESC LIMIT 1`, locID))
}

func (p *Postgres) SpectrumAt(ctx context.Context, locID int64, t time.Time) (SpectrumRevision, error) {
	return scanSpec(p.db.QueryRowContext(ctx,
		`SELECT `+specCols+` FROM spectrum_revisions WHERE location_id=$1 AND created_at <= $2
		 ORDER BY id DESC LIMIT 1`, locID, t))
}

func (p *Postgres) ListSpectrumRevisions(ctx context.Context, locID int64) ([]SpectrumRevision, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT `+specCols+` FROM spectrum_revisions WHERE location_id=$1 ORDER BY id`, locID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SpectrumRevision
	for rows.Next() {
		s, err := scanSpec(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ---------- records ----------

const recCols = `id, logical_id, location_id, inspect_date, method, found, crack_length_m,
	detection_limit_m, inspector, supersedes_id, superseded_by, created_at`

func scanRecord(row interface{ Scan(...any) error }) (Record, error) {
	var r Record
	var insp time.Time
	var sup, supBy sql.NullInt64
	var crack, lim sql.NullFloat64
	err := row.Scan(&r.ID, &r.LogicalID, &r.LocationID, &insp, &r.Method, &r.Found,
		&crack, &lim, &r.Inspector, &sup, &supBy, &r.CreatedAt)
	if err != nil {
		return r, notFoundOnNoRows(err, "record")
	}
	r.InspectDate = insp
	if crack.Valid {
		v := crack.Float64
		r.CrackLengthM = &v
	}
	if lim.Valid {
		v := lim.Float64
		r.DetectionLimitM = &v
	}
	if sup.Valid {
		v := sup.Int64
		r.SupersedesID = &v
	}
	if supBy.Valid {
		v := supBy.Int64
		r.SupersededBy = &v
	}
	return r, nil
}

// AppendRecord inserts the record, links a correction, and bumps the
// per-location version under a row lock so concurrent submissions are
// serialized at the database as well as in-process.
func (p *Postgres) AppendRecord(ctx context.Context, in NewRecordInput) (Record, int64, error) {
	tx, err := p.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return Record{}, 0, err
	}
	defer tx.Rollback()

	if err := p.stampTx(ctx, tx); err != nil {
		return Record{}, 0, err
	}
	if _, err := tx.ExecContext(ctx, `SELECT id FROM locations WHERE id=$1 FOR UPDATE`, in.LocationID); err != nil {
		return Record{}, 0, err
	}

	var logicalID int64
	var sup sql.NullInt64
	if in.SupersedesID != nil {
		sup = sql.NullInt64{Int64: *in.SupersedesID, Valid: true}
	}
	if sup.Valid {
		if err := tx.QueryRowContext(ctx,
			`SELECT logical_id FROM records WHERE id=$1`, sup.Int64).Scan(&logicalID); err != nil {
			return Record{}, 0, err
		}
	}

	var rID int64
	var createdAt time.Time
	if sup.Valid {
		row := tx.QueryRowContext(ctx, `
			INSERT INTO records
			  (logical_id, location_id, inspect_date, method, found, crack_length_m,
			   detection_limit_m, inspector, supersedes_id)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
			RETURNING id, created_at`,
			logicalID, in.LocationID, DateToValue(in.InspectDate), in.Method, in.Found,
			nullFloat(in.Found, in.CrackLengthM), nullFloat(!in.Found, in.DetectionLimitM),
			in.Inspector, sup)
		if err := row.Scan(&rID, &createdAt); err != nil {
			return Record{}, 0, err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE records SET superseded_by=$1 WHERE id=$2 AND superseded_by IS NULL`,
			rID, sup.Int64); err != nil {
			return Record{}, 0, err
		}
	} else {
		// New event: insert, then make logical_id equal the new id.
		row := tx.QueryRowContext(ctx, `
			INSERT INTO records
			  (logical_id, location_id, inspect_date, method, found, crack_length_m,
			   detection_limit_m, inspector)
			VALUES (0,$1,$2,$3,$4,$5,$6,$7)
			RETURNING id, created_at`,
			in.LocationID, DateToValue(in.InspectDate), in.Method, in.Found,
			nullFloat(in.Found, in.CrackLengthM), nullFloat(!in.Found, in.DetectionLimitM),
			in.Inspector)
		if err := row.Scan(&rID, &createdAt); err != nil {
			return Record{}, 0, err
		}
		logicalID = rID
		if _, err := tx.ExecContext(ctx,
			`UPDATE records SET logical_id=id WHERE id=$1`, rID); err != nil {
			return Record{}, 0, err
		}
	}

	var verID int64
	row := tx.QueryRowContext(ctx, `
		INSERT INTO record_versions (location_id, seq, event_id)
		SELECT $1, COALESCE(MAX(seq),0)+1, $2 FROM record_versions WHERE location_id=$1
		RETURNING id`, in.LocationID, rID)
	if err := row.Scan(&verID); err != nil {
		return Record{}, 0, err
	}

	r := Record{ID: rID, LogicalID: logicalID, LocationID: in.LocationID,
		InspectDate: DateToValue(in.InspectDate), Method: in.Method, Found: in.Found,
		CrackLengthM: in.CrackLengthM, DetectionLimitM: in.DetectionLimitM,
		Inspector: in.Inspector, SupersedesID: in.SupersedesID, CreatedAt: createdAt}
	if err := tx.Commit(); err != nil {
		return Record{}, 0, err
	}
	return r, verID, nil
}

func nullFloat(use bool, p *float64) sql.NullFloat64 {
	if use && p != nil {
		return sql.NullFloat64{Float64: *p, Valid: true}
	}
	return sql.NullFloat64{}
}

func (p *Postgres) GetRecord(ctx context.Context, id int64) (Record, error) {
	return scanRecord(p.db.QueryRowContext(ctx, `SELECT `+recCols+` FROM records WHERE id=$1`, id))
}

func (p *Postgres) ListActiveRecords(ctx context.Context, locID int64) ([]Record, error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT `+recCols+` FROM records
		WHERE location_id=$1 AND superseded_by IS NULL
		ORDER BY inspect_date, created_at, id`, locID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectRecords(rows)
}

func (p *Postgres) ListRecordHistory(ctx context.Context, locID int64) ([]Record, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT `+recCols+` FROM records WHERE location_id=$1 ORDER BY id DESC`, locID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectRecords(rows)
}

type rowScanner interface {
	Next() bool
	Scan(...any) error
	Err() error
}

func collectRecords(rows *sql.Rows) ([]Record, error) {
	var out []Record
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (p *Postgres) CurrentRecordVersion(ctx context.Context, locID int64) (int64, error) {
	var id sql.NullInt64
	err := p.db.QueryRowContext(ctx,
		`SELECT id FROM record_versions WHERE location_id=$1 ORDER BY seq DESC LIMIT 1`, locID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id.Int64, err
}

func (p *Postgres) RecordVersionAt(ctx context.Context, locID int64, t time.Time) (int64, int, error) {
	var id int64
	var seq int
	err := p.db.QueryRowContext(ctx,
		`SELECT id, seq FROM record_versions WHERE location_id=$1 AND created_at <= $2
		 ORDER BY seq DESC LIMIT 1`, locID, t).Scan(&id, &seq)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, ErrNotFound{"record version at date"}
	}
	return id, seq, err
}

// ---------- plan snapshots ----------

func (p *Postgres) AddPlanSnapshot(ctx context.Context, s PlanSnapshot) (PlanSnapshot, error) {
	row := p.db.QueryRowContext(ctx, `
		INSERT INTO plan_snapshots (
		  location_id, record_version_id, material_version_id, spectrum_id, base_date,
		  current_a, current_physical_l, assumed_from_limit, critical_a, fracture_a,
		  net_yield_a, already_critical, days_to_critical, cycles_per_day, cycles_to_critical,
		  fitted_c, coefficient_calibrated, safety_factor, interval_days, next_inspect_date,
		  trigger, computed_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22)
		RETURNING id, computed_at`,
		s.LocationID, s.RecordVersionID, s.MaterialVersionID, s.SpectrumID, s.BaseDate,
		s.CurrentA, s.CurrentPhysicalL, s.AssumedFromLimit, s.CriticalA, s.FractureA,
		s.NetYieldA, s.AlreadyCritical, s.DaysToCritical, s.CyclesPerDay, s.CyclesToCritical,
		s.FittedC, s.CoefficientCalibrated, s.SafetyFactor, s.IntervalDays, s.NextInspectDate,
		s.Trigger, nzTime(s.ComputedAt))
	if err := row.Scan(&s.ID, &s.ComputedAt); err != nil {
		return s, err
	}
	return s, nil
}

func nzTime(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now()
	}
	return t
}

func (p *Postgres) scanSnapshots(q string, locID int64, args ...any) ([]PlanSnapshot, error) {
	rows, err := p.db.Query(q, append([]any{locID}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PlanSnapshot
	for rows.Next() {
		var s PlanSnapshot
		var base, next sql.NullTime
		var ver, spec sql.NullInt64
		err := rows.Scan(&s.ID, &s.LocationID, &ver, &s.MaterialVersionID, &spec, &base,
			&s.CurrentA, &s.CurrentPhysicalL, &s.AssumedFromLimit, &s.CriticalA, &s.FractureA,
			&s.NetYieldA, &s.AlreadyCritical, &s.DaysToCritical, &s.CyclesPerDay,
			&s.CyclesToCritical, &s.FittedC, &s.CoefficientCalibrated, &s.SafetyFactor,
			&s.IntervalDays, &next, &s.Trigger, &s.ComputedAt)
		if err != nil {
			return nil, err
		}
		if base.Valid {
			s.BaseDate = &base.Time
		}
		if next.Valid {
			s.NextInspectDate = &next.Time
		}
		if ver.Valid {
			s.RecordVersionID = &ver.Int64
		}
		if spec.Valid {
			s.SpectrumID = &spec.Int64
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (p *Postgres) PlanSnapshotsBefore(ctx context.Context, locID int64, t time.Time) ([]PlanSnapshot, error) {
	return p.scanSnapshots(`
		SELECT id, location_id, record_version_id, material_version_id, spectrum_id, base_date,
		  current_a, current_physical_l, assumed_from_limit, critical_a, fracture_a,
		  net_yield_a, already_critical, days_to_critical, cycles_per_day, cycles_to_critical,
		  fitted_c, coefficient_calibrated, safety_factor, interval_days, next_inspect_date,
		  trigger, computed_at
		FROM plan_snapshots WHERE location_id=$1 AND computed_at<=$2
		ORDER BY computed_at DESC`, locID, t)
}

func (p *Postgres) LatestPlanSnapshot(ctx context.Context, locID int64) (PlanSnapshot, error) {
	ss, err := p.scanSnapshots(`
		SELECT id, location_id, record_version_id, material_version_id, spectrum_id, base_date,
		  current_a, current_physical_l, assumed_from_limit, critical_a, fracture_a,
		  net_yield_a, already_critical, days_to_critical, cycles_per_day, cycles_to_critical,
		  fitted_c, coefficient_calibrated, safety_factor, interval_days, next_inspect_date,
		  trigger, computed_at
		FROM plan_snapshots WHERE location_id=$1 ORDER BY computed_at DESC, id DESC LIMIT 1`, locID)
	if err != nil {
		return PlanSnapshot{}, err
	}
	if len(ss) == 0 {
		return PlanSnapshot{}, ErrNotFound{"plan snapshot"}
	}
	return ss[0], nil
}
