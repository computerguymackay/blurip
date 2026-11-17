package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Encode statuses.
const (
	EncodeWaiting  = "waiting"
	EncodeEncoding = "encoding"
	EncodeDone     = "done"
	EncodeFailed   = "failed"
	EncodeRemoved  = "removed"
)

// Encode is an entry in the HandBrake encode queue.
type Encode struct {
	ID             int64
	JobID          int64 // 0 if added by path
	SourcePath     string
	DiscKind       string // "DVD", "Blu-ray" or ""
	Title          string
	Preset         string
	AllTracks      bool
	TracksAnswered bool
	DestDir        string
	DestName       string
	Status         string
	FailureReason  string
	OutputPath     string
	QueuedAt       time.Time
	StartedAt      time.Time
	FinishedAt     time.Time
	BytesIn        int64
	BytesOut       int64
	SourceDeleted  bool
	Length         time.Duration // the rip's running time; 0 if not known
}

// AddEncode queues an entry (status waiting) at the end of the queue and
// returns its ID.
func (s *Store) AddEncode(ctx context.Context, e Encode) (int64, error) {
	r, err := s.db.ExecContext(ctx, `
		INSERT INTO encodes(job_id, source_path, disc_kind, title, preset, all_tracks, tracks_answered, dest_dir, dest_name, status, queued_at, position)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, (SELECT COALESCE(MAX(position), 0) + 1 FROM encodes))`,
		nullID(e.JobID), e.SourcePath, e.DiscKind, e.Title, e.Preset, e.AllTracks, e.TracksAnswered,
		e.DestDir, e.DestName, EncodeWaiting, fmtTime(e.QueuedAt))
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}

// UpdateEncode saves an entry's editable fields: source, title, preset,
// tracks, destination and status.
func (s *Store) UpdateEncode(ctx context.Context, e Encode) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE encodes SET source_path = ?, disc_kind = ?, title = ?, preset = ?, all_tracks = ?, tracks_answered = ?,
		       dest_dir = ?, dest_name = ?, status = ?, failure_reason = ?
		WHERE id = ?`,
		e.SourcePath, e.DiscKind, e.Title, e.Preset, e.AllTracks, e.TracksAnswered, e.DestDir, e.DestName,
		e.Status, e.FailureReason, e.ID)
	return err
}

// StartEncode marks an entry as encoding.
func (s *Store) StartEncode(ctx context.Context, id int64, now time.Time) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE encodes SET status = ?, started_at = ?, failure_reason = '' WHERE id = ?", EncodeEncoding, fmtTime(now), id)
	return err
}

// FinishEncode records how an encode ended: e's status, reason, output,
// sizes, length, finish time and whether the rip was deleted.
func (s *Store) FinishEncode(ctx context.Context, e Encode) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE encodes SET status = ?, failure_reason = ?, output_path = ?, finished_at = ?, bytes_in = ?, bytes_out = ?, source_deleted = ?,
		       length_seconds = ?
		WHERE id = ?`,
		e.Status, e.FailureReason, e.OutputPath, fmtTime(e.FinishedAt), e.BytesIn, e.BytesOut, e.SourceDeleted,
		e.Length.Seconds(), e.ID)
	return err
}

// RequeueEncode puts an entry that was stopped part way back in the queue.
func (s *Store) RequeueEncode(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE encodes SET status = ?, started_at = NULL WHERE id = ?", EncodeWaiting, id)
	return err
}

// RecoverEncodes puts entries left "encoding" by a stopped or crashed
// encoder back in the queue. Only call it while holding the encode lock.
func (s *Store) RecoverEncodes(ctx context.Context) (int64, error) {
	r, err := s.db.ExecContext(ctx,
		"UPDATE encodes SET status = ?, started_at = NULL WHERE status = ?", EncodeWaiting, EncodeEncoding)
	if err != nil {
		return 0, err
	}
	return r.RowsAffected()
}

// NextEncode returns the first waiting entry.
func (s *Store) NextEncode(ctx context.Context) (Encode, bool, error) {
	es, err := s.queryEncodes(ctx, "WHERE status = ? ORDER BY position, id LIMIT 1", EncodeWaiting)
	if err != nil || len(es) == 0 {
		return Encode{}, false, err
	}
	return es[0], true, nil
}

// GetEncode returns one entry.
func (s *Store) GetEncode(ctx context.Context, id int64) (Encode, error) {
	es, err := s.queryEncodes(ctx, "WHERE id = ?", id)
	if err != nil {
		return Encode{}, err
	}
	if len(es) == 0 {
		return Encode{}, ErrNotFound
	}
	return es[0], nil
}

// Encodes returns entries with the given statuses (all if none), in queue
// order.
func (s *Store) Encodes(ctx context.Context, statuses ...string) ([]Encode, error) {
	if len(statuses) == 0 {
		return s.queryEncodes(ctx, "ORDER BY position, id")
	}
	q := "WHERE status IN (?" + repeat(",?", len(statuses)-1) + ") ORDER BY position, id"
	args := make([]any, len(statuses))
	for i, st := range statuses {
		args[i] = st
	}
	return s.queryEncodes(ctx, q, args...)
}

// ReorderEncodes puts the entries ids in that order. They fill the places
// in the queue they had between them, so every other entry (such as
// finished ones, in the full list) keeps its place. Unknown ids are an
// error.
func (s *Store) ReorderEncodes(ctx context.Context, ids []int64) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT id FROM encodes ORDER BY position, id")
		if err != nil {
			return err
		}
		var all []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			all = append(all, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		moving := make(map[int64]bool, len(ids))
		for _, id := range ids {
			moving[id] = true
		}
		next := 0
		for i, id := range all {
			if moving[id] {
				all[i] = ids[next]
				next++
			}
		}
		if next != len(ids) {
			return ErrNotFound
		}
		for i, id := range all {
			if _, err := tx.ExecContext(ctx, "UPDATE encodes SET position = ? WHERE id = ?", i+1, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// EncodesForJob returns the entries queued from a rip.
func (s *Store) EncodesForJob(ctx context.Context, jobID int64) ([]Encode, error) {
	return s.queryEncodes(ctx, "WHERE job_id = ? ORDER BY id", jobID)
}

// IsQueued reports whether a file is in the queue (waiting, encoding or
// failed). A file encoded before can be queued again.
func (s *Store) IsQueued(ctx context.Context, sourcePath string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM encodes WHERE source_path = ? AND status IN (?, ?, ?)",
		sourcePath, EncodeWaiting, EncodeEncoding, EncodeFailed).Scan(&n)
	return n > 0, err
}

func repeat(s string, n int) string {
	out := ""
	for range n {
		out += s
	}
	return out
}

func (s *Store) queryEncodes(ctx context.Context, where string, args ...any) ([]Encode, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, COALESCE(job_id, 0), source_path, disc_kind, title, preset, all_tracks, tracks_answered,
		       dest_dir, dest_name, status, failure_reason, output_path, queued_at, started_at, finished_at,
		       bytes_in, bytes_out, source_deleted, length_seconds
		FROM encodes `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Encode
	for rows.Next() {
		var e Encode
		var queued, started, finished sql.NullString
		var length float64
		if err := rows.Scan(&e.ID, &e.JobID, &e.SourcePath, &e.DiscKind, &e.Title, &e.Preset, &e.AllTracks, &e.TracksAnswered,
			&e.DestDir, &e.DestName, &e.Status, &e.FailureReason, &e.OutputPath, &queued, &started, &finished,
			&e.BytesIn, &e.BytesOut, &e.SourceDeleted, &length); err != nil {
			return nil, err
		}
		e.QueuedAt, e.StartedAt, e.FinishedAt = parseTime(queued), parseTime(started), parseTime(finished)
		e.Length = seconds(length)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ShowAllTracks returns whether a show was said to be in a foreign
// language (keep every track); ok is false if it hasn't been asked.
func (s *Store) ShowAllTracks(ctx context.Context, show string) (all, ok bool, err error) {
	err = s.db.QueryRowContext(ctx, "SELECT all_tracks FROM show_tracks WHERE show = ?", show).Scan(&all)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	return all, err == nil, err
}

// SetShowAllTracks records the answer for a show.
func (s *Store) SetShowAllTracks(ctx context.Context, show string, all bool) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO show_tracks(show, all_tracks) VALUES(?, ?) ON CONFLICT(show) DO UPDATE SET all_tracks = excluded.all_tracks",
		show, all)
	return err
}
