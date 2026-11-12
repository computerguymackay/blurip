CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE drives (
    id      INTEGER PRIMARY KEY,
    device  TEXT NOT NULL UNIQUE,
    name    TEXT NOT NULL DEFAULT '',
    enabled INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE discs (
    id           INTEGER PRIMARY KEY,
    fingerprint  TEXT NOT NULL UNIQUE,
    volume_label TEXT NOT NULL DEFAULT '',
    disc_type    TEXT NOT NULL DEFAULT '',
    first_seen   TEXT NOT NULL
);

CREATE TABLE jobs (
    id             INTEGER PRIMARY KEY,
    disc_id        INTEGER REFERENCES discs(id),
    drive_id       INTEGER REFERENCES drives(id),
    started_at     TEXT NOT NULL,
    finished_at    TEXT,
    status         TEXT NOT NULL CHECK (status IN ('running', 'succeeded', 'failed', 'interrupted', 'skipped')),
    failure_reason TEXT NOT NULL DEFAULT '',
    output_path    TEXT NOT NULL DEFAULT '',
    bytes_written  INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX jobs_disc_status ON jobs(disc_id, status);
CREATE INDEX jobs_status ON jobs(status);

CREATE TABLE job_titles (
    job_id             INTEGER NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    title_id           INTEGER NOT NULL,
    audio_selection    TEXT NOT NULL DEFAULT '',
    subtitle_selection TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (job_id, title_id)
);
