-- crackstation schema, PostgreSQL 16.
--
-- All created_at columns default to a session-controllable clock: when the
-- application sets the transaction-local GUC 'app.now' (RFC3339 text) it is
-- used verbatim, otherwise the wall clock now() applies. This is what lets
-- historical replay ("plan as of date") be tested deterministically; in
-- production 'app.now' is simply never set.

CREATE OR REPLACE FUNCTION app_now() RETURNS timestamptz AS $$
BEGIN
  RETURN COALESCE(nullif(current_setting('app.now', true), '')::timestamptz, now());
EXCEPTION WHEN OTHERS THEN
  RETURN now();
END;
$$ LANGUAGE plpgsql IMMUTABLE;

CREATE TABLE IF NOT EXISTS materials (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    grade      TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT app_now()
);

CREATE TABLE IF NOT EXISTS material_versions (
    id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    material_id    BIGINT NOT NULL REFERENCES materials(id),
    version        INT  NOT NULL,
    paris_m        DOUBLE PRECISION NOT NULL,
    paris_c        DOUBLE PRECISION NOT NULL,
    fracture_kic   DOUBLE PRECISION NOT NULL,
    yield_strength DOUBLE PRECISION NOT NULL,
    created_by     TEXT NOT NULL DEFAULT '',
    note           TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT app_now(),
    UNIQUE (material_id, version)
);

CREATE TABLE IF NOT EXISTS locations (
    id                BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name              TEXT NOT NULL,
    crane             TEXT NOT NULL DEFAULT '',
    geometry          TEXT NOT NULL CHECK (geometry IN ('center_through_crack','edge_crack')),
    width_m           DOUBLE PRECISION NOT NULL CHECK (width_m > 0),
    material_id       BIGINT NOT NULL REFERENCES materials(id),
    commissioned_date DATE NOT NULL,
    safety_factor     DOUBLE PRECISION NOT NULL DEFAULT 2 CHECK (safety_factor > 0),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT app_now()
);

CREATE TABLE IF NOT EXISTS spectrum_revisions (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    location_id BIGINT NOT NULL REFERENCES locations(id),
    blocks      JSONB NOT NULL,
    created_by  TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT app_now()
);
CREATE INDEX IF NOT EXISTS ix_spectrum_loc_time ON spectrum_revisions (location_id, created_at);

CREATE TABLE IF NOT EXISTS records (
    id                BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    logical_id        BIGINT NOT NULL,
    location_id       BIGINT NOT NULL REFERENCES locations(id),
    inspect_date      DATE NOT NULL,
    method            TEXT NOT NULL DEFAULT '',
    found             BOOLEAN NOT NULL,
    crack_length_m    DOUBLE PRECISION,
    detection_limit_m DOUBLE PRECISION,
    inspector         TEXT NOT NULL DEFAULT '',
    supersedes_id     BIGINT REFERENCES records(id),
    superseded_by     BIGINT REFERENCES records(id),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT app_now()
);
CREATE INDEX IF NOT EXISTS ix_records_loc ON records (location_id);
CREATE INDEX IF NOT EXISTS ix_records_logical ON records (logical_id);

CREATE TABLE IF NOT EXISTS record_versions (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    location_id BIGINT NOT NULL REFERENCES locations(id),
    seq         INT NOT NULL,
    event_id    BIGINT NOT NULL REFERENCES records(id),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT app_now(),
    UNIQUE (location_id, seq)
);

CREATE TABLE IF NOT EXISTS plan_snapshots (
    id                     BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    location_id            BIGINT NOT NULL REFERENCES locations(id),
    record_version_id      BIGINT REFERENCES record_versions(id),
    material_version_id    BIGINT NOT NULL REFERENCES material_versions(id),
    spectrum_id            BIGINT REFERENCES spectrum_revisions(id),
    base_date              DATE,
    current_a              DOUBLE PRECISION NOT NULL,
    current_physical_l     DOUBLE PRECISION NOT NULL,
    assumed_from_limit     BOOLEAN NOT NULL,
    critical_a             DOUBLE PRECISION NOT NULL,
    fracture_a             DOUBLE PRECISION NOT NULL,
    net_yield_a            DOUBLE PRECISION NOT NULL,
    already_critical       BOOLEAN NOT NULL,
    days_to_critical       DOUBLE PRECISION NOT NULL,
    cycles_per_day         DOUBLE PRECISION NOT NULL,
    cycles_to_critical     DOUBLE PRECISION NOT NULL,
    fitted_c               DOUBLE PRECISION NOT NULL,
    coefficient_calibrated BOOLEAN NOT NULL,
    safety_factor          DOUBLE PRECISION NOT NULL,
    interval_days          DOUBLE PRECISION NOT NULL,
    next_inspect_date      DATE,
    trigger                TEXT NOT NULL DEFAULT '',
    computed_at            TIMESTAMPTZ NOT NULL DEFAULT app_now()
);
CREATE INDEX IF NOT EXISTS ix_snapshots_loc_time ON plan_snapshots (location_id, computed_at);
