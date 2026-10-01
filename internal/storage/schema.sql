CREATE TABLE IF NOT EXISTS checkpoints (
    container_id TEXT PRIMARY KEY,
    payload BLOB NOT NULL
);

CREATE TABLE IF NOT EXISTS current_state (
    container_id TEXT PRIMARY KEY,
    payload BLOB NOT NULL
);

CREATE TABLE IF NOT EXISTS events (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    container_id TEXT NOT NULL,
    observed_at TEXT NOT NULL,
    payload BLOB NOT NULL
);

CREATE INDEX IF NOT EXISTS events_container_sequence
    ON events (container_id, sequence DESC);

PRAGMA user_version = 1;
