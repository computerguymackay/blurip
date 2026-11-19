-- MakeMKV's own description of each disc (`makemkvcon -r info` output,
-- gzipped), from the latest time it was read: every title's length, size,
-- segments and streams, kept for looking discs up or sharing them later.
CREATE TABLE disc_info (
    disc_id         INTEGER PRIMARY KEY REFERENCES discs(id) ON DELETE CASCADE,
    makemkv_version TEXT NOT NULL DEFAULT '',
    read_at         TEXT NOT NULL,
    raw_gz          BLOB NOT NULL
);
