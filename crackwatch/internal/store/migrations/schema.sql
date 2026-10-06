-- crackwatch schema. Idempotent; applied on startup.

CREATE TABLE IF NOT EXISTS materials (
    id          TEXT PRIMARY KEY,
    grade       TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS material_versions (
    id            TEXT PRIMARY KEY,
    material_id   TEXT NOT NULL REFERENCES materials(id),
    version       INTEGER NOT NULL,
    k1c           DOUBLE PRECISION NOT NULL CHECK (k1c > 0),
    sy            DOUBLE PRECISION NOT NULL CHECK (sy > 0),
    m             DOUBLE PRECISION NOT NULL CHECK (m > 0),
    c             DOUBLE PRECISION NOT NULL CHECK (c > 0),
    grade         TEXT NOT NULL,
    effective_at  TIMESTAMPTZ NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL,
    UNIQUE (material_id, version)
);

CREATE TABLE IF NOT EXISTS locations (
    id               TEXT PRIMARY KEY,
    name             TEXT NOT NULL,
    geometry         TEXT NOT NULL CHECK (geometry IN ('center_crack','edge_crack')),
    width_mm         DOUBLE PRECISION NOT NULL CHECK (width_mm > 0),
    material_id      TEXT NOT NULL REFERENCES materials(id),
    safety_factor    DOUBLE PRECISION NOT NULL CHECK (safety_factor > 0),
    commissioned_at  TIMESTAMPTZ NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL,
    updated_at       TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS spectrum_versions (
    id            TEXT PRIMARY KEY,
    location_id   TEXT NOT NULL REFERENCES locations(id),
    version       INTEGER NOT NULL,
    blocks        JSONB NOT NULL,
    effective_at  TIMESTAMPTZ NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL,
    UNIQUE (location_id, version)
);

-- Append-only inspection record store. Corrections append a new row naming
-- the superseded row; deletions are rows with kind='void'. Nothing is ever
-- updated or deleted here.
CREATE TABLE IF NOT EXISTS inspection_events (
    id               TEXT PRIMARY KEY,
    location_id      TEXT NOT NULL REFERENCES locations(id),
    seq              BIGINT NOT NULL,
    inspected_at     TIMESTAMPTZ NOT NULL,
    method           TEXT NOT NULL,
    kind             TEXT NOT NULL CHECK (kind IN ('found','not_detected','void')),
    length_mm        DOUBLE PRECISION NOT NULL DEFAULT 0,
    detect_limit_mm  DOUBLE PRECISION NOT NULL DEFAULT 0,
    note             TEXT NOT NULL DEFAULT '',
    supersedes_id    TEXT NOT NULL DEFAULT '',
    inspector        TEXT NOT NULL DEFAULT '',
    recorded_at      TIMESTAMPTZ NOT NULL,
    UNIQUE (location_id, seq)
);
-- An event can be superseded at most once (excluding the empty marker).
CREATE UNIQUE INDEX IF NOT EXISTS uq_events_supersedes
    ON inspection_events(supersedes_id) WHERE supersedes_id <> '';
CREATE INDEX IF NOT EXISTS idx_events_location_recorded
    ON inspection_events(location_id, recorded_at);
CREATE INDEX IF NOT EXISTS idx_events_supersedes
    ON inspection_events(supersedes_id);

-- Plans are cached keyed by exactly the record/parameter versions they were
-- derived from.
CREATE TABLE IF NOT EXISTS plan_snapshots (
    location_id           TEXT NOT NULL REFERENCES locations(id),
    basis_event_seq       BIGINT NOT NULL,
    material_version_id   TEXT NOT NULL,
    spectrum_version_id   TEXT NOT NULL,
    payload               JSONB NOT NULL,
    computed_at           TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (location_id, basis_event_seq, material_version_id, spectrum_version_id)
);
