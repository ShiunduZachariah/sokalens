-- players + venues reference catalogue (Task: API-Football reference ingestion).
-- Players are scoped to a canonical team; both tables are low-churn reference
-- entities populated by the worker's players/venues ingestion jobs.
CREATE TABLE IF NOT EXISTS players (
    id          BIGINT PRIMARY KEY,
    team_id     BIGINT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    age         INTEGER,
    number      INTEGER,
    position    TEXT,
    photo_url   TEXT,
    nationality TEXT,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_players_team_id
    ON players (team_id);

CREATE INDEX IF NOT EXISTS idx_players_name_lower
    ON players (lower(name));

CREATE TABLE IF NOT EXISTS venues (
    id         BIGINT PRIMARY KEY,
    name       TEXT NOT NULL,
    address    TEXT,
    city       TEXT,
    country    TEXT,
    capacity   INTEGER,
    surface    TEXT,
    image_url  TEXT,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_venues_name_lower
    ON venues (lower(name));
