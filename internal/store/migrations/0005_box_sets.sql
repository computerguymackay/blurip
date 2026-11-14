-- TV box sets (blurip --box-set): how far each show's season has got, so
-- numbering carries on after blurip is restarted.
CREATE TABLE box_sets (
    id           INTEGER PRIMARY KEY,
    show         TEXT    NOT NULL COLLATE NOCASE,
    season       INTEGER NOT NULL,
    last_disc    INTEGER NOT NULL DEFAULT 0,   -- disc number of the last disc numbered; 0 = unknown
    next_episode INTEGER NOT NULL DEFAULT 1,
    started_at   TEXT    NOT NULL,
    updated_at   TEXT    NOT NULL,
    UNIQUE (show, season)
);

-- Which episode each title of a disc became. Kept per disc (not per job)
-- so a disc put back in after a failed rip gets the same numbers.
CREATE TABLE disc_episodes (
    disc_id    INTEGER NOT NULL REFERENCES discs(id),
    box_set_id INTEGER NOT NULL REFERENCES box_sets(id) ON DELETE CASCADE,
    disc_no    INTEGER NOT NULL DEFAULT 0,     -- from the disc label; 0 = unknown
    title_id   INTEGER NOT NULL,
    position   INTEGER NOT NULL,               -- rip order on the disc, from 0
    episode    INTEGER NOT NULL,
    PRIMARY KEY (disc_id, title_id)
);
CREATE INDEX disc_episodes_box_set ON disc_episodes(box_set_id);
