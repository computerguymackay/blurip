-- The HandBrake encode queue (blurip -Q, blurip encode). Each entry keeps
-- its own settings, so changing the defaults never changes entries that
-- are already queued.
CREATE TABLE encodes (
    id              INTEGER PRIMARY KEY,
    job_id          INTEGER REFERENCES jobs(id),   -- the rip it came from; NULL if added by path
    source_path     TEXT    NOT NULL,              -- the ripped MKV
    disc_kind       TEXT    NOT NULL DEFAULT '',   -- DVD | Blu-ray | ''
    title           TEXT    NOT NULL,              -- "Ip Man", "Killjoys - S01E01"
    preset          TEXT    NOT NULL,              -- "General/Fast 1080p30"
    all_tracks      INTEGER NOT NULL DEFAULT 0,    -- keep every audio and subtitle track (foreign films; MKV)
    tracks_answered INTEGER NOT NULL DEFAULT 1,    -- 0: the foreign-film question wasn't answered
    dest_dir        TEXT    NOT NULL,              -- the movies or shows folder
    dest_name       TEXT    NOT NULL,              -- path inside dest_dir, without extension: "Ip Man/Ip Man"
    status          TEXT    NOT NULL DEFAULT 'waiting', -- waiting | encoding | done | failed | removed
    failure_reason  TEXT    NOT NULL DEFAULT '',
    output_path     TEXT    NOT NULL DEFAULT '',
    queued_at       TEXT    NOT NULL,
    started_at      TEXT,
    finished_at     TEXT,
    bytes_in        INTEGER NOT NULL DEFAULT 0,
    bytes_out       INTEGER NOT NULL DEFAULT 0,
    source_deleted  INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX encodes_status ON encodes(status, id);
CREATE INDEX encodes_job ON encodes(job_id);

-- Whether a TV show is in a foreign language (keep every track when
-- encoding), asked once per show in box-set mode with -Q.
CREATE TABLE show_tracks (
    show       TEXT    PRIMARY KEY COLLATE NOCASE,
    all_tracks INTEGER NOT NULL
);
