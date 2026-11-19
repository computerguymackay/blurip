package store

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"errors"
	"io"
	"time"
)

// DiscInfo is MakeMKV's raw description of a disc (`makemkvcon -r info`).
type DiscInfo struct {
	DiscID         int64
	MakeMKVVersion string
	ReadAt         time.Time
	Raw            []byte
}

// SaveDiscInfo keeps the raw info output for a disc, replacing any from
// an earlier read.
func (s *Store) SaveDiscInfo(ctx context.Context, d DiscInfo) error {
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	if _, err := w.Write(d.Raw); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO disc_info(disc_id, makemkv_version, read_at, raw_gz) VALUES(?, ?, ?, ?)
		ON CONFLICT(disc_id) DO UPDATE SET
			makemkv_version = excluded.makemkv_version, read_at = excluded.read_at, raw_gz = excluded.raw_gz`,
		d.DiscID, d.MakeMKVVersion, fmtTime(d.ReadAt), gz.Bytes())
	return err
}

// GetDiscInfo returns the raw info saved for a disc; ok is false if none
// was (discs read before blurip kept it).
func (s *Store) GetDiscInfo(ctx context.Context, discID int64) (d DiscInfo, ok bool, err error) {
	var read sql.NullString
	var gz []byte
	err = s.db.QueryRowContext(ctx, "SELECT disc_id, makemkv_version, read_at, raw_gz FROM disc_info WHERE disc_id = ?", discID).
		Scan(&d.DiscID, &d.MakeMKVVersion, &read, &gz)
	if errors.Is(err, sql.ErrNoRows) {
		return DiscInfo{}, false, nil
	} else if err != nil {
		return DiscInfo{}, false, err
	}
	d.ReadAt = parseTime(read)
	r, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return DiscInfo{}, false, err
	}
	if d.Raw, err = io.ReadAll(r); err != nil {
		return DiscInfo{}, false, err
	}
	return d, true, nil
}
